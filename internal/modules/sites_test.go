// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
)

func TestSitesAPI(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionSiteStatus, helperapi.SiteStatus{Supported: true, PHP: []string{"8.4"}, Sites: map[string]helperapi.SiteState{}})
	e.helper.respond(helperapi.ActionSiteApply, helperapi.OpRef{ID: opID})
	e.helper.respond(helperapi.ActionSiteRemove, helperapi.OpRef{ID: opID})
	result, _ := json.Marshal(helperapi.SiteResult{Code: "example-com", Dir: "/srv/www/example-com", Owner: "cw-example-com", Runner: "cw-example-com-run"})
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{Op: helperapi.Op{ID: opID, State: helperapi.OpSucceeded, Result: result}})

	// The code comes from the first domain.
	r := admin.expect(admin.do("POST", "/api/v1/sites", map[string]any{"type": "php", "domains": []string{"www.Example.com", "example.com"},
		"php": "8.4", "rewrite": "wordpress", "writable": []string{"wp-content/uploads"}, "allowSymlinks": true}), 202, "")
	job := r.obj("job")
	if job["kind"] != "site.create" || job["params"].(map[string]any)["site"] != "example-com" {
		t.Fatalf("job: %v", job)
	}
	if final, _ := waitJob(t, admin, job["id"].(string)); final["state"] != jobs.Succeeded {
		t.Fatalf("create job: %v", final)
	}
	var sent helperapi.SiteSpec
	_ = json.Unmarshal([]byte(e.helper.sent(helperapi.ActionSiteApply)), &sent)
	if sent.Code != "example-com" || sent.Domains[0] != "www.example.com" || !sent.Enabled || sent.Rewrite != "wordpress" || !sent.AllowSymlinks {
		t.Fatalf("sent: %+v", sent)
	}
	site := admin.expect(admin.do("GET", "/api/v1/sites/example-com", nil), 200, "")
	if site.body["state"] != "active" || site.body["owner"] != "cw-example-com" || site.body["drift"] != true {
		t.Fatalf("site: %v", site.body)
	}

	// Domains are unique; a second site from the same domain gets "-2".
	admin.expect(admin.do("POST", "/api/v1/sites", map[string]any{"type": "static", "domains": []string{"example.com"}}), 409, helperapi.CodeSiteDomainTaken)
	r = admin.expect(admin.do("POST", "/api/v1/sites", map[string]any{"type": "static", "domains": []string{"www.example.com.cn"}}), 202, "")
	if code := r.obj("job")["params"].(map[string]any)["site"]; code != "example-com-cn" {
		t.Fatalf("derived code: %v", code)
	}
	r = admin.expect(admin.do("POST", "/api/v1/sites", map[string]any{"type": "static", "domains": []string{"example-com.org"}}), 202, "")
	if code := r.obj("job")["params"].(map[string]any)["site"]; code != "example-com-org" {
		t.Fatalf("derived code: %v", code)
	}
	for name, body := range map[string]map[string]any{
		"bad domain":           {"type": "static", "domains": []string{"a b.com"}},
		"proxy without target": {"type": "proxy", "domains": []string{"p.example.com"}},
		"rewrite of php":       {"type": "static", "domains": []string{"s.example.com"}, "rewrite": "wordpress"},
		"bad code":             {"code": "Bad", "type": "static", "domains": []string{"s.example.com"}},
	} {
		if r := admin.do("POST", "/api/v1/sites", body); r.status != 400 {
			t.Errorf("%s: %d %v", name, r.status, r.body)
		}
	}

	// Changes keep the code and the type.
	admin.expect(admin.do("PUT", "/api/v1/sites/example-com", map[string]any{"type": "static", "domains": []string{"example.com"}}), 400, "validation.invalid")
	admin.expect(admin.do("PUT", "/api/v1/sites/example-com", map[string]any{"type": "php", "domains": []string{"example.com", "blog.example.com"}, "php": "8.4"}), 202, "")
	admin.expect(admin.do("POST", "/api/v1/sites/example-com/stop", nil), 202, "")
	if r := admin.expect(admin.do("GET", "/api/v1/sites", nil), 200, ""); len(r.body["sites"].([]any)) != 3 || r.body["supported"] != true {
		t.Fatalf("list: %v", r.body)
	}

	// Logs come from the helper.
	e.helper.respond(helperapi.ActionSiteLog, helperapi.SiteLogResponse{Data: "{\"status\":200}\n", Offset: 15, Size: 15})
	if r := admin.expect(admin.do("GET", "/api/v1/sites/example-com/log?kind=error&offset=-1", nil), 200, ""); r.body["offset"] != 15.0 {
		t.Fatalf("log: %v", r.body)
	}
	if !strings.Contains(e.helper.sent(helperapi.ActionSiteLog), `"kind":"error"`) {
		t.Fatalf("log request: %s", e.helper.sent(helperapi.ActionSiteLog))
	}
	admin.expect(admin.do("GET", "/api/v1/sites/example-com/log?kind=debug", nil), 400, "validation.invalid")

	// Deleting needs step-up; the row goes once the helper removed the site.
	admin.expect(admin.do("DELETE", "/api/v1/sites/example-com?deleteFiles=true", nil), 403, "auth.step_up_required")
	admin.elevate("")
	id := admin.expect(admin.do("DELETE", "/api/v1/sites/example-com?deleteFiles=true", nil), 202, "").obj("job")["id"].(string)
	if final, _ := waitJob(t, admin, id); final["state"] != jobs.Succeeded {
		t.Fatalf("delete: %v", final)
	}
	if !strings.Contains(e.helper.sent(helperapi.ActionSiteRemove), `"deleteFiles":true`) {
		t.Fatalf("remove request: %s", e.helper.sent(helperapi.ActionSiteRemove))
	}
	admin.expect(admin.do("GET", "/api/v1/sites/example-com", nil), 404, helperapi.CodeSiteNotFound)
	// Its domains are free again.
	admin.expect(admin.do("POST", "/api/v1/sites", map[string]any{"type": "static", "domains": []string{"example.com"}}), 202, "")

	// A failed change marks the site failed with the helper's code.
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{Op: helperapi.Op{ID: opID, State: helperapi.OpFailed,
		Error: &helperapi.Error{Code: helperapi.CodeSiteConfigInvalid}}})
	id = admin.expect(admin.do("POST", "/api/v1/sites/example-com-cn/stop", nil), 202, "").obj("job")["id"].(string)
	waitJob(t, admin, id)
	if r := admin.expect(admin.do("GET", "/api/v1/sites/example-com-cn", nil), 200, ""); r.body["state"] != "failed" || r.body["errorCode"] != helperapi.CodeSiteConfigInvalid {
		t.Fatalf("failed site: %v", r.body)
	}

	// Auditors read sites but cannot change them.
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "aud", "password": pw, "role": "auditor"}), 201, "")
	aud := e.client()
	aud.expect(aud.login("aud", pw), 200, "")
	aud.expect(aud.do("GET", "/api/v1/sites", nil), 200, "")
	aud.expect(aud.do("GET", "/api/v1/sites/rewrites", nil), 200, "")
	aud.expect(aud.do("POST", "/api/v1/sites/example-com-cn/start", nil), 403, "auth.forbidden")

	// An app's environment values are for those who manage sites: readers
	// see the names only.
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{Op: helperapi.Op{ID: opID, State: helperapi.OpSucceeded, Result: result}})
	waitJob(t, admin, admin.expect(admin.do("POST", "/api/v1/sites", map[string]any{"type": "app", "domains": []string{"app.example.com"},
		"app": map[string]any{"command": "node server.js", "port": 0, "env": []map[string]any{{"name": "DB_PASSWORD", "value": "hunter2-secret"}}}}), 202, "").obj("job")["id"].(string))
	for _, path := range []string{"/api/v1/sites", "/api/v1/sites/app-example-com"} {
		if r := aud.expect(aud.do("GET", path, nil), 200, ""); strings.Contains(r.raw, "hunter2-secret") || !strings.Contains(r.raw, "DB_PASSWORD") {
			t.Fatalf("reader's view of %s: %s", path, r.raw)
		}
		if r := admin.expect(admin.do("GET", path, nil), 200, ""); !strings.Contains(r.raw, "hunter2-secret") {
			t.Fatalf("manager's view of %s lacks the value", path)
		}
	}

	// Helper refusals of a log read keep their code.
	e.helper.fail(helperapi.ActionSiteLog, &helperapi.Error{Code: helperapi.CodeSiteNotFound})
	admin.expect(admin.do("GET", "/api/v1/sites/example-com-cn/log", nil), 409, helperapi.CodeSiteNotFound)
}

// A change interrupted by a restart leaves no site or stream applying
// forever; one whose job still runs stays.
func TestSitesRecoverInterrupted(t *testing.T) {
	e := newEnv(t)
	for _, q := range []string{
		`INSERT INTO jobs (id, kind, permission, manage_permission, state, created_at) VALUES
			('aaaaaaaaaaaaaaaaaaaaaaaa', 'site.update', 'sites.read', 'sites.manage', 'interrupted', 1),
			('bbbbbbbbbbbbbbbbbbbbbbbb', 'site.update', 'sites.read', 'sites.manage', 'running', 1)`,
		`INSERT INTO sites (code, type, spec, state, last_job, created_at, updated_at) VALUES
			('gone', 'static', '{}', 'applying', 'aaaaaaaaaaaaaaaaaaaaaaaa', 1, 1),
			('never', 'static', '{}', 'applying', '', 1, 1),
			('busy', 'static', '{}', 'applying', 'bbbbbbbbbbbbbbbbbbbbbbbb', 1, 1),
			('fine', 'static', '{}', 'active', 'aaaaaaaaaaaaaaaaaaaaaaaa', 1, 1)`,
		`INSERT INTO streams (code, spec, state, last_job, created_at, updated_at) VALUES ('tcp', '{}', 'applying', 'aaaaaaaaaaaaaaaaaaaaaaaa', 1, 1)`,
	} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.sites.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]map[string]string{
		"sites":   {"gone": "failed job.interrupted", "never": "failed job.interrupted", "busy": "applying ", "fine": "active "},
		"streams": {"tcp": "failed job.interrupted"},
	} {
		for code, w := range want {
			var state, errCode string
			_ = e.db.QueryRow(`SELECT state, error_code FROM `+table+` WHERE code = ?`, code).Scan(&state, &errCode)
			if got := state + " " + errCode; got != w {
				t.Errorf("%s %s: %q, want %q", table, code, got, w)
			}
		}
	}
}
