// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/notify"
)

// bearer sends a request with an API token and no cookie, session key or
// CSRF header.
func (e *env) bearer(token, method, path string, body any) reply {
	e.t.Helper()
	c := e.client()
	c.hc.Jar = nil
	return c.do(method, path, body, "Authorization", "Bearer "+token, api.CSRFHeader, "", api.SessionHeader, "")
}

func TestAPITokens(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	req := map[string]any{"name": "ci", "permissions": []string{"system.read", "settings.read", "settings.manage"}, "expiresInDays": 30, "allowedIps": []string{"127.0.0.1"}}
	admin.expect(admin.do("POST", "/api/v1/me/tokens", req), 403, "auth.step_up_required")
	admin.elevate("")
	created := admin.expect(admin.do("POST", "/api/v1/me/tokens", req), 201, "")
	token := created.body["token"].(string)
	info := created.obj("info")
	if !strings.HasPrefix(token, "cairn_") || !strings.HasPrefix(token, info["prefix"].(string)) {
		t.Fatalf("token %q info %v", token, info)
	}
	list := admin.expect(admin.do("GET", "/api/v1/me/tokens", nil), 200, "")
	if raw, _ := json.Marshal(list.body); strings.Contains(string(raw), token) {
		t.Fatal("listing reveals the token")
	}

	if r := e.bearer(token, "GET", "/api/v1/system/overview", nil); r.status != 200 {
		t.Fatalf("token within scope: %d %v", r.status, r.body)
	}
	// Outside the token's scope, even though the owner (admin) could.
	if r := e.bearer(token, "GET", "/api/v1/audit", nil); r.status != 403 || r.code() != "auth.forbidden" {
		t.Fatalf("token outside scope: %d %s", r.status, r.code())
	}
	// No CSRF header needed with a token; the change is audited as "api".
	if r := e.bearer(token, "PUT", "/api/v1/me/locale", map[string]any{"locale": "zh-CN"}); r.status != 204 {
		t.Fatalf("token write: %d %v", r.status, r.body)
	}
	// Tokens cannot elevate, so step-up routes stay closed to them.
	if r := e.bearer(token, "POST", "/api/v1/auth/elevate", map[string]any{"password": pw}); r.status != 403 || r.code() != "auth.forbidden" {
		t.Fatalf("token elevate: %d %s", r.status, r.code())
	}
	// The account's own security is closed to every token, whatever its
	// scopes: no token can sign its owner out, revoke tokens, change the
	// password or two-step sign-in, or list the sessions.
	for _, c := range []struct{ method, path string }{
		{"DELETE", "/api/v1/me/totp"}, {"DELETE", "/api/v1/me/sessions"}, {"GET", "/api/v1/me/sessions"},
		{"DELETE", "/api/v1/me/sessions/0000"}, {"GET", "/api/v1/me/tokens"}, {"DELETE", fmt.Sprintf("/api/v1/me/tokens/%d", int64(info["id"].(float64)))},
		{"PUT", "/api/v1/me/password"}, {"POST", "/api/v1/me/totp/confirm"}, {"POST", "/api/v1/me/recovery-codes"},
	} {
		if r := e.bearer(token, c.method, c.path, map[string]any{}); r.status != 403 || r.code() != "auth.forbidden" {
			t.Fatalf("token on %s %s: %d %s", c.method, c.path, r.status, r.code())
		}
	}
	admin.expect(admin.do("GET", "/api/v1/auth/me", nil), 200, "")
	if r := e.bearer("cairn_bogus", "GET", "/api/v1/system/overview", nil); r.status != 401 || r.code() != "auth.token_invalid" {
		t.Fatalf("bogus token: %d %s", r.status, r.code())
	}
	// A cookie cannot be smuggled in next to a bad bearer token.
	if r := admin.do("GET", "/api/v1/auth/me", nil, "Authorization", "Bearer cairn_bogus"); r.status != 401 {
		t.Fatalf("bearer must take precedence over the cookie: %d", r.status)
	}

	// Audited actions done with a token are recorded with source "api".
	if r := e.bearer(token, "PUT", "/api/v1/settings/default-locale", map[string]any{"locale": "en"}); r.status != 204 {
		t.Fatalf("token changing a setting: %d %v", r.status, r.body)
	}
	var src string
	_ = e.db.QueryRow(`SELECT source FROM audit_log WHERE action = 'settings.default_locale' ORDER BY id DESC LIMIT 1`).Scan(&src)
	if src != "api" {
		t.Fatalf("token action audited with source %q", src)
	}
	_ = e.db.QueryRow(`SELECT source FROM audit_log WHERE action = 'auth.elevate' AND result = 'ok' ORDER BY id DESC LIMIT 1`).Scan(&src)
	if src != "web" {
		t.Fatalf("browser elevate audited as %q", src)
	}

	id := int64(info["id"].(float64))
	admin.expect(admin.do("DELETE", fmt.Sprintf("/api/v1/me/tokens/%d", id), nil), 204, "")
	if r := e.bearer(token, "GET", "/api/v1/system/overview", nil); r.status != 401 {
		t.Fatalf("revoked token still works: %d", r.status)
	}
}

type receiver struct {
	mu     sync.Mutex
	events []string
	sigOK  bool
}

func (rc *receiver) handler(secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		defer rc.mu.Unlock()
		rc.events = append(rc.events, r.Header.Get("X-Cairn-Event"))
		rc.sigOK = r.Header.Get("X-Cairn-Signature") == notify.Sign(secret, r.Header.Get("X-Cairn-Timestamp"), body)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (rc *receiver) saw(event string) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for _, e := range rc.events {
		if e == event {
			return true
		}
	}
	return false
}

func TestNotificationsEndToEnd(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	rc := &receiver{}
	hook := httptest.NewServer(rc.handler("s3cret"))
	defer hook.Close()

	channel := map[string]any{"type": "webhook", "name": "ops hook", "locale": "zh-CN", "enabled": true,
		"events": []string{"security.", "system."}, "minLevel": "info", "config": map[string]any{"url": hook.URL, "secret": "s3cret"}}
	admin.expect(admin.do("POST", "/api/v1/notify/channels", channel), 403, "auth.step_up_required")
	admin.elevate("")
	bad := map[string]any{"type": "webhook", "name": "x", "locale": "en", "events": []string{"security."}, "minLevel": "info",
		"config": map[string]any{"url": "ftp://example.com"}}
	if r := admin.expect(admin.do("POST", "/api/v1/notify/channels", bad), 400, "validation.invalid"); r.params()["field"] != "url" {
		t.Fatalf("url validation: %v", r.params())
	}
	created := admin.expect(admin.do("POST", "/api/v1/notify/channels", channel), 201, "")
	ch := created.obj("channel")
	if cfg := ch["config"].(map[string]any); cfg["hasSecret"] != true || cfg["secret"] != nil {
		t.Fatalf("secret leaked or flag missing: %v", ch)
	}
	path := fmt.Sprintf("/api/v1/notify/channels/%d", int64(ch["id"].(float64)))

	// Test message: delivered and signed.
	admin.expect(admin.do("POST", path+"/test", nil), 204, "")
	if !rc.saw("system.test") || !rc.sigOK {
		t.Fatalf("test message: events=%v sigOK=%v", rc.events, rc.sigOK)
	}

	// Toggling only "enabled" keeps the stored secret.
	admin.expect(admin.do("PATCH", path, map[string]any{"enabled": false}), 200, "")
	got := admin.expect(admin.do("PATCH", path, map[string]any{"enabled": true}), 200, "").obj("channel")
	if got["config"].(map[string]any)["hasSecret"] != true || got["enabled"] != true {
		t.Fatalf("partial update lost data: %v", got)
	}

	// Operators cannot see notification settings. (Checked before the lockout
	// below, which also blocks this test's address.)
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "ops", "password": pw, "role": "operator"}), 201, "")
	ops := e.client()
	ops.expect(ops.login("ops", pw), 200, "")
	ops.expect(ops.do("GET", "/api/v1/notify/channels", nil), 403, "auth.forbidden")

	// A lockout (five wrong passwords) reaches the channel through the bus.
	anon := e.client()
	for i := 0; i < 5; i++ {
		anon.login("admin", "wrong password!!")
	}
	deadline := time.Now().Add(10 * time.Second)
	for !rc.saw("security.lockout") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !rc.saw("security.lockout") {
		t.Fatalf("lockout not delivered; got %v", rc.events)
	}
	// The receiver may answer before the sender records the delivery.
	var ds []any
	for time.Now().Before(deadline) {
		ds = admin.expect(admin.do("GET", "/api/v1/notify/deliveries", nil), 200, "").body["deliveries"].([]any)
		if len(ds) >= 2 && ds[0].(map[string]any)["status"] == "sent" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(ds) < 2 || ds[0].(map[string]any)["status"] != "sent" {
		t.Fatalf("deliveries: %v", ds)
	}

	// A failing receiver is reported with a reason.
	hook.Close()
	r := admin.expect(admin.do("POST", path+"/test", nil), 502, "notify.delivery_failed")
	if reason, _ := r.params()["reason"].(string); reason == "" {
		t.Fatalf("missing reason: %v", r.body)
	}

	// The stored secret does not go to another destination: a new URL needs
	// the secret again (or "" to drop it).
	r = admin.expect(admin.do("PATCH", path, map[string]any{"config": map[string]any{"url": "https://elsewhere.example/hook"}}), 400, "validation.invalid")
	if r.params()["field"] != "secret" || r.params()["rule"] != "required" {
		t.Fatalf("new destination kept the secret: %v", r.params())
	}
	admin.expect(admin.do("PATCH", path, map[string]any{"config": map[string]any{"url": "https://elsewhere.example/hook", "secret": "n3w"}}), 200, "")
	mailCh := admin.expect(admin.do("POST", "/api/v1/notify/channels", map[string]any{"type": "email", "name": "mail", "locale": "en", "enabled": false,
		"events": []string{"security."}, "minLevel": "info", "config": map[string]any{"host": "smtp.example.com", "port": 587, "security": "starttls",
			"username": "u", "password": "smtp-pass", "from": "panel@example.com", "to": []string{"ops@example.com"}}}), 201, "").obj("channel")
	mailPath := fmt.Sprintf("/api/v1/notify/channels/%d", int64(mailCh["id"].(float64)))
	for _, change := range []map[string]any{{"host": "smtp.evil.example"}, {"port": 2525}, {"security": "none"}} {
		r = admin.expect(admin.do("PATCH", mailPath, map[string]any{"config": change}), 400, "validation.invalid")
		if r.params()["field"] != "password" || r.params()["rule"] != "required" {
			t.Fatalf("new mail server kept the password: %v", r.params())
		}
	}
	admin.expect(admin.do("PATCH", mailPath, map[string]any{"config": map[string]any{"from": "cairn@example.com"}}), 200, "")
	admin.expect(admin.do("PATCH", mailPath, map[string]any{"config": map[string]any{"security": "tls"}}), 200, "")
	admin.expect(admin.do("PATCH", mailPath, map[string]any{"config": map[string]any{"host": "smtp2.example.com", "password": "other-pass"}}), 200, "")

	// Clearing the secret with "" works.
	got = admin.expect(admin.do("PATCH", path, map[string]any{"config": map[string]any{"secret": ""}}), 200, "").obj("channel")
	if got["config"].(map[string]any)["hasSecret"] != false {
		t.Fatalf("secret not cleared: %v", got)
	}
	admin.expect(admin.do("DELETE", path, nil), 204, "")
	admin.expect(admin.do("POST", path+"/test", nil), 404, "request.not_found")
}

func TestOpenAPICoversEveryRoute(t *testing.T) {
	e := newEnv(t)
	for _, r := range e.router.Routes() {
		if r.Summary == "" || r.Tag == "" {
			t.Errorf("%s %s is not documented (api.Doc missing)", r.Method, r.Path)
		}
		if r.Method != "GET" && r.Method != "DELETE" && r.Request == nil && !r.NoBody && r.RawBody == "" {
			t.Errorf("%s %s documents neither a body (api.Body) nor its absence (api.NoBody)", r.Method, r.Path)
		}
	}
	admin := e.setupAdmin("admin")
	doc := admin.expect(admin.do("GET", "/api/v1/openapi.json", nil), 200, "")
	paths := doc.body["paths"].(map[string]any)
	for _, p := range []string{"/api/v1/users/{id}", "/api/v1/notify/channels", "/api/v1/me/tokens", "/api/v1/openapi.json"} {
		if _, ok := paths[p]; !ok {
			t.Errorf("path %s missing from the document", p)
		}
	}
	op := paths["/api/v1/users/{id}"].(map[string]any)["patch"].(map[string]any)
	if op["x-cairn-permission"] != "users.manage" || op["x-cairn-step-up"] != true {
		t.Fatalf("users patch op: %v", op)
	}
	if tags, _ := doc.body["tags"].([]any); len(tags) < 6 {
		t.Fatalf("top-level tags: %v", doc.body["tags"])
	}
	e.client().expect(e.client().do("GET", "/api/v1/openapi.json", nil), 401, "auth.unauthenticated")
}
