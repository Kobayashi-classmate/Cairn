// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

func (e *env) count(q string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

// lockouts collects the lockout notices published from now on.
func (e *env) lockouts() func() []events.Event {
	var mu sync.Mutex
	var got []events.Event
	e.bus.Subscribe(func(ev events.Event) {
		if ev.Type == events.SecurityLockout {
			mu.Lock()
			got = append(got, ev)
			mu.Unlock()
		}
	})
	return func() []events.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]events.Event(nil), got...)
	}
}

// A sign-in takes any text as the username, from anyone: what no account
// can be called is a wrong password like any other, with no throttle key
// of its own, and recorded short and without control characters.
func TestSignInWithAnyUsername(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin("admin")
	lockouts := e.lockouts()
	name := "x\r\nSubject: your panel was breached, sign in at http://evil.example/\x1b[31m\x00\u202e" + strings.Repeat("A", 200_000)
	c := e.clientFrom("127.0.0.9")
	for range 5 {
		c.expect(c.login(name, "wrong password 1234"), 401, "auth.invalid_credentials")
	}
	// The address is locked as after any five wrong passwords.
	c.expect(c.login("admin", pw), 429, "auth.rate_limited")
	if n := e.count(`SELECT count(*) FROM login_failures WHERE key LIKE 'user:%'`); n != 0 {
		t.Fatalf("%d throttle keys for usernames", n)
	}
	rows, err := e.db.Query(`SELECT username FROM audit_log WHERE action = 'auth.login' AND result <> 'ok'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			t.Fatal(err)
		}
		names = append(names, u)
	}
	_ = rows.Close()
	if len(names) != 5 {
		t.Fatalf("%d audit rows", len(names))
	}
	for _, u := range names {
		if len(u) > 64 || strings.ContainsAny(u, "\r\n\x1b\x00\u202e") || !strings.HasPrefix(u, "xSubject: ") {
			t.Fatalf("audited as %q", u)
		}
	}
	ev := lockouts()
	if len(ev) != 1 {
		t.Fatalf("%d lockout notices", len(ev))
	}
	if u, _ := ev[0].Params["username"].(string); len(u) > 64 || strings.ContainsAny(u, "\r\n\x1b\x00") {
		t.Fatalf("notice names %q", u)
	}

	// A name an account could have still gets its own key.
	other := e.clientFrom("127.0.0.10")
	other.expect(other.login("ghost", "wrong password 1234"), 401, "auth.invalid_credentials")
	if n := e.count(`SELECT count(*) FROM login_failures WHERE key = 'user:ghost'`); n != 1 {
		t.Fatalf("user:ghost counted %d times", n)
	}
	other.expect(other.login("admin", pw), 200, "")
}

// One lockout notice per address, whatever names were tried from it.
func TestLockoutNoticeIsPerAddress(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin("admin")
	lockouts := e.lockouts()
	c := e.clientFrom("127.0.0.11")
	for i := range 5 {
		c.expect(c.login(fmt.Sprintf("user%d", i), "wrong password 1234"), 401, "auth.invalid_credentials")
	}
	ev := lockouts()
	if len(ev) != 1 || ev[0].DedupeKey != "lockout:"+auth.IPKey("127.0.0.11") {
		t.Fatalf("notices %+v", ev)
	}
}

// Before the first administrator exists, the setup token is tried like a
// password: counted per address, and a wrong one recorded without the
// username sent with it.
func TestSetupTokenIsThrottled(t *testing.T) {
	e := newEnv(t)
	guesser := e.clientFrom("127.0.0.12")
	name := "x\r\n" + strings.Repeat("A", 100_000)
	for range 5 {
		guesser.expect(guesser.do("POST", "/api/v1/setup", map[string]any{"token": "wrong", "username": name, "password": pw}), 400, "setup.invalid_token")
	}
	guesser.expect(guesser.do("POST", "/api/v1/setup", map[string]any{"token": "wrong", "username": name, "password": pw}), 429, "auth.rate_limited")
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'auth.setup' AND username = ''`); n != 5 {
		t.Fatalf("%d anonymous setup rows", n)
	}
	if n := e.count(`SELECT coalesce(sum(length(username)), 0) FROM audit_log`); n != 0 {
		t.Fatalf("%d bytes of usernames audited", n)
	}
	// The administrator, from another address, is not held up; and once
	// there is one, setup answers that it is done without counting.
	e.setupAdmin("admin")
	for range 6 {
		guesser.expect(guesser.do("POST", "/api/v1/setup", map[string]any{"token": "wrong", "username": "b", "password": pw}), 409, "setup.already_completed")
	}
}

// A client that half-closes its connection while its password is checked
// ends the request's context. That must not leave the attempt unrecorded
// (and the lockout unannounced), nor turn the answer to the right password
// into an error: that would tell it from a wrong one.
func TestHalfClosedSignInIsRecorded(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin("admin")
	lockouts := e.lockouts()
	addr := strings.TrimPrefix(e.srv.URL, "https://")
	// signIn sends a sign-in over a connection of its own from ip and,
	// with delay >= 0, half-closes it that long after the request went
	// out (TLS close_notify, then FIN). It still reads the answer.
	signIn := func(ip, username, password string, delay time.Duration) (int, time.Duration) {
		t.Helper()
		d := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}, Timeout: 5 * time.Second}
		conn, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}}) //nolint:gosec // the test server's certificate
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		body := fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
		start := time.Now()
		_, _ = fmt.Fprintf(conn, "POST /api/v1/auth/login HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nX-Cairn-Request: 1\r\nContent-Length: %d\r\n\r\n%s",
			addr, len(body), body)
		if delay >= 0 {
			time.Sleep(delay)
			_ = conn.CloseWrite()
		}
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		res, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			return 0, time.Since(start)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		return res.StatusCode, time.Since(start)
	}
	audited := func(username string) int64 {
		return e.count(`SELECT count(*) FROM audit_log WHERE action = 'auth.login' AND username = ?`, username)
	}
	status, took := signIn("127.0.7.1", "ghost", "wrong password 0", -1)
	if status != 401 || audited("ghost") != 1 {
		t.Fatalf("connection left open: %d, %d audit rows", status, audited("ghost"))
	}
	// Half-closed at points through the password check.
	for i, part := range []float64{0.2, 0.35, 0.5, 0.65, 0.8} {
		user := fmt.Sprintf("ghost%d", i)
		status, _ := signIn(fmt.Sprintf("127.0.7.%d", i+2), user, "wrong password", time.Duration(float64(took)*part))
		if status != 401 || audited(user) != 1 {
			t.Errorf("half-closed after %.0f%% of a sign-in: %d, %d audit rows", part*100, status, audited(user))
		}
	}
	// Five of them from one address lock it, and say so.
	for i := range 5 {
		signIn("127.0.8.1", fmt.Sprintf("victim%d", i), "wrong password", took/2)
	}
	if n := len(lockouts()); n != 1 {
		t.Errorf("%d lockout notices after five half-closed failures", n)
	}
	// The right password signs in all the same.
	for i := range 3 {
		if status, _ := signIn(fmt.Sprintf("127.0.9.%d", i+1), "admin", pw, took/2); status != 200 {
			t.Errorf("right password, half-closed: %d", status)
		}
	}
}

// What a URL to download carries after the path (a presigned link's
// signature, a ?private_token=) is a credential: it stays out of the audit
// log, the job's parameters, its log and its error.
func TestFetchKeepsTheQueryOutOfRecords(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionFileRoots, helperapi.FileRoots{Roots: []helperapi.FileRoot{{ID: "sites", Path: "/srv/www", Available: true}}})
	e.opsSucceed(helperapi.ActionFilePlace)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "remote file") }))
	defer src.Close()
	redirect := httptest.NewServer(http.RedirectHandler(src.URL+"/dl/readme.txt?X-Amz-Signature=signedsecret", http.StatusFound))
	defer redirect.Close()
	e.files.HTTP = src.Client()
	// A fresh installation: the uploads directory does not exist yet, and
	// the first download must not fail for want of space in it.
	if _, err := os.Stat(filepath.Join(e.dataDir, "uploads")); err == nil {
		t.Fatal("uploads exists before the first transfer")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	_ = ln.Close()

	job := admin.expect(admin.do("POST", "/api/v1/files/fetch", map[string]any{"root": "sites", "dir": "shop/app",
		"url": redirect.URL + "/get/readme.txt?private_token=tokensecret#fragsecret"}), 202, "").obj("job")
	final, log := waitJob(t, admin, job["id"].(string))
	if final["state"] != "succeeded" || !strings.Contains(log, src.URL+"/dl/readme.txt") {
		t.Fatalf("fetch %v\n%s", final, log)
	}
	if p, _ := final["params"].(map[string]any); p["url"] != redirect.URL+"/get/readme.txt" {
		t.Fatalf("job params %v", final["params"])
	}
	failed := admin.expect(admin.do("POST", "/api/v1/files/fetch", map[string]any{"root": "sites", "dir": "shop/app",
		"url": "http://" + closed + "/f.txt?token=failsecret"}), 202, "").obj("job")
	final, failedLog := waitJob(t, admin, failed["id"].(string))
	errInfo, _ := final["error"].(map[string]any)
	params, _ := errInfo["params"].(map[string]any)
	detail, _ := params["detail"].(string)
	if final["state"] != "failed" || !strings.Contains(detail, closed+"/f.txt") {
		t.Fatalf("failed fetch %v\n%s", final, failedLog)
	}

	var records strings.Builder
	for _, q := range []string{`SELECT params FROM audit_log`, `SELECT params || error_params FROM jobs`} {
		rows, err := e.db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			records.WriteString(s + "\n")
		}
		_ = rows.Close()
	}
	records.WriteString(log + failedLog + detail)
	for _, secret := range []string{"tokensecret", "fragsecret", "signedsecret", "failsecret"} {
		if strings.Contains(records.String(), secret) {
			t.Errorf("%s recorded:\n%s", secret, records.String())
		}
	}
	if !strings.Contains(records.String(), `"url":"`+redirect.URL+`/get/readme.txt"`) {
		t.Errorf("the URL is not recorded:\n%s", records.String())
	}
}
