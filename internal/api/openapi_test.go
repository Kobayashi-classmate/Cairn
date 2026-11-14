// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/authz"
)

type base struct {
	ID int64 `json:"id"`
}

type thing struct {
	base
	Name    string            `json:"name"`
	Tags    []string          `json:"tags,omitempty"`
	Parent  *thing            `json:"parent"`
	Labels  map[string]string `json:"labels"`
	Secret  string            `json:"-"`
	private int               //nolint:unused // proves unexported fields stay out of the schema
}

type thingResponse struct {
	Thing thing `json:"thing"`
}

func TestOpenAPIFromRegistry(t *testing.T) {
	rt := NewRouter(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	noop := func(http.ResponseWriter, *http.Request) error { return nil }
	rt.Handle("GET /api/v1/meta", noop, Public(), Doc("core", "Instance metadata"))
	rt.Handle("PATCH /api/v1/things/{id}", noop, Require(authz.UsersManage), StepUp(),
		Doc("things", "Update a thing"), Body(thing{}), Returns(200, thingResponse{}))
	rt.Handle("DELETE /api/v1/things/{id}", noop, Require(authz.UsersManage), Returns(204, nil))

	doc := rt.OpenAPI("Cairn", "1.2.3")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		OpenAPI string                               `json:"openapi"`
		Paths   map[string]map[string]map[string]any `json:"paths"`
		Comp    struct {
			Schemas map[string]map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.OpenAPI != "3.1.0" {
		t.Fatalf("version %s", d.OpenAPI)
	}
	meta := d.Paths["/api/v1/meta"]["get"]
	if _, has := meta["x-cairn-permission"]; has || meta["summary"] != "Instance metadata" {
		t.Fatalf("public op: %v", meta)
	}
	patch := d.Paths["/api/v1/things/{id}"]["patch"]
	if patch["x-cairn-permission"] != "users.manage" || patch["x-cairn-step-up"] != true || patch["operationId"] != "patch_things_by_id" {
		t.Fatalf("patch op: %v", patch)
	}
	if params := patch["parameters"].([]any); len(params) != 1 || params[0].(map[string]any)["name"] != "id" {
		t.Fatalf("path params: %v", patch["parameters"])
	}
	if _, ok := d.Paths["/api/v1/things/{id}"]["delete"]["responses"].(map[string]any)["204"]; !ok {
		t.Fatal("204 response missing")
	}

	s := d.Comp.Schemas["api.thing"]
	props := s["properties"].(map[string]any)
	for _, k := range []string{"id", "name", "tags", "parent", "labels"} {
		if _, ok := props[k]; !ok {
			t.Errorf("property %s missing (embedded fields must be flattened)", k)
		}
	}
	for _, k := range []string{"Secret", "-", "private"} {
		if _, ok := props[k]; ok {
			t.Errorf("property %s must not be documented", k)
		}
	}
	req := map[string]bool{}
	for _, r := range s["required"].([]any) {
		req[r.(string)] = true
	}
	if !req["name"] || req["tags"] || req["parent"] {
		t.Fatalf("required fields: %v", s["required"])
	}
	// Self-reference resolves through $ref instead of recursing forever.
	parent := props["parent"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)
	if parent["$ref"] != "#/components/schemas/api.thing" {
		t.Fatalf("recursive ref: %v", parent)
	}
}
