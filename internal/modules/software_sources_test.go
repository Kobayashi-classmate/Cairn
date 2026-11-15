// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/sources"
)

func (f *fakeHelper) sent(action string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.requests[action])
}

func TestSoftwareProbeAndSourcePolicy(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")

	// The policy defaults to the recommended (fastest matching) source.
	if r := admin.expect(admin.do("GET", "/api/v1/software/settings", nil), 200, ""); r.body["downloadPolicy"] != "recommended" {
		t.Fatalf("default policy: %v", r.body)
	}
	admin.expect(admin.do("PUT", "/api/v1/software/settings", map[string]any{"downloadPolicy": "fastest"}), 400, "validation.invalid")
	admin.expect(admin.do("PUT", "/api/v1/software/settings", map[string]any{"downloadPolicy": "official"}), 200, "")

	slots := helperapi.ProbeResult{Took: 1200, Slots: []helperapi.SourceSlot{{
		ID: "runtime:nodejs:24", Kind: helperapi.SlotRuntime, Software: []string{"nodejs"}, Version: "24.22.3",
		Selectable: true, Selected: sources.IDOfficial,
		Candidates: []sources.Candidate{{ID: sources.IDOfficial, Kind: sources.KindOfficial, Status: sources.StatusInSync, Eligible: true, Recommended: true}},
	}}}
	e.helper.respond(helperapi.ActionPkgProbe, slots)
	r := admin.expect(admin.do("POST", "/api/v1/software/probe", map[string]any{"items": []map[string]any{{"software": "nodejs", "line": "24"}}}), 200, "")
	if r.body["policy"] != "official" || len(r.body["slots"].([]any)) != 1 {
		t.Fatalf("probe: %v", r.body)
	}
	var sent helperapi.ProbeRequest
	if err := json.Unmarshal([]byte(e.helper.sent(helperapi.ActionPkgProbe)), &sent); err != nil || sent.Policy != helperapi.PolicyOfficial ||
		len(sent.Items) != 1 || sent.Items[0].Software != "nodejs" {
		t.Fatalf("sent to the helper: %+v %v", sent, err)
	}
	admin.expect(admin.do("POST", "/api/v1/software/probe", map[string]any{"items": []map[string]any{}}), 400, "validation.invalid")
	admin.expect(admin.do("POST", "/api/v1/software/probe", map[string]any{"items": []map[string]any{{"software": "nope", "line": "1"}}}), 404, "request.not_found")
	admin.expect(admin.do("POST", "/api/v1/software/probe", map[string]any{"items": []map[string]any{{"software": "php", "line": "7.4"}}}), 409, helperapi.CodePkgNotAvailable)

	// The chosen source and the policy go to the helper with the install.
	e.helper.respond(helperapi.ActionPkgInstall, helperapi.OpRef{ID: opID})
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{Op: helperapi.Op{ID: opID, State: helperapi.OpSucceeded}})
	id := admin.expect(admin.do("POST", "/api/v1/software/nodejs/install", map[string]any{"line": "24", "source": "official"}), 202, "").obj("job")["id"].(string)
	waitJob(t, admin, id)
	if got := e.helper.sent(helperapi.ActionPkgInstall); got != `{"software":"nodejs","line":"24","source":"official","policy":"official"}` {
		t.Fatalf("install request: %s", got)
	}
	admin.expect(admin.do("POST", "/api/v1/software/nodejs/install", map[string]any{"line": "24", "source": "Not An ID"}), 400, "validation.invalid")
	admin.elevate("")
	admin.expect(admin.do("POST", "/api/v1/software/nodejs/remove", map[string]any{"line": "24", "source": "official"}), 400, "validation.invalid")
	var audited int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'software.install' AND params LIKE '%"source":"official"%'`).Scan(&audited)
	if audited != 1 {
		t.Fatalf("audited installs with a source: %d", audited)
	}

	// Auditors read the policy but cannot change it or probe.
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "aud", "password": pw, "role": "auditor"}), 201, "")
	aud := e.client()
	aud.expect(aud.login("aud", pw), 200, "")
	aud.expect(aud.do("GET", "/api/v1/software/settings", nil), 200, "")
	aud.expect(aud.do("PUT", "/api/v1/software/settings", map[string]any{"downloadPolicy": "recommended"}), 403, "auth.forbidden")
	aud.expect(aud.do("POST", "/api/v1/software/probe", map[string]any{"items": []map[string]any{{"software": "nodejs", "line": "24"}}}), 403, "auth.forbidden")
	if !strings.Contains(e.helper.sent(helperapi.ActionPkgProbe), "nodejs") {
		t.Fatal("probe request lost")
	}
}
