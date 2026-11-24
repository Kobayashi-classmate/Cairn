// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// rawRequest sends one request from a loopback address with exactly the
// given headers: no cookie jar, nothing added.
func rawRequest(t *testing.T, e *env, fromIP, method, path string, body any, headers map[string]string) (int, map[string]any, http.Header) {
	t.Helper()
	c := e.clientFrom(fromIP)
	c.hc.Jar = nil
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return res.StatusCode, out, res.Header
}

// Browsers send a host's cookies to every port of it: a website on the
// panel's host name receives the administrator's panel cookies with every
// visit, and its code can replay them from anywhere, with any headers it
// likes. The cookies alone must be worth nothing, before and after the
// administrator re-authenticates; the administrator's own page, which also
// holds the session key, works as ever.
func TestSessionCookieAloneIsRefused(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin("admin")
	// The administrator signs in with a browser at 127.0.0.3.
	const adminUA = "Mozilla/5.0 (administrator's browser)"
	status, signedIn, hdr := rawRequest(t, e, "127.0.0.3", "POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": pw},
		map[string]string{"X-Cairn-Request": "1", "User-Agent": adminUA})
	if status != 200 {
		t.Fatalf("login: %d %v", status, signedIn)
	}
	var cookies []string
	var session string
	for _, c := range (&http.Response{Header: hdr}).Cookies() {
		// The cookies stay as strict as a cookie can be; none of it keeps
		// them from another port of the same host.
		if !strings.HasPrefix(c.Name, "__Host-cairn_") || c.Domain != "" || c.Path != "/" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
			t.Errorf("cookie %s: domain=%q path=%q secure=%v httpOnly=%v sameSite=%v", c.Name, c.Domain, c.Path, c.Secure, c.HttpOnly, c.SameSite)
		}
		cookies = append(cookies, c.Name+"="+c.Value)
		if c.Name == "__Host-cairn_session" {
			session = c.Value
		}
	}
	if session == "" || len(cookies) != 2 {
		t.Fatalf("cookies of a sign-in: %v", cookies)
	}
	// Everything the browser would hand to the website: both cookies.
	cookie := strings.Join(cookies, "; ")
	key, _ := signedIn["sessionKey"].(string)

	// The website's code replays the cookies from another address with
	// another client, adding whatever headers it likes.
	thief := func(method, path string, body any, extra ...string) (int, map[string]any) {
		t.Helper()
		h := map[string]string{"Cookie": cookie, "User-Agent": "site-code/1.0", "X-Cairn-Request": "1"}
		for i := 0; i+1 < len(extra); i += 2 {
			h[extra[i]] = extra[i+1]
		}
		s, b, _ := rawRequest(t, e, "127.0.0.66", method, path, body, h)
		return s, b
	}
	refused := func(what string, status int, body map[string]any) {
		t.Helper()
		if status != 401 {
			t.Fatalf("%s: want 401, got %d %v", what, status, body)
		}
	}
	evil := map[string]any{"username": "evil", "password": pw, "role": "admin"}
	replay := func(when string) {
		t.Helper()
		s, b := thief("GET", "/api/v1/auth/me", nil)
		refused(when+": GET /auth/me with the cookies alone", s, b)
		if _, told := b["elevatedUntil"]; told {
			t.Fatalf("%s: the refusal tells the step-up state: %v", when, b)
		}
		s, b = thief("PUT", "/api/v1/me/theme", map[string]any{"theme": "dark"})
		refused(when+": PUT /me/theme with the cookies alone", s, b)
		s, b = thief("PUT", "/api/v1/files/content", map[string]any{"root": "sites", "path": "other/app/x.php", "content": "<?php", "etag": ""})
		refused(when+": PUT /files/content with the cookies alone", s, b)
		s, b = thief("POST", "/api/v1/users", evil)
		refused(when+": POST /users with the cookies alone", s, b)
		s, b = thief("PUT", "/api/v1/terminal/settings", map[string]any{"rootTerminal": true})
		refused(when+": PUT /terminal/settings with the cookies alone", s, b)
		s, b = thief("POST", "/api/v1/me/tokens", map[string]any{"name": "evil", "permissions": []string{"system.read"}})
		refused(when+": POST /me/tokens with the cookies alone", s, b)
		// Guesses at the key: the cookie's own value, the constant of the
		// old header, something made up.
		for _, guess := range []string{session, "1", "bm90IHRoZSBzZXNzaW9uIGtleQ"} {
			s, b = thief("GET", "/api/v1/auth/me", nil, "X-Cairn-Session", guess)
			refused(when+": GET /auth/me with a guessed key", s, b)
			s, b = thief("POST", "/api/v1/users", evil, "X-Cairn-Session", guess)
			refused(when+": POST /users with a guessed key", s, b)
		}
	}
	replay("before the step-up")

	// The administrator's own page holds the key and works; it re-enters
	// the password.
	own := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		s, b, _ := rawRequest(t, e, "127.0.0.3", method, path, body,
			map[string]string{"Cookie": cookie, "X-Cairn-Session": key, "User-Agent": adminUA})
		return s, b
	}
	if s, me := own("GET", "/api/v1/auth/me", nil); s != 200 {
		t.Fatalf("the administrator's own page: GET /auth/me -> %d %v", s, me)
	}
	if s, b := own("POST", "/api/v1/auth/elevate", map[string]any{"password": pw}); s != 200 {
		t.Fatalf("the administrator's own page: POST /auth/elevate -> %d %v", s, b)
	}
	// The window the administrator opened is of no use to the copy.
	replay("inside the administrator's step-up window")
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM users WHERE username = 'evil'`).Scan(&n)
	if n != 0 {
		t.Fatal("the copy created an account")
	}
	// The replays did not cost the administrator the session either.
	if s, b := own("PUT", "/api/v1/me/theme", map[string]any{"theme": "dark"}); s != 204 {
		t.Fatalf("the administrator's own page after the replays: PUT /me/theme -> %d %v", s, b)
	}
	if s, b := own("POST", "/api/v1/users", map[string]any{"username": "colleague", "password": pw, "role": "operator"}); s != 201 {
		t.Fatalf("the administrator's own page inside the window: POST /users -> %d %v", s, b)
	}
}
