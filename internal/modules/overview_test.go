// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// codesOf lists the "code" of each entry of an overview section.
func codesOf(list any) []string {
	var out []string
	for _, v := range list.([]any) {
		out = append(out, v.(map[string]any)["code"].(string))
	}
	return out
}

func findCode(list any, code string) map[string]any {
	for _, v := range list.([]any) {
		if m := v.(map[string]any); m["code"] == code {
			return m
		}
	}
	return nil
}

func TestOverviewAndSiteSummaries(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	now := time.Now().Unix()

	// A site served with a certificate that expires in three days, backed up
	// by a plan whose newest run failed.
	if _, err := e.db.Exec(`INSERT INTO certificates (id, name, source, domains, state, not_after, auto_renew, created_at, updated_at)
		VALUES ('c0123456789abcde', 'shop', 'upload', '["shop.example.com"]', 'valid', ?, 0, ?, ?)`, now+3*86400, now, now); err != nil {
		t.Fatal(err)
	}
	spec := `{"code":"shop","type":"static","domains":["shop.example.com"],"tls":{"cert":"c0123456789abcde"},"enabled":true}`
	if _, err := e.db.Exec(`INSERT INTO sites (code, type, spec, state, created_at, updated_at) VALUES ('shop', 'static', ?, 'active', ?, ?)`, spec, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO sites (code, type, spec, state, created_at, updated_at) VALUES ('blog', 'static', '{"code":"blog","type":"static","domains":["blog.example.com"],"enabled":true}', 'failed', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	p := admin.expect(admin.do("POST", "/api/v1/backups/plans", map[string]any{"name": "Nightly",
		"targets": []any{map[string]any{"kind": "site", "site": "shop"}}}), 201, "")
	pid := p.body["id"].(string)
	e.helper.respond(helperapi.ActionBackupRuns, helperapi.BackupRuns{Runs: []helperapi.BackupRun{
		{Started: now - 100, Finished: now - 90, OK: false, Error: &helperapi.Error{Code: "backup.failed"}},
		{Started: now - 200, Finished: now - 190, OK: true, Size: 10},
	}})
	e.helper.respond(helperapi.ActionSiteStatus, helperapi.SiteStatus{Supported: true, Sites: map[string]helperapi.SiteState{}})

	list := admin.expect(admin.do("GET", "/api/v1/sites", nil), 200, "")
	if list.body["backups"] != true {
		t.Fatalf("backups not included: %v", list.body)
	}
	var shop, blog map[string]any
	for _, s := range list.body["sites"].([]any) {
		switch s := s.(map[string]any); s["code"] {
		case "shop":
			shop = s
		case "blog":
			blog = s
		}
	}
	cert, _ := shop["cert"].(map[string]any)
	backup, _ := shop["backup"].(map[string]any)
	if cert["id"] != "c0123456789abcde" || cert["state"] != "valid" || cert["notAfter"] != float64(now+3*86400) || cert["autoRenew"] != false {
		t.Fatalf("cert: %v", shop)
	}
	if fmt.Sprint(backup["plans"]) != "["+pid+"]" || backup["lastOkAt"] != float64(now-190) || backup["lastFailedAt"] != float64(now-100) {
		t.Fatalf("backup: %v", backup)
	}
	if blog["cert"] != nil || blog["backup"] != nil {
		t.Fatalf("blog: %v", blog)
	}
	one := admin.expect(admin.do("GET", "/api/v1/sites/shop", nil), 200, "")
	if one.obj("cert")["name"] != "shop" || one.obj("backup")["lastOkAt"] != float64(now-190) {
		t.Fatalf("one site: %v", one.body)
	}

	// The overview: the services of installed software, checks and notices.
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	nginx, _ := cat.Find("nginx")
	var line, unit string
	for _, o := range nginx.Offers(catalog.System{Chain: []string{"debian-13", "debian"}, Arch: "amd64", Now: time.Now()}) {
		if o.Method != "" && len(o.Services) > 0 {
			line, unit = o.Line, o.Services[0]
			break
		}
	}
	if unit == "" {
		t.Fatal("no nginx line with a service on debian-13")
	}
	e.helper.respond(helperapi.ActionPkgStatus, helperapi.SoftwareStatus{
		Installed: map[string]map[string][]helperapi.InstalledPackage{"nginx": {line: {{Name: "nginx", Version: line + ".3-1"}}}},
		Services:  map[string]string{unit: "active"},
	})
	e.helper.respond(helperapi.ActionFirewallStatus, helperapi.FirewallStatus{Backend: "nftables", Installed: true, Enabled: true, Loaded: true, Systemd: true,
		Rules: []helperapi.FirewallRule{{ID: "ssh", Owner: "ssh"}, {ID: "panel", Owner: "panel"}}})
	e.helper.respond(helperapi.ActionSSHStatus, helperapi.SSHStatus{Installed: true, Ports: []int{22}, PasswordAuthentication: true, PermitRootLogin: "prohibit-password"})
	e.helper.respond(helperapi.ActionHostStatus, helperapi.HostStatus{RebootRequired: true,
		Updates: helperapi.UpdateList{Packages: []helperapi.PackageUpdate{{Name: "openssl", Security: true}, {Name: "curl"}}}})
	e.helper.respond(helperapi.ActionCertStatus, helperapi.CertStatus{Certs: map[string]helperapi.CertInfo{"c0123456789abcde": {Sites: []string{"shop"}}}})

	ov := admin.expect(admin.do("GET", "/api/v1/overview", nil), 200, "")
	services := ov.body["services"].([]any)
	if len(services) != 1 {
		t.Fatalf("services: %v", services)
	}
	if s := services[0].(map[string]any); s["software"] != "nginx" || s["line"] != line || s["unit"] != unit || s["state"] != "running" || s["version"] != line+".3-1" {
		t.Fatalf("service: %v", s)
	}
	checks := codesOf(ov.body["checks"])
	for _, want := range []string{"auth.totp_off", "certs.panel_self_signed", "firewall.on", "ssh.password"} {
		if !slices.Contains(checks, want) {
			t.Errorf("check %s missing: %v", want, checks)
		}
	}
	if c := findCode(ov.body["checks"], "firewall.on"); c["params"].(map[string]any)["rules"] != float64(2) || c["route"] != "firewall" {
		t.Errorf("firewall: %v", c)
	}
	notices := codesOf(ov.body["notices"])
	for _, want := range []string{"sites.failed", "certs.expiring", "backups.failed", "host.updates", "host.reboot"} {
		if !slices.Contains(notices, want) {
			t.Errorf("notice %s missing: %v", want, notices)
		}
	}
	if n := findCode(ov.body["notices"], "host.updates"); n["params"].(map[string]any)["count"] != float64(2) || n["params"].(map[string]any)["security"] != float64(1) {
		t.Errorf("updates: %v", n)
	}
	if n := findCode(ov.body["notices"], "certs.expiring"); n["params"].(map[string]any)["soonest"] != float64(now+3*86400) {
		t.Errorf("expiring: %v", n)
	}
	if u := ov.body["unavailable"].([]any); len(u) != 0 {
		t.Errorf("unavailable: %v", u)
	}

	// A module the helper cannot answer for is listed, the rest still shows.
	e.helper.fail(helperapi.ActionFirewallStatus, &helperapi.Error{Code: helperapi.CodeUnavailable})
	ov = admin.expect(admin.do("GET", "/api/v1/overview", nil), 200, "")
	if u := fmt.Sprint(ov.body["unavailable"]); u != "[firewall]" || slices.Contains(codesOf(ov.body["checks"]), "firewall.on") || !slices.Contains(codesOf(ov.body["checks"]), "ssh.password") {
		t.Fatalf("unavailable firewall: %v", ov.body)
	}

	// A token sees only what its permissions allow.
	admin.elevate("")
	tok := admin.expect(admin.do("POST", "/api/v1/me/tokens", map[string]any{"name": "ro", "permissions": []string{"sites.read"}, "expiresInDays": 1}), 201, "")
	ro := e.bearer(tok.body["token"].(string), "GET", "/api/v1/overview", nil)
	if ro.status != 200 || len(ro.body["services"].([]any)) != 0 || fmt.Sprint(codesOf(ro.body["notices"])) != "[sites.failed]" ||
		fmt.Sprint(codesOf(ro.body["checks"])) != "[auth.totp_off]" {
		t.Fatalf("token overview: %d %v", ro.status, ro.body)
	}

	// With the backups module off, sites carry no backup information.
	admin.expect(admin.do("PUT", "/api/v1/modules/backups", map[string]any{"enabled": false}), 200, "")
	list = admin.expect(admin.do("GET", "/api/v1/sites", nil), 200, "")
	if list.body["backups"] != false {
		t.Fatalf("backups off: %v", list.body)
	}
	for _, s := range list.body["sites"].([]any) {
		if s.(map[string]any)["backup"] != nil {
			t.Fatalf("backup shown with the module off: %v", s)
		}
	}
	if slices.Contains(codesOf(admin.expect(admin.do("GET", "/api/v1/overview", nil), 200, "").body["notices"]), "backups.failed") {
		t.Fatal("notice of a module that is off")
	}
}
