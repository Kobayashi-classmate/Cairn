// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
)

const opID = "0123456789abcdef01234567"

func (f *fakeHelper) respond(action string, v any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.responses == nil {
		f.responses = map[string]any{}
	}
	f.responses[action] = v
}

// waitJob follows a job's log with long polling until it ends.
func waitJob(t *testing.T, c *client, id string) (map[string]any, string) {
	t.Helper()
	var log strings.Builder
	offset := 0.0
	for range 50 {
		r := c.expect(c.do("GET", fmt.Sprintf("/api/v1/jobs/%s/log?offset=%d&wait=2", id, int64(offset)), nil), 200, "")
		log.WriteString(r.body["log"].(string))
		offset = r.body["offset"].(float64)
		job := r.obj("job")
		if jobs.Done(job["state"].(string)) && offset >= r.body["logSize"].(float64) {
			return job, log.String()
		}
	}
	t.Fatalf("job %s did not finish", id)
	return nil, ""
}

func TestSoftwareRefreshJob(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionPkgRefresh, helperapi.OpRef{ID: opID})
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{
		Op:  helperapi.Op{ID: opID, Kind: helperapi.ActionPkgRefresh, State: helperapi.OpSucceeded},
		Log: "$ apt-get update\nHit:1 http://deb.debian.org/debian trixie InRelease\n", Offset: 66, LogSize: 66,
	})
	r := admin.expect(admin.do("POST", "/api/v1/software/refresh", nil), 202, "")
	job := r.obj("job")
	id := job["id"].(string)
	if job["kind"] != "software.refresh" || job["username"] != "admin" || job["source"] != "web" {
		t.Fatalf("job: %v", job)
	}
	final, log := waitJob(t, admin, id)
	if final["state"] != jobs.Succeeded || !strings.Contains(log, "Hit:1 http://deb.debian.org") || !strings.Contains(log, "Refreshing the package indexes") {
		t.Fatalf("final %v, log %q", final, log)
	}
	list := admin.expect(admin.do("GET", "/api/v1/jobs", nil), 200, "").body["jobs"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != id {
		t.Fatalf("list: %v", list)
	}
	var results []string
	rows, _ := e.db.Query(`SELECT action || ':' || result FROM audit_log WHERE action IN ('software.refresh', 'job.finish') ORDER BY id`)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		results = append(results, s)
	}
	rows.Close()
	if strings.Join(results, ",") != "software.refresh:ok,job.finish:ok" {
		t.Fatalf("audit: %v", results)
	}

	// An auditor sees the job (software.read) but may not cancel or start one.
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "aud", "password": pw, "role": "auditor"}), 403, "auth.step_up_required")
	admin.elevate("")
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "aud", "password": pw, "role": "auditor"}), 201, "")
	aud := e.client()
	aud.expect(aud.login("aud", pw), 200, "")
	aud.expect(aud.do("GET", "/api/v1/jobs/"+id, nil), 200, "")
	aud.expect(aud.do("POST", "/api/v1/jobs/"+id+"/cancel", nil), 403, "auth.forbidden")
	aud.expect(aud.do("POST", "/api/v1/software/refresh", nil), 403, "auth.forbidden")

	// A token limited to system.read cannot see software jobs at all.
	tok := admin.expect(admin.do("POST", "/api/v1/me/tokens", map[string]any{"name": "ro", "permissions": []string{"system.read"}, "expiresInDays": 1}), 201, "").body["token"].(string)
	if r := e.bearer(tok, "GET", "/api/v1/jobs/"+id, nil); r.status != 404 {
		t.Fatalf("token saw the job: %d", r.status)
	}
	if r := e.bearer(tok, "GET", "/api/v1/jobs", nil); len(r.body["jobs"].([]any)) != 0 {
		t.Fatalf("token listed jobs: %v", r.body)
	}
	admin.expect(admin.do("GET", "/api/v1/jobs/zzz", nil), 404, "request.not_found")
}

func TestJobFailureCancelAndRestart(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	var mu sync.Mutex
	var failed []events.Event
	e.bus.Subscribe(func(ev events.Event) {
		if ev.Type == jobs.EventFailed {
			mu.Lock()
			failed = append(failed, ev)
			mu.Unlock()
		}
	})

	// A failing operation fails the job with the operation's error code.
	e.helper.respond(helperapi.ActionPkgRefresh, helperapi.OpRef{ID: opID})
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{
		Op: helperapi.Op{ID: opID, State: helperapi.OpFailed, Error: &helperapi.Error{Code: helperapi.CodeOpFailed, Params: map[string]any{"reason": "exit 100"}}},
	})
	id := admin.expect(admin.do("POST", "/api/v1/software/refresh", nil), 202, "").obj("job")["id"].(string)
	final, _ := waitJob(t, admin, id)
	if final["state"] != jobs.Failed || final["error"].(map[string]any)["code"] != helperapi.CodeOpFailed {
		t.Fatalf("failed job: %v", final)
	}
	mu.Lock()
	if len(failed) != 1 || failed[0].Params["kind"] != "software.refresh" {
		t.Fatalf("failure event: %v", failed)
	}
	mu.Unlock()

	// While a job follows an operation the helper decides about canceling.
	release := make(chan struct{})
	rec, err := e.jobs.Start(context.Background(), jobs.Spec{Kind: "test.wait", Permission: authz.SystemRead, ManagePermission: authz.SettingsManage},
		func(ctx context.Context, j *jobs.Job) error {
			j.Logf("waiting\n")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	admin.expect(admin.do("POST", "/api/v1/jobs/"+rec.ID+"/cancel", nil), 202, "")
	final, log := waitJob(t, admin, rec.ID)
	if final["state"] != jobs.Canceled || !strings.Contains(log, "waiting") {
		t.Fatalf("canceled job: %v %q", final, log)
	}
	admin.expect(admin.do("POST", "/api/v1/jobs/"+rec.ID+"/cancel", nil), 409, jobs.CodeNotCancelable)

	// The helper refuses to cancel an installation in progress.
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{Op: helperapi.Op{ID: opID, State: helperapi.OpRunning}})
	e.helper.mu.Lock()
	e.helper.errs = map[string]error{helperapi.ActionOpCancel: &helperapi.Error{Code: helperapi.CodeOpNotCancelable}}
	// Forget the earlier jobs' calls: the wait below must see this job's
	// first op.get, which comes after it recorded its operation.
	e.helper.calls = nil
	e.helper.mu.Unlock()
	rec, _ = e.jobs.Start(context.Background(), jobs.Spec{Kind: "test.op", Permission: authz.SystemRead, ManagePermission: authz.SettingsManage},
		func(_ context.Context, j *jobs.Job) error { return j.RunOp(helperapi.ActionPkgRefresh, nil, nil) })
	deadline := time.Now().Add(5 * time.Second)
	for !e.helper.called(helperapi.ActionOpGet) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	admin.expect(admin.do("POST", "/api/v1/jobs/"+rec.ID+"/cancel", nil), 409, jobs.CodeNotCancelable)
	close(release)
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{Op: helperapi.Op{ID: opID, State: helperapi.OpSucceeded}})
	if final, _ := waitJob(t, admin, rec.ID); final["state"] != jobs.Succeeded {
		t.Fatalf("op job: %v", final)
	}

	// Jobs left unfinished by a previous web process are interrupted.
	if _, err := e.db.Exec(`INSERT INTO jobs (id, kind, permission, manage_permission, state, created_at) VALUES (?, 'x', 'system.read', 'settings.manage', 'running', ?)`,
		"aaaaaaaaaaaaaaaaaaaaaaaa", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	m2 := &jobs.Manager{DB: e.db, Dir: t.TempDir(), Helper: e.helper, Log: e.jobs.Log}
	if err := m2.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := admin.expect(admin.do("GET", "/api/v1/jobs/aaaaaaaaaaaaaaaaaaaaaaaa", nil), 200, "").obj("job"); r["state"] != jobs.Interrupted ||
		r["error"].(map[string]any)["code"] != jobs.CodeInterrupted {
		t.Fatalf("not interrupted: %v", r)
	}
}

// demo is an optional module for the module switch tests.
type demo struct{}

func (demo) ID() string { return "demo" }
func (demo) Info() module.Info {
	return module.Info{
		Optional:    true,
		Permissions: []module.Permission{{Name: "demo.read", Roles: []authz.Role{authz.Operator}}},
		Nav:         []module.NavItem{{Key: "demo", LabelKey: "demo", Icon: "apps", Route: "demo", Permission: "demo.read", Order: 50}},
	}
}
func (demo) Routes(r *api.Router) {
	r.Handle("GET /api/v1/demo", func(w http.ResponseWriter, _ *http.Request) error {
		return api.WriteJSON(w, 200, map[string]any{"ok": true})
	}, api.Require("demo.read"), api.Doc("demo", "Demo"), api.Returns(200, nil))
}

func navKeys(t *testing.T, c *client) string {
	t.Helper()
	var keys []string
	for _, it := range c.expect(c.do("GET", "/api/v1/ui/nav", nil), 200, "").body["items"].([]any) {
		keys = append(keys, it.(map[string]any)["key"].(string))
	}
	return strings.Join(keys, ",")
}

func TestNavAndModuleSwitches(t *testing.T) {
	e := newEnv(t, demo{})
	admin := e.setupAdmin("admin")
	if got := navKeys(t, admin); got != "overview,sites,software,demo,jobs,users,messages,audit,settings" {
		t.Fatalf("admin menu: %s", got)
	}
	admin.elevate("")
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "aud", "password": pw, "role": "auditor"}), 201, "")
	aud := e.client()
	aud.expect(aud.login("aud", pw), 200, "")
	if got := navKeys(t, aud); got != "overview,sites,software,jobs,messages,audit,settings" {
		t.Fatalf("auditor menu: %s", got)
	}
	// Module permissions reach the role defaults and /auth/me.
	me := aud.expect(aud.do("GET", "/api/v1/auth/me", nil), 200, "")
	if perms := fmt.Sprint(me.body["permissions"]); !strings.Contains(perms, "software.read") || strings.Contains(perms, "software.manage") {
		t.Fatalf("auditor permissions: %s", perms)
	}

	mods := admin.expect(admin.do("GET", "/api/v1/modules", nil), 200, "").body["modules"].([]any)
	if len(mods) != 10 || fmt.Sprint(mods[9]) != "map[enabled:true id:demo optional:true]" {
		t.Fatalf("modules: %v", mods)
	}
	admin.expect(admin.do("PUT", "/api/v1/modules/core", map[string]any{"enabled": false}), 409, "module.not_optional")
	admin.expect(admin.do("PUT", "/api/v1/modules/nope", map[string]any{"enabled": false}), 404, "request.not_found")
	admin.expect(admin.do("GET", "/api/v1/demo", nil), 200, "")
	admin.expect(admin.do("PUT", "/api/v1/modules/demo", map[string]any{"enabled": false}), 200, "")
	admin.expect(admin.do("GET", "/api/v1/demo", nil), 404, "module.disabled")
	if got := navKeys(t, admin); strings.Contains(got, "demo") {
		t.Fatalf("disabled module still in the menu: %s", got)
	}
	// The choice is stored.
	reg := module.NewRegistry(e.db)
	reg.Add(demo{})
	if err := reg.Load(context.Background()); err != nil || reg.Enabled("demo") {
		t.Fatalf("not persisted: %v", err)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'module.disable' AND result = 'ok'`).Scan(&n)
	if n != 1 {
		t.Fatalf("module switch audited %d times", n)
	}
}

func TestSoftwareCatalogAPI(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	r := admin.expect(admin.do("GET", "/api/v1/software", nil), 200, "")
	if r.body["release"] != "debian-13" || r.body["supported"] != true {
		t.Fatalf("system: %v", r.body)
	}
	var php map[string]any
	for _, s := range r.body["software"].([]any) {
		if s.(map[string]any)["id"] == "php" {
			php = s.(map[string]any)
		}
	}
	if php == nil || php["name"] != "PHP" || php["category"] != "runtime" {
		t.Fatalf("php: %v", php)
	}
	recommended := ""
	for _, o := range php["offers"].([]any) {
		if o.(map[string]any)["recommended"] == true {
			recommended = o.(map[string]any)["line"].(string)
		}
	}
	if recommended != "8.4" {
		t.Fatalf("recommended php on debian-13: %q", recommended)
	}
	if repos := fmt.Sprint(r.body["repos"]); !strings.Contains(repos, "official:false") || strings.Contains(repos, "approved") {
		t.Fatalf("repos: %s", repos)
	}
}

func TestJobPagingAndCancelAudit(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	noop := func(context.Context, *jobs.Job) error { return nil }
	// Many jobs in the same second, most of them invisible to an auditor.
	var visible []string
	for i := range 25 {
		perm := authz.SettingsManage // admins only
		if i%5 == 0 {
			perm = authz.SystemRead
		}
		rec, err := e.jobs.Start(context.Background(), jobs.Spec{Kind: "test.page", Permission: perm, ManagePermission: authz.SettingsManage}, noop)
		if err != nil {
			t.Fatal(err)
		}
		if perm == authz.SystemRead {
			visible = append([]string{rec.ID}, visible...)
		}
	}
	page := func(c *client, query string) ([]string, string) {
		r := c.expect(c.do("GET", "/api/v1/jobs"+query, nil), 200, "")
		var ids []string
		for _, j := range r.body["jobs"].([]any) {
			ids = append(ids, j.(map[string]any)["id"].(string))
		}
		next := ""
		if n, ok := r.body["nextBefore"].(float64); ok {
			next = fmt.Sprint(int64(n))
		}
		return ids, next
	}
	var all []string
	next := ""
	for {
		q := "?limit=7"
		if next != "" {
			q += "&before=" + next
		}
		ids, n := page(admin, q)
		all = append(all, ids...)
		if n == "" {
			break
		}
		next = n
	}
	if len(all) != 25 {
		t.Fatalf("admin paged %d jobs", len(all))
	}
	seen := map[string]bool{}
	for _, id := range all {
		if seen[id] {
			t.Fatalf("job %s listed twice", id)
		}
		seen[id] = true
	}
	admin.elevate("")
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "aud", "password": pw, "role": "auditor"}), 201, "")
	aud := e.client()
	aud.expect(aud.login("aud", pw), 200, "")
	// A page of 2 among mostly invisible rows is still full, and the last
	// page ends the listing.
	ids, n := page(aud, "?limit=2")
	if len(ids) != 2 || n == "" || ids[0] != visible[0] {
		t.Fatalf("auditor first page: %v %q (want %v)", ids, n, visible[:2])
	}
	ids, n = page(aud, "?limit=10&before="+n)
	if len(ids) != 3 || n != "" {
		t.Fatalf("auditor rest: %v %q", ids, n)
	}

	// A refused cancel is audited with the job's kind.
	admin.expect(admin.do("POST", "/api/v1/jobs/"+all[0]+"/cancel", nil), 409, jobs.CodeNotCancelable)
	var params string
	_ = e.db.QueryRow(`SELECT params FROM audit_log WHERE action = 'job.cancel' ORDER BY id DESC LIMIT 1`).Scan(&params)
	if !strings.Contains(params, `"kind":"test.page"`) || !strings.Contains(params, all[0]) {
		t.Fatalf("cancel audit params: %s", params)
	}
}

func TestSoftwareInstallAndRemove(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	result, _ := json.Marshal(helperapi.InstallResult{Software: "php", Line: "8.4", Method: "system",
		Packages: []helperapi.InstalledPackage{{Name: "php8.4-fpm", Version: "8.4.27-1~deb13u1"}}})
	e.helper.respond(helperapi.ActionPkgInstall, helperapi.OpRef{ID: opID})
	e.helper.respond(helperapi.ActionPkgRemove, helperapi.OpRef{ID: opID})
	e.helper.respond(helperapi.ActionOpGet, helperapi.OpGetResponse{Op: helperapi.Op{ID: opID, State: helperapi.OpSucceeded, Result: result}})

	r := admin.expect(admin.do("POST", "/api/v1/software/php/install", map[string]any{"line": "8.4"}), 202, "")
	job := r.obj("job")
	if job["kind"] != "software.install" || job["params"].(map[string]any)["name"] != "PHP" {
		t.Fatalf("job: %v", job)
	}
	final, log := waitJob(t, admin, job["id"].(string))
	if final["state"] != jobs.Succeeded || !strings.Contains(log, "php8.4-fpm 8.4.27-1~deb13u1") {
		t.Fatalf("install job: %v %q", final, log)
	}
	// The helper is told only which entry and line, nothing else.
	e.helper.mu.Lock()
	sent := string(e.helper.requests[helperapi.ActionPkgInstall])
	e.helper.mu.Unlock()
	if sent != `{"software":"php","line":"8.4"}` {
		t.Fatalf("sent to the helper: %s", sent)
	}
	// Prebuilt runtimes go through the same operation.
	admin.expect(admin.do("POST", "/api/v1/software/nodejs/install", map[string]any{"line": "24"}), 202, "")

	for _, tc := range []struct {
		path, line string
		status     int
		code       string
		reason     any
	}{
		{"/api/v1/software/php/install", "7.4", 409, helperapi.CodePkgNotAvailable, "end_of_life"},
		{"/api/v1/software/nodejs/install", "20", 409, helperapi.CodePkgNotAvailable, "end_of_life"},
		{"/api/v1/software/php/install", "9.9", 404, "request.not_found", nil},
		{"/api/v1/software/nope/install", "1", 404, "request.not_found", nil},
	} {
		r := admin.expect(admin.do("POST", tc.path, map[string]any{"line": tc.line}), tc.status, tc.code)
		if tc.reason != nil && r.params()["reason"] != tc.reason {
			t.Errorf("%s %s: params %v", tc.path, tc.line, r.params())
		}
	}

	// Removing needs a recent re-authentication.
	admin.expect(admin.do("POST", "/api/v1/software/php/remove", map[string]any{"line": "8.4"}), 403, "auth.step_up_required")
	admin.elevate("")
	id := admin.expect(admin.do("POST", "/api/v1/software/php/remove", map[string]any{"line": "8.4"}), 202, "").obj("job")["id"].(string)
	if final, _ := waitJob(t, admin, id); final["state"] != jobs.Succeeded || final["kind"] != "software.remove" {
		t.Fatalf("remove job: %v", final)
	}
	var audited int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action IN ('software.install', 'software.remove') AND result = 'ok'`).Scan(&audited)
	if audited != 3 { // php install, nodejs install, php remove
		t.Fatalf("audited %d software actions", audited)
	}

	// Installed lines come from the helper's status.
	e.helper.respond(helperapi.ActionPkgStatus, helperapi.SoftwareStatus{Installed: map[string]map[string][]helperapi.InstalledPackage{
		"php": {"8.4": {{Name: "php8.4-fpm", Version: "8.4.27-1~deb13u1"}}}}})
	list := admin.expect(admin.do("GET", "/api/v1/software", nil), 200, "")
	installed := map[string]bool{}
	for _, sw := range list.body["software"].([]any) {
		if sw.(map[string]any)["id"] != "php" {
			continue
		}
		for _, o := range sw.(map[string]any)["offers"].([]any) {
			if o.(map[string]any)["installed"] == true {
				installed[o.(map[string]any)["line"].(string)] = true
			}
		}
	}
	if list.body["statusAvailable"] != true || len(installed) != 1 || !installed["8.4"] {
		t.Fatalf("installed state: %v %v", list.body["statusAvailable"], installed)
	}
	e.helper.mu.Lock()
	e.helper.errs = map[string]error{helperapi.ActionPkgStatus: &helperapi.Error{Code: helperapi.CodeUnavailable}}
	e.helper.mu.Unlock()
	if r := admin.expect(admin.do("GET", "/api/v1/software", nil), 200, ""); r.body["statusAvailable"] != false {
		t.Fatalf("helper down: %v", r.body["statusAvailable"])
	}
}
