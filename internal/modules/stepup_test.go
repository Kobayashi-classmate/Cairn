// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/core"
)

// stepUpReported checks that /auth/me and /settings report the switch.
func stepUpReported(t *testing.T, c *client, want bool) {
	t.Helper()
	if got := c.expect(c.do("GET", "/api/v1/auth/me", nil), 200, "").body["stepUpRequired"]; got != want {
		t.Fatalf("/auth/me stepUpRequired %v, want %v", got, want)
	}
	if got := c.expect(c.do("GET", "/api/v1/settings", nil), 200, "").body["stepUpRequired"]; got != want {
		t.Fatalf("/settings stepUpRequired %v, want %v", got, want)
	}
}

// stepUpAudit returns the enabled parameter of each settings.step_up entry,
// oldest first.
func stepUpAudit(t *testing.T, e *env) []bool {
	t.Helper()
	rows, err := e.db.Query(`SELECT params FROM audit_log WHERE action = 'settings.step_up' AND result = 'ok' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []bool
	for rows.Next() {
		var raw string
		var p struct {
			Enabled *bool `json:"enabled"`
		}
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil || p.Enabled == nil {
			t.Fatalf("audit params %q", raw)
		}
		out = append(out, *p.Enabled)
	}
	return out
}

// Administrators can turn re-authentication for sensitive operations off.
// Browser sessions then pass every step-up check, on routes and inside
// handlers (files outside the sites root, root tasks, a saved bucket's key);
// API tokens are refused as before, since they can never re-authenticate.
func TestStepUpSwitch(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.opsSucceed(helperapi.ActionCronApply)
	e.helper.respond(helperapi.ActionTermUsers, helperapi.TermUsers{RootTerminal: true, Users: []helperapi.TermUser{
		{Name: "root", Kind: helperapi.TermUserRoot, Home: "/root"}}})

	// On by default: nothing is stored until someone changes it.
	var stored int
	if err := e.db.QueryRow(`SELECT count(*) FROM kv WHERE key = 'security.step_up'`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("stored switch before any change: %d %v", stored, err)
	}
	stepUpReported(t, admin, true)

	// Turning it off is a sensitive operation itself.
	off, on := map[string]any{"enabled": false}, map[string]any{"enabled": true}
	admin.expect(admin.do("PUT", "/api/v1/settings/step-up", off), 403, "auth.step_up_required")
	admin.elevate("")
	admin.expect(admin.do("PUT", "/api/v1/settings/step-up", map[string]any{}), 400, "validation.invalid")
	tok := admin.expect(admin.do("POST", "/api/v1/me/tokens", map[string]any{"name": "everything", "expiresInDays": 1,
		"permissions": []string{"settings.read", "settings.manage", "users.manage", "files.read", "files.manage", "files.admin",
			"cron.read", "cron.manage", "cron.root", "backups.read", "backups.manage"}}), 201, "").body["token"].(string)
	if r := e.bearer(tok, "PUT", "/api/v1/settings/step-up", off); r.status != 403 || r.code() != "auth.step_up_required" {
		t.Fatalf("token turning step-up off: %d %s", r.status, r.raw)
	}
	if r := admin.expect(admin.do("PUT", "/api/v1/settings/step-up", off), 200, ""); r.body["enabled"] != false {
		t.Fatalf("answer %v", r.body)
	}
	var value string
	if err := e.db.QueryRow(`SELECT value FROM kv WHERE key = 'security.step_up'`).Scan(&value); err != nil || value != "off" {
		t.Fatalf("stored switch %q %v", value, err)
	}
	if got := stepUpAudit(t, e); len(got) != 1 || got[0] {
		t.Fatalf("audit %v", got)
	}

	// A session that never re-authenticated gets through.
	plain := e.client()
	if r := plain.expect(plain.login("admin", pw), 200, ""); r.body["stepUpRequired"] != false || r.body["elevatedUntil"] != float64(0) {
		t.Fatalf("sign-in answer %v", r.body)
	}
	stepUpReported(t, plain, false)
	if until := plain.do("GET", "/api/v1/auth/me", nil).body["elevatedUntil"]; until != float64(0) {
		t.Fatalf("elevatedUntil of a plain session: %v", until)
	}
	plain.expect(plain.do("POST", "/api/v1/users", map[string]any{"username": "ops", "password": pw, "role": "operator"}), 201, "")
	mkdir := map[string]any{"root": "backups", "dir": "", "name": "x.d"}
	plain.expect(plain.do("POST", "/api/v1/files/mkdir", mkdir), 200, "")
	plain.expect(plain.do("GET", "/api/v1/files/search?root=system&q=secret&content=true", nil), 200, "")
	rootTask := map[string]any{"name": "Root job", "type": "shell", "schedule": "hourly", "user": "root", "script": "apt-get clean", "enabled": true}
	plain.runJob(plain.expect(plain.do("POST", "/api/v1/cron", rootTask), 202, ""))
	storage := map[string]any{"name": "R2", "endpoint": "acct.r2.cloudflarestorage.com", "bucket": "panel-backups", "accessKey": "AK", "secretKey": "S3CRET"}
	sid := plain.expect(plain.do("POST", "/api/v1/backups/storages", storage), 201, "").body["id"].(string)
	withSavedKey := map[string]any{"id": sid, "endpoint": "collector.example", "bucket": "my-bucket", "accessKey": "AK"}
	plain.expect(plain.do("POST", "/api/v1/backups/storages/test", withSavedKey), 204, "")

	// Tokens are refused all the same, on routes and inside handlers; they
	// still list the roots outside the sites.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/users", map[string]any{"username": "eve", "password": pw, "role": "operator"}},
		{"POST", "/api/v1/files/mkdir", mkdir},
		{"GET", "/api/v1/files/search?root=system&q=secret&content=true", nil},
		{"POST", "/api/v1/cron", rootTask},
		{"POST", "/api/v1/backups/storages/test", withSavedKey},
	} {
		if r := e.bearer(tok, c.method, c.path, c.body); r.status != 403 || r.code() != "auth.step_up_required" {
			t.Fatalf("token %s %s with step-up off: %d %s", c.method, c.path, r.status, r.raw)
		}
	}
	if r := e.bearer(tok, "GET", "/api/v1/files/list?root=backups", nil); r.status != 200 {
		t.Fatalf("token list of backups: %d %s", r.status, r.raw)
	}

	// Turning it back on needs no re-authentication (it is off), and the
	// checks are back for the plain session.
	if r := plain.expect(plain.do("PUT", "/api/v1/settings/step-up", on), 200, ""); r.body["enabled"] != true {
		t.Fatalf("answer %v", r.body)
	}
	stepUpReported(t, plain, true)
	plain.expect(plain.do("POST", "/api/v1/users", map[string]any{"username": "ops2", "password": pw, "role": "operator"}), 403, "auth.step_up_required")
	plain.expect(plain.do("POST", "/api/v1/files/mkdir", mkdir), 403, "auth.step_up_required")
	plain.expect(plain.do("POST", "/api/v1/cron", rootTask), 403, "auth.step_up_required")
	plain.expect(plain.do("POST", "/api/v1/backups/storages/test", withSavedKey), 403, "auth.step_up_required")
	plain.expect(plain.do("PUT", "/api/v1/settings/step-up", off), 403, "auth.step_up_required")
	if got := stepUpAudit(t, e); len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("audit %v", got)
	}
}

// Completing setup starts the session re-authenticated: the password was
// entered a moment ago, and the first-run wizard saves its settings (the
// step-up switch among them) without asking for it again.
func TestSetupElevatesTheSession(t *testing.T) {
	e := newEnv(t)
	token, err := auth.CreateSetupToken(context.Background(), e.db, auth.SetupTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	c := e.client()
	r := c.expect(c.do("POST", "/api/v1/setup", map[string]any{"token": token, "username": "admin", "password": pw, "locale": "en"}), 200, "")
	now := float64(time.Now().Unix())
	until, _ := r.body["elevatedUntil"].(float64)
	if until <= now || until > now+auth.ElevationWindow.Seconds()+5 || r.body["stepUpRequired"] != true {
		t.Fatalf("setup answer %v", r.body)
	}
	if me := c.expect(c.do("GET", "/api/v1/auth/me", nil), 200, ""); me.body["elevatedUntil"] != until {
		t.Fatalf("/auth/me %v, setup said %v", me.body, until)
	}
	c.expect(c.do("PUT", "/api/v1/settings/step-up", map[string]any{"enabled": false}), 200, "")
	stepUpReported(t, c, false)

	// A sign-in is not re-authenticated.
	other := e.client()
	if r := other.expect(other.login("admin", pw), 200, ""); r.body["elevatedUntil"] != float64(0) || r.body["stepUpRequired"] != false {
		t.Fatalf("sign-in answer %v", r.body)
	}
}

// The switch is stored: a restarted panel (new settings, router and core
// module over the same database) reads it back.
func TestStepUpSwitchSurvivesARestart(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	admin.elevate("")
	admin.expect(admin.do("PUT", "/api/v1/settings/step-up", map[string]any{"enabled": false}), 200, "")
	if _, err := e.db.Exec(`UPDATE sessions SET elevated_until = 0`); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	settings := core.NewSettings("en")
	if err := settings.LoadStepUp(context.Background(), e.db); err != nil {
		t.Fatal(err)
	}
	if settings.StepUpRequired() {
		t.Fatal("restarted panel requires step-up")
	}
	router := api.NewRouter(core.Authenticator(e.db), log)
	router.SetStepUpRequired(settings.StepUpRequired)
	registry := module.NewRegistry(e.db)
	registry.Add(&core.Module{DB: e.db, Settings: settings, Helper: e.helper, Log: log, Modules: registry})
	registry.Mount(router)
	srv := httptest.NewUnstartedServer(api.SecurityHeaders(api.Recover(log, router)))
	srv.Config.ConnContext = api.ConnContext
	srv.StartTLS()
	t.Cleanup(srv.Close)

	// The same browser, signed in as before, talks to the restarted panel
	// (cookies do not depend on the port).
	restarted := *e
	restarted.srv = srv
	c := &client{e: &restarted, hc: admin.hc, key: admin.key}
	stepUpReported(t, c, false)
	// A step-up route of the core module, without re-authenticating.
	c.expect(c.do("POST", "/api/v1/me/tokens", map[string]any{"name": "ci", "permissions": []string{"system.read"}, "expiresInDays": 1}), 201, "")
	c.expect(c.do("PUT", "/api/v1/settings/step-up", map[string]any{"enabled": true}), 200, "")
	c.expect(c.do("POST", "/api/v1/me/tokens", map[string]any{"name": "ci2", "permissions": []string{"system.read"}, "expiresInDays": 1}),
		403, "auth.step_up_required")

	// Only "off" turns it off; a missing row (a new installation) or any
	// other value means on.
	for _, c := range []struct {
		stmt string
		want bool
	}{
		{`UPDATE kv SET value = 'off' WHERE key = 'security.step_up'`, false},
		{`UPDATE kv SET value = 'OFF' WHERE key = 'security.step_up'`, true},
		{`UPDATE kv SET value = 'off' WHERE key = 'security.step_up'`, false},
		{`DELETE FROM kv WHERE key = 'security.step_up'`, true},
	} {
		if _, err := e.db.Exec(c.stmt); err != nil {
			t.Fatal(err)
		}
		if err := settings.LoadStepUp(context.Background(), e.db); err != nil || settings.StepUpRequired() != c.want {
			t.Fatalf("after %s: required %v, %v", c.stmt, settings.StepUpRequired(), err)
		}
	}
}
