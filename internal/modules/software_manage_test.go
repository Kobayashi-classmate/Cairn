// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

func findOffer(t *testing.T, body map[string]any, id, line string) map[string]any {
	t.Helper()
	for _, s := range body["software"].([]any) {
		s := s.(map[string]any)
		if s["id"] != id {
			continue
		}
		for _, o := range s["offers"].([]any) {
			if o := o.(map[string]any); o["line"] == line {
				return o
			}
		}
	}
	t.Fatalf("no offer %s %s", id, line)
	return nil
}

func TestSoftwareLineManagement(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionPkgStatus, helperapi.SoftwareStatus{
		Installed: map[string]map[string][]helperapi.InstalledPackage{
			"nginx": {"1.26": {{Name: "nginx", Version: "1.26.3-3"}}},
			"php":   {"8.4": {{Name: "php8.4-fpm", Version: "8.4.27-1"}}},
		},
		Services: map[string]string{"nginx": "active", "php8.4-fpm": "failed"}, Reloadable: []string{"nginx"},
	})

	// The list carries the services' states and the editable files.
	list := admin.expect(admin.do("GET", "/api/v1/software", nil), 200, "")
	nginx := findOffer(t, list.body, "nginx", "1.26")
	if fmt.Sprint(nginx["units"]) != "[map[canReload:true name:nginx state:running]]" {
		t.Fatalf("units: %v", nginx["units"])
	}
	if fmt.Sprint(nginx["configs"]) != "[map[apply:reload check:true id:nginx.conf path:/etc/nginx/nginx.conf service:nginx]]" {
		t.Fatalf("configs: %v", nginx["configs"])
	}
	php := findOffer(t, list.body, "php", "8.4")
	if php["extensions"] != true || fmt.Sprint(php["units"]) != "[map[canReload:false name:php8.4-fpm state:failed]]" {
		t.Fatalf("php: %v", php)
	}

	// Services.
	e.opsSucceed(helperapi.ActionPkgService, helperapi.ActionPkgExtApply, helperapi.ActionPkgConfigWrite)
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/software/nginx/service", map[string]any{"line": "1.26", "action": "reload"}), 202, ""))
	if q := sentAs[helperapi.ServiceRequest](t, e, helperapi.ActionPkgService); q.Software != "nginx" || q.Line != "1.26" || q.Action != "reload" {
		t.Fatalf("service: %+v", q)
	}
	admin.expect(admin.do("POST", "/api/v1/software/nginx/service", map[string]any{"line": "1.26", "action": "kill"}), 400, "validation.invalid")
	admin.expect(admin.do("POST", "/api/v1/software/nginx/service", map[string]any{"line": "9.9", "action": "start"}), 404, "request.not_found")

	// Extensions: unknown and built-in ones fail before the job.
	e.helper.respond(helperapi.ActionPkgExtStatus, helperapi.ExtStatus{Modules: []string{"core", "sodium"}, Extensions: []helperapi.ExtensionInfo{
		{ID: "intl", Name: "intl", Modules: []string{"intl"}, Available: true},
		{ID: "sodium", Name: "Sodium", Modules: []string{"sodium"}, Available: true, Loaded: true, Builtin: true},
	}})
	ext := admin.expect(admin.do("GET", "/api/v1/software/php/lines/8.4/extensions", nil), 200, "")
	if len(ext.body["extensions"].([]any)) != 2 || fmt.Sprint(ext.body["modules"]) != "[core sodium]" {
		t.Fatalf("extensions: %v", ext.body)
	}
	admin.expect(admin.do("POST", "/api/v1/software/php/lines/8.4/extensions", map[string]any{"install": []string{"nope"}}), 400, helperapi.CodePkgExtUnknown)
	admin.expect(admin.do("POST", "/api/v1/software/php/lines/8.4/extensions", map[string]any{"remove": []string{"sodium"}}), 400, helperapi.CodePkgExtUnavailable)
	admin.expect(admin.do("POST", "/api/v1/software/php/lines/8.4/extensions", map[string]any{}), 400, "validation.invalid")
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/software/php/lines/8.4/extensions", map[string]any{"install": []string{"intl"}}), 202, ""))
	if q := sentAs[helperapi.ExtApplyRequest](t, e, helperapi.ActionPkgExtApply); q.Line != "8.4" || fmt.Sprint(q.Install) != "[intl]" {
		t.Fatalf("extensions: %+v", q)
	}

	// Configuration files need step-up.
	cfg := "/api/v1/software/nginx/lines/1.26/configs/nginx.conf"
	admin.expect(admin.do("GET", cfg, nil), 403, "auth.step_up_required")
	admin.elevate("")
	e.helper.respond(helperapi.ActionPkgConfigRead, helperapi.ConfigFile{ID: "nginx.conf", Path: "/etc/nginx/nginx.conf",
		Content: "worker_processes 1;\n", SHA256: strings.Repeat("a", 64), Editable: true, Versions: []helperapi.ConfigVersion{}})
	got := admin.expect(admin.do("GET", cfg, nil), 200, "")
	if got.body["content"] != "worker_processes 1;\n" || got.body["editable"] != true {
		t.Fatalf("config: %v", got.body)
	}
	if q := sentAs[helperapi.ConfigRequest](t, e, helperapi.ActionPkgConfigRead); q.Config != "nginx.conf" || q.Version != "" {
		t.Fatalf("read: %+v", q)
	}
	admin.expect(admin.do("GET", cfg+"/versions/123", nil), 200, "")
	if q := sentAs[helperapi.ConfigRequest](t, e, helperapi.ActionPkgConfigRead); q.Version != "123" {
		t.Fatalf("version: %+v", q)
	}
	admin.expect(admin.do("GET", cfg+"/versions/x1", nil), 400, "validation.invalid")
	admin.expect(admin.do("PUT", cfg, map[string]any{"content": "x;\n", "base": strings.Repeat("b", 64)}), 409, helperapi.CodePkgConfigChanged)
	admin.expect(admin.do("PUT", cfg, map[string]any{"content": "x;\n", "base": "short"}), 400, "validation.invalid")
	admin.expect(admin.do("PUT", cfg, map[string]any{"content": strings.Repeat("x", helperapi.ConfigMax+1), "base": strings.Repeat("a", 64)}), 400, helperapi.CodePkgConfigTooLarge)
	admin.runJob(admin.expect(admin.do("PUT", cfg, map[string]any{"content": "worker_processes 2;\n", "base": strings.Repeat("a", 64)}), 202, ""))
	if q := sentAs[helperapi.ConfigWriteRequest](t, e, helperapi.ActionPkgConfigWrite); q.Content != "worker_processes 2;\n" || q.Config != "nginx.conf" {
		t.Fatalf("write: %+v", q)
	}
	// A server that keeps the files read-only refuses before the job.
	e.helper.respond(helperapi.ActionPkgConfigRead, helperapi.ConfigFile{ID: "nginx.conf", SHA256: strings.Repeat("a", 64), Editable: false})
	admin.expect(admin.do("PUT", cfg, map[string]any{"content": "y;\n", "base": strings.Repeat("a", 64)}), 403, helperapi.CodePkgConfigLocked)
	e.helper.fail(helperapi.ActionPkgConfigRead, &helperapi.Error{Code: helperapi.CodePkgConfigUnknown})
	admin.expect(admin.do("GET", "/api/v1/software/nginx/lines/1.26/configs/other.conf", nil), 404, helperapi.CodePkgConfigUnknown)

	// Auditors read the list but cannot open configuration files.
	audits := admin.expect(admin.do("GET", "/api/v1/audit?limit=50", nil), 200, "")
	var kinds []string
	for _, ev := range audits.body["entries"].([]any) {
		kinds = append(kinds, ev.(map[string]any)["action"].(string))
	}
	for _, k := range []string{"software.service", "software.extensions", "software.config"} {
		if !strings.Contains(strings.Join(kinds, ","), k) {
			t.Errorf("audit %s missing: %v", k, kinds)
		}
	}

	// Writing nginx or PHP configuration is as good as root: operators,
	// who manage software otherwise, neither read nor write the files.
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "oper", "password": pw, "role": "operator"}), 201, "")
	op := e.client()
	op.expect(op.login("oper", pw), 200, "")
	op.elevate("")
	if perms := fmt.Sprint(op.expect(op.do("GET", "/api/v1/auth/me", nil), 200, "").body["permissions"]); !strings.Contains(perms, "software.manage") ||
		strings.Contains(perms, "software.config") {
		t.Fatalf("operator permissions %s", perms)
	}
	op.expect(op.do("GET", cfg, nil), 403, "auth.forbidden")
	op.expect(op.do("GET", cfg+"/versions/123", nil), 403, "auth.forbidden")
	op.expect(op.do("PUT", cfg, map[string]any{"content": "load_module /tmp/x.so;\n", "base": strings.Repeat("a", 64)}), 403, "auth.forbidden")

	// Probe refusals keep their codes instead of becoming internal errors.
	e.helper.fail(helperapi.ActionPkgProbe, &helperapi.Error{Code: helperapi.CodePkgBusy})
	admin.expect(admin.do("POST", "/api/v1/software/probe", map[string]any{"items": []any{map[string]any{"software": "nodejs", "line": "24"}}}), 409, helperapi.CodePkgBusy)
}

// A line that is installed stays managed after its upstream support has
// ended; only installing it anew is refused. Debian 13 on 2027-01-01: PHP
// 8.2 (sury) ended the day before, PostgreSQL 14 (PGDG) weeks earlier, PHP
// 8.3 (sury) is still supported and 8.4 is Debian's own.
func TestSoftwareEndOfLifeLines(t *testing.T) {
	e := newEnv(t)
	e.software.Now = func() time.Time { return time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC) }
	admin := e.setupAdmin("admin")
	admin.elevate("")
	e.helper.respond(helperapi.ActionPkgStatus, helperapi.SoftwareStatus{
		Installed: map[string]map[string][]helperapi.InstalledPackage{
			"php":        {"8.2": {{Name: "php8.2-fpm", Version: "8.2.30-1"}}, "8.4": {{Name: "php8.4-fpm", Version: "8.4.27-1"}}},
			"postgresql": {"14": {{Name: "postgresql-14", Version: "14.20-1.pgdg13+1"}}},
		},
		Services: map[string]string{"php8.2-fpm": "active", "php8.4-fpm": "active", "postgresql": "active"},
	})

	// The list: installed, with its services and files, not installable.
	list := admin.expect(admin.do("GET", "/api/v1/software", nil), 200, "")
	for _, ref := range [][2]string{{"php", "8.2"}, {"postgresql", "14"}} {
		o := findOffer(t, list.body, ref[0], ref[1])
		if o["method"] != "vendor" || o["installed"] != true || o["installable"] != false || o["endOfLife"] != true || o["reason"] != "end_of_life" ||
			o["eol"] == nil || o["recommended"] != false || o["units"] == nil || o["configs"] == nil {
			t.Errorf("%s %s: %v", ref[0], ref[1], o)
		}
	}
	if o := findOffer(t, list.body, "php", "8.2"); fmt.Sprint(o["units"]) != "[map[canReload:false name:php8.2-fpm state:running]]" || o["extensions"] != true {
		t.Errorf("php 8.2: %v", o)
	}
	// Other lines are as before: a supported vendor line, the system's
	// line (recommended), and an ended line that is not installed.
	if o := findOffer(t, list.body, "php", "8.3"); o["method"] != "vendor" || o["installable"] != true || o["endOfLife"] != false || o["reason"] != nil || o["installed"] != false {
		t.Errorf("php 8.3: %v", o)
	}
	if o := findOffer(t, list.body, "php", "8.4"); o["method"] != "system" || o["installable"] != true || o["recommended"] != true || o["endOfLife"] != false {
		t.Errorf("php 8.4: %v", o)
	}
	if o := findOffer(t, list.body, "php", "7.4"); o["installable"] != false || o["reason"] != "end_of_life" || o["installed"] != false || o["units"] != nil {
		t.Errorf("php 7.4: %v", o)
	}

	// Everything but installing works on the ended lines.
	e.opsSucceed(helperapi.ActionPkgService, helperapi.ActionPkgRemove, helperapi.ActionPkgConfigWrite, helperapi.ActionPkgExtApply, helperapi.ActionPkgInstall, helperapi.ActionPkgInstallSet)
	e.helper.respond(helperapi.ActionPkgExtStatus, helperapi.ExtStatus{Extensions: []helperapi.ExtensionInfo{{ID: "intl", Name: "intl", Modules: []string{"intl"}, Available: true}}})
	e.helper.respond(helperapi.ActionPkgConfigRead, helperapi.ConfigFile{ID: "php.ini", SHA256: strings.Repeat("a", 64), Editable: true})
	for _, tc := range []struct {
		method, path string
		body         any
		status       int
	}{
		{"POST", "/api/v1/software/php/service", map[string]any{"line": "8.2", "action": "restart"}, 202},
		{"GET", "/api/v1/software/php/lines/8.2/extensions", nil, 200},
		{"POST", "/api/v1/software/php/lines/8.2/extensions", map[string]any{"install": []string{"intl"}}, 202},
		{"GET", "/api/v1/software/php/lines/8.2/configs/php.ini", nil, 200},
		{"PUT", "/api/v1/software/php/lines/8.2/configs/php.ini", map[string]any{"content": "memory_limit = 256M\n", "base": strings.Repeat("a", 64)}, 202},
		{"POST", "/api/v1/software/php/remove", map[string]any{"line": "8.2"}, 202},
		{"POST", "/api/v1/software/postgresql/service", map[string]any{"line": "14", "action": "restart"}, 202},
		{"GET", "/api/v1/software/postgresql/lines/14/configs/postgresql.conf", nil, 200},
		{"POST", "/api/v1/software/postgresql/remove", map[string]any{"line": "14"}, 202},
	} {
		r := admin.do(tc.method, tc.path, tc.body)
		if r.status != tc.status {
			t.Errorf("%s %s: %d %s %v", tc.method, tc.path, r.status, r.code(), r.params())
			continue
		}
		if tc.status == 202 {
			admin.runJob(r)
		}
	}
	if q := sentAs[helperapi.SoftwareRequest](t, e, helperapi.ActionPkgRemove); q.Software != "postgresql" || q.Line != "14" {
		t.Errorf("remove sent to the helper: %+v", q)
	}

	// Installing an ended line is refused before the helper is asked: on
	// its own, in a set and in a probe.
	installs := func() int {
		n := 0
		for _, a := range e.helper.callsOf() {
			if a == helperapi.ActionPkgInstall || a == helperapi.ActionPkgInstallSet || a == helperapi.ActionPkgProbe {
				n++
			}
		}
		return n
	}
	asked := installs()
	for _, tc := range []struct {
		path string
		body any
		eol  string
	}{
		{"/api/v1/software/php/install", map[string]any{"line": "8.2"}, "2026-12-31"},
		{"/api/v1/software/postgresql/install", map[string]any{"line": "14"}, "2026-11-12"},
		{"/api/v1/software/install", map[string]any{"items": []map[string]any{{"software": "nginx", "line": "1.26"}, {"software": "php", "line": "8.2"}}}, "2026-12-31"},
		{"/api/v1/software/probe", map[string]any{"items": []map[string]any{{"software": "php", "line": "8.2"}}}, "2026-12-31"},
	} {
		r := admin.expect(admin.do("POST", tc.path, tc.body), 409, helperapi.CodePkgNotAvailable)
		if r.params()["reason"] != "end_of_life" || r.params()["eol"] != tc.eol {
			t.Errorf("%s: params %v", tc.path, r.params())
		}
	}
	if installs() != asked {
		t.Error("the helper was asked to install or probe an ended line")
	}
	// A supported line from the same repository still installs.
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/software/php/install", map[string]any{"line": "8.3"}), 202, ""))
	// A line nobody builds here stays unknown to every route.
	admin.expect(admin.do("POST", "/api/v1/software/valkey/remove", map[string]any{"line": "7.2"}), 409, helperapi.CodePkgNotAvailable)

	// The overview keeps the ended line's service.
	ov := admin.expect(admin.do("GET", "/api/v1/overview", nil), 200, "")
	units := []string{}
	for _, s := range ov.body["services"].([]any) {
		units = append(units, s.(map[string]any)["unit"].(string))
	}
	if !strings.Contains(strings.Join(units, ","), "php8.2-fpm") {
		t.Errorf("overview services: %v", units)
	}
}

func TestDatabaseBackupColumns(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	now := time.Now().Unix()
	e.helper.respond(helperapi.ActionDBStatus, mysqlStatus([]helperapi.DBInfo{{Name: "shop", SizeBytes: 4096}, {Name: "blog"}}, nil))
	p := admin.expect(admin.do("POST", "/api/v1/backups/plans", map[string]any{"name": "Nightly",
		"targets": []any{map[string]any{"kind": "database", "engine": "mysql", "database": "shop"}}}), 201, "")
	e.helper.respond(helperapi.ActionBackupRuns, helperapi.BackupRuns{Runs: []helperapi.BackupRun{{Started: now - 200, Finished: now - 190, OK: true, Size: 10}}})
	dumps := filepath.Join(e.dataDir, "backups/db/mysql")
	if err := os.MkdirAll(dumps, 0o700); err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(dumps, "blog_20261121-010203.sql.gz")
	if err := os.WriteFile(dump, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(now-50, 0)
	_ = os.Chtimes(dump, at, at)

	list := admin.expect(admin.do("GET", "/api/v1/databases", nil), 200, "")
	if list.body["backups"] != true {
		t.Fatalf("backups: %v", list.body)
	}
	byName := map[string]map[string]any{}
	for _, d := range list.body["engines"].([]any)[0].(map[string]any)["databases"].([]any) {
		byName[d.(map[string]any)["name"].(string)] = d.(map[string]any)
	}
	backup, _ := byName["shop"]["backup"].(map[string]any)
	if fmt.Sprint(backup["plans"]) != "["+p.body["id"].(string)+"]" || backup["lastOkAt"] != float64(now-190) || byName["shop"]["lastDumpAt"] != nil {
		t.Fatalf("shop: %v", byName["shop"])
	}
	if byName["blog"]["backup"] != nil || byName["blog"]["lastDumpAt"] != float64(now-50) {
		t.Fatalf("blog: %v", byName["blog"])
	}

	// With the backups module off the list says so.
	admin.elevate("")
	admin.expect(admin.do("PUT", "/api/v1/modules/backups", map[string]any{"enabled": false}), 200, "")
	list = admin.expect(admin.do("GET", "/api/v1/databases", nil), 200, "")
	if list.body["backups"] != false {
		t.Fatalf("backups off: %v", list.body["backups"])
	}
}
