// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/store"
)

type fakeHelper struct {
	err   error
	calls []string
}

func (f *fakeHelper) Call(_ context.Context, action string, _ helperapi.Caller, _, _ any) error {
	f.calls = append(f.calls, action)
	return f.err
}

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	client *http.Client
	db     *sql.DB
	helper *fakeHelper
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "cairn.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &fakeHelper{}
	router := api.NewRouter(Authenticator(db), log)
	(&Module{DB: db, Settings: NewSettings("en"), Helper: h, Log: log}).Routes(router)
	srv := httptest.NewTLSServer(api.SecurityHeaders(api.Recover(log, router)))
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.Jar, _ = cookiejar.New(nil)
	return &harness{t: t, srv: srv, client: client, db: db, helper: h}
}

type reply struct {
	status int
	body   map[string]any
}

func (r reply) code() string {
	e, _ := r.body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func (r reply) params() map[string]any {
	e, _ := r.body["error"].(map[string]any)
	p, _ := e["params"].(map[string]any)
	return p
}

func (h *harness) do(method, path string, body any, headers map[string]string) reply {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	if method != http.MethodGet {
		req.Header.Set(api.CSRFHeader, "1")
	}
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	out := reply{status: res.StatusCode, body: map[string]any{}}
	data, _ := io.ReadAll(res.Body)
	if len(data) > 0 {
		_ = json.Unmarshal(data, &out.body)
	}
	return out
}

const pw = "a long enough password"

func TestFirstRunSetupAndSession(t *testing.T) {
	h := newHarness(t)

	if r := h.do("GET", "/api/v1/meta", nil, nil); r.body["setupRequired"] != true || r.body["defaultLocale"] != "en" {
		t.Fatalf("meta before setup: %+v", r.body)
	}
	setup := map[string]any{"token": "nope", "username": "admin", "password": pw, "locale": "zh-CN"}
	if r := h.do("POST", "/api/v1/setup", setup, map[string]string{api.CSRFHeader: ""}); r.status != 403 || r.code() != "request.csrf_failed" {
		t.Fatalf("missing CSRF header: %d %s", r.status, r.code())
	}
	if r := h.do("POST", "/api/v1/setup", setup, nil); r.status != 400 || r.code() != "setup.invalid_token" {
		t.Fatalf("bad token: %d %s", r.status, r.code())
	}

	token, err := auth.CreateSetupToken(context.Background(), h.db, auth.SetupTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	setup["token"] = token
	setup["password"] = "short"
	if r := h.do("POST", "/api/v1/setup", setup, nil); r.status != 400 || r.code() != "validation.invalid" ||
		r.params()["field"] != "password" || r.params()["rule"] != "min_length" || r.params()["min"] != float64(auth.MinPasswordLen) {
		t.Fatalf("weak password: %d %+v", r.status, r.body)
	}
	setup["password"] = pw
	setup["extra"] = true
	if r := h.do("POST", "/api/v1/setup", setup, nil); r.status != 400 || r.code() != "request.invalid_json" {
		t.Fatalf("unknown field should be rejected: %d %s", r.status, r.code())
	}
	delete(setup, "extra")
	if r := h.do("POST", "/api/v1/setup", setup, nil); r.status != 200 {
		t.Fatalf("setup: %d %+v", r.status, r.body)
	}
	if r := h.do("POST", "/api/v1/setup", setup, nil); r.status != 409 || r.code() != "setup.already_completed" {
		t.Fatalf("second setup: %d %s", r.status, r.code())
	}

	// Setup signs the administrator in.
	r := h.do("GET", "/api/v1/auth/me", nil, nil)
	user, _ := r.body["user"].(map[string]any)
	if r.status != 200 || user["username"] != "admin" || user["locale"] != "zh-CN" {
		t.Fatalf("me after setup: %d %+v", r.status, r.body)
	}
	if r := h.do("GET", "/api/v1/meta", nil, nil); r.body["setupRequired"] != false {
		t.Fatalf("meta after setup: %+v", r.body)
	}

	// Language switching.
	if r := h.do("PUT", "/api/v1/me/locale", map[string]string{"locale": "en"}, nil); r.status != 204 {
		t.Fatalf("set locale: %d %+v", r.status, r.body)
	}
	if r := h.do("PUT", "/api/v1/me/locale", map[string]string{"locale": "xx"}, nil); r.status != 400 || r.code() != "locale.unsupported" {
		t.Fatalf("unsupported locale: %d %s", r.status, r.code())
	}
	// A cross-site Origin is refused even with the header.
	if r := h.do("PUT", "/api/v1/me/locale", map[string]string{"locale": "en"}, map[string]string{"Origin": "https://evil.example"}); r.status != 403 {
		t.Fatalf("foreign origin: %d", r.status)
	}

	// The panel default goes through the root helper.
	if r := h.do("PUT", "/api/v1/settings/default-locale", map[string]string{"locale": "zh-CN"}, nil); r.status != 204 {
		t.Fatalf("default locale: %d %+v", r.status, r.body)
	}
	if len(h.helper.calls) != 1 || h.helper.calls[0] != helperapi.ActionSetDefaultLocale {
		t.Fatalf("helper not called: %v", h.helper.calls)
	}
	if r := h.do("GET", "/api/v1/meta", nil, nil); r.body["defaultLocale"] != "zh-CN" {
		t.Fatalf("default locale not applied: %+v", r.body)
	}
	h.helper.err = &helperapi.Error{Code: helperapi.CodeUnavailable}
	if r := h.do("PUT", "/api/v1/settings/default-locale", map[string]string{"locale": "en"}, nil); r.status != 502 || r.code() != helperapi.CodeUnavailable {
		t.Fatalf("helper down: %d %s", r.status, r.code())
	}

	// Sign out.
	if r := h.do("POST", "/api/v1/auth/logout", nil, nil); r.status != 204 {
		t.Fatalf("logout: %d", r.status)
	}
	if r := h.do("GET", "/api/v1/auth/me", nil, nil); r.status != 401 {
		t.Fatalf("me after logout: %d", r.status)
	}

	// Audit trail exists.
	var n int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('auth.setup','auth.logout','settings.default_locale') AND result = 'ok'`).Scan(&n)
	if n != 3 {
		t.Fatalf("want 3 audit entries, got %d", n)
	}
}

func TestLoginThrottling(t *testing.T) {
	h := newHarness(t)
	if _, err := auth.CreateUser(context.Background(), h.db, "admin", pw, ""); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{"username": "admin", "password": "definitely wrong"}
	for i := 0; i < 5; i++ {
		if r := h.do("POST", "/api/v1/auth/login", bad, nil); r.status != 401 || r.code() != "auth.invalid_credentials" {
			t.Fatalf("attempt %d: %d %s", i, r.status, r.code())
		}
	}
	// Locked now, even with the right password.
	r := h.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": pw}, nil)
	if r.status != 429 || r.code() != "auth.rate_limited" {
		t.Fatalf("want rate limit, got %d %s", r.status, r.code())
	}
	if secs, _ := r.params()["retryAfterSeconds"].(float64); secs < 1 || secs > 60 {
		t.Fatalf("retryAfterSeconds out of range: %v", r.params())
	}
	// Unknown users get the same answer as wrong passwords.
	if r := h.do("POST", "/api/v1/auth/login", map[string]string{"username": "ghost", "password": "whatever"}, nil); r.status != 429 && r.code() != "auth.invalid_credentials" {
		t.Fatalf("unknown user: %d %s", r.status, r.code())
	}
}

func TestUnauthenticatedAndUnknownRoutes(t *testing.T) {
	h := newHarness(t)
	if r := h.do("GET", "/api/v1/auth/me", nil, nil); r.status != 401 || r.code() != "auth.unauthenticated" {
		t.Fatalf("me without session: %d %s", r.status, r.code())
	}
	if r := h.do("GET", "/api/v1/does-not-exist", nil, nil); r.status != 404 || r.code() != "request.not_found" {
		t.Fatalf("unknown route: %d %s", r.status, r.code())
	}
}
