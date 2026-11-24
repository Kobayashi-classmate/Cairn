// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// tab is another tab of the same browser: it shares the cookies of c and
// holds the given session key (none for a tab that never signed in).
func (c *client) tab(key string) *client {
	return &client{e: c.e, hc: c.hc, key: key}
}

var routeParam = regexp.MustCompile(`\{[A-Za-z0-9_.]+\}`)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// Every route that takes a sign-in refuses the session cookie when the
// session key is missing or wrong, and a ticket opens nothing but the
// routes that take tickets.
func TestEveryRouteNeedsTheSessionKey(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	admin.elevate("")
	e.helper.respond(helperapi.ActionFileRoots, helperapi.FileRoots{Roots: []helperapi.FileRoot{{ID: "sites", Path: "/srv/www", Available: true}}})
	bare := admin.tab("")
	wrong := admin.tab("bm90IHRoZSBzZXNzaW9uIGtleQ")
	// A real ticket, for a route that takes one, is not a key anywhere else.
	download := admin.ticket("/api/v1/files/download?root=sites&path=x")
	ticket := strings.SplitN(download, "ticket=", 2)[1]
	checked, tickets := 0, 0
	for _, r := range e.router.Routes() {
		if r.Public {
			continue
		}
		path := routeParam.ReplaceAllString(r.Path, "x")
		check := func(what string, got reply, code string) {
			t.Helper()
			// A HEAD answer has no body to carry the code.
			if got.status != 401 || r.Method != http.MethodHead && got.code() != code {
				t.Errorf("%s %s %s: want 401 %s, got %d %s", r.Method, r.Path, what, code, got.status, got.code())
			}
		}
		check("with the cookie alone", bare.do(r.Method, path, nil), "auth.unauthenticated")
		check("with a wrong key", wrong.do(r.Method, path, nil), "auth.session_expired")
		if r.Ticket {
			tickets++
			check("with a made-up ticket", bare.do(r.Method, path+"?ticket=bm90IGEgdGlja2V0", nil), "auth.session_expired")
			continue
		}
		check("with a ticket for another route", bare.do(r.Method, path+"?ticket="+ticket, nil), "auth.unauthenticated")
		checked++
	}
	if checked < 100 || tickets != 4 {
		t.Fatalf("checked %d routes and %d that take tickets", checked, tickets)
	}
	// None of that spent the ticket: it still opens its own route (the
	// helper has no such file, which is the route's answer, not a refusal).
	if status, _ := bare.link(download); status == 401 {
		t.Fatal("the ticket was spent on routes that take none")
	}
	// The session survived all of it.
	admin.expect(admin.do("GET", "/api/v1/auth/me", nil), 200, "")
	// A request that changes something still needs a custom header, and
	// the page's own origin.
	admin.expect(admin.do("PUT", "/api/v1/me/theme", map[string]any{"theme": "dark"}, api.SessionHeader, ""), 403, "request.csrf_failed")
	admin.expect(admin.do("PUT", "/api/v1/me/theme", map[string]any{"theme": "dark"}, "Origin", "https://"+strings.TrimPrefix(e.srv.URL, "https://")+".evil.example"), 403, "request.csrf_failed")
	admin.expect(admin.do("PUT", "/api/v1/me/theme", map[string]any{"theme": "dark"}, "Origin", e.srv.URL), 204, "")
	// The constant header marks a request as the page's; it is not a key.
	admin.expect(admin.do("PUT", "/api/v1/me/theme", map[string]any{"theme": "dark"}, api.SessionHeader, "", api.CSRFHeader, "1"), 401, "auth.unauthenticated")
}

// The session key is told once, by the sign-in, and kept nowhere a reader
// of the panel's records could find it.
func TestSessionKeyIsToldOnce(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	key := admin.key
	if len(key) < 40 {
		t.Fatalf("session key %q", key)
	}
	second := e.client()
	r := second.expect(second.login("admin", pw), 200, "")
	if k, _ := r.body["sessionKey"].(string); k == "" || k == key {
		t.Fatalf("a second sign-in: key %q, the first one's %q", k, key)
	}
	// The key of one session does not open another.
	other := second.tab(key)
	other.expect(other.do("GET", "/api/v1/auth/me", nil), 401, "auth.session_expired")
	admin.elevate("")
	for _, path := range []string{"/api/v1/auth/me", "/api/v1/me/sessions", "/api/v1/audit?limit=50", "/api/v1/users"} {
		if r := admin.expect(admin.do("GET", path, nil), 200, ""); strings.Contains(r.raw, key) || strings.Contains(r.raw, "sessionKey") {
			t.Errorf("GET %s tells the session key: %s", path, r.raw)
		}
	}
	for _, q := range []string{
		`SELECT count(*) FROM audit_log WHERE params LIKE '%' || ? || '%'`,
		`SELECT count(*) FROM sessions WHERE id = ? OR key_hash = ? OR user_agent = ?`,
		`SELECT count(*) FROM login_failures WHERE key LIKE '%' || ? || '%'`,
	} {
		args := make([]any, strings.Count(q, "?"))
		for i := range args {
			args[i] = key
		}
		var n int
		if err := e.db.QueryRow(q, args...).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d %v", q, n, err)
		}
	}
}

// A browser's WebSocket handshake carries cookies and no headers of the
// page's choosing: the terminal takes a one-time ticket instead, and the
// cookie alone attaches to nothing.
func TestTerminalAttachTakesATicket(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionTermUsers, helperapi.TermUsers{RootTerminal: true, Users: []helperapi.TermUser{
		{Name: "cw-shop", Kind: "site", Home: "/srv/www/shop", Site: "shop"}, {Name: "root", Kind: "root", Home: "/root"}}})
	e.helper.respond(helperapi.ActionTermOpen, helperapi.TermSession{PID: 42, User: "root", Shell: "/bin/bash", Home: "/root"})
	var shells []*os.File
	e.helper.giveFile(helperapi.ActionTermOpen, func() (*os.File, error) {
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
		if err != nil {
			return nil, err
		}
		_ = syscall.SetNonblock(fds[0], true)
		shells = append(shells, os.NewFile(uintptr(fds[1]), "shell"))
		return os.NewFile(uintptr(fds[0]), "master"), nil
	})
	defer func() {
		for _, s := range shells {
			_ = s.Close()
		}
	}()
	admin.elevate("")
	open := map[string]any{"user": "root", "cols": 80, "rows": 24}
	id := admin.expect(admin.do("POST", "/api/v1/terminal/sessions", open), 201, "").body["id"].(string)
	other := admin.expect(admin.do("POST", "/api/v1/terminal/sessions", open), 201, "").body["id"].(string)
	ws := "/api/v1/terminal/sessions/" + id + "/ws"

	// dial is the browser's handshake: the cookie jar of c, the address,
	// and no header of the page's.
	dial := func(c *client, path string) (*websocket.Conn, int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, res, err := websocket.Dial(ctx, strings.Replace(e.srv.URL, "https://", "wss://", 1)+path, &websocket.DialOptions{HTTPClient: c.hc})
		status := 0
		if res != nil {
			status = res.StatusCode
			if res.Body != nil {
				_ = res.Body.Close()
			}
		}
		if err != nil {
			return nil, status
		}
		return conn, status
	}
	refused := func(what string, c *client, path string) {
		t.Helper()
		if conn, status := dial(c, path); conn != nil || status != 401 {
			if conn != nil {
				_ = conn.Close(websocket.StatusNormalClosure, "")
			}
			t.Errorf("%s: want 401, got %d (attached: %v)", what, status, conn != nil)
		}
	}
	attached := func() int {
		n := 0
		for _, s := range admin.expect(admin.do("GET", "/api/v1/terminal/sessions", nil), 200, "").body["sessions"].([]any) {
			if s.(map[string]any)["attached"] == true {
				n++
			}
		}
		return n
	}

	refused("the cookie alone", admin, ws)
	refused("a made-up ticket", admin, ws+"?ticket=bm90IGEgdGlja2V0")
	// A ticket is for one address: not another terminal, not another route.
	refused("a ticket for another terminal", admin, strings.Replace(admin.ticket("/api/v1/terminal/sessions/"+other+"/ws"), other, id, 1))
	e.helper.respond(helperapi.ActionFileRoots, helperapi.FileRoots{Roots: []helperapi.FileRoot{{ID: "sites", Path: "/srv/www", Available: true}}})
	download := admin.ticket("/api/v1/files/download?root=sites&path=x")
	refused("a ticket for a download", admin, ws+"?ticket="+strings.SplitN(download, "ticket=", 2)[1])
	// A ticket is for one session: another sign-in of the same account
	// cannot use it, with its own cookie or without any.
	second := e.client()
	second.expect(second.login("admin", pw), 200, "")
	refused("another session's ticket", second, admin.ticket(ws))
	refused("a ticket without the cookie", e.client(), admin.ticket(ws))
	refused("the other session's ticket with this session's cookie", admin, second.ticket(ws))
	// An expired ticket.
	late := admin.ticket(ws)
	if _, err := e.db.Exec(`UPDATE session_tickets SET expires_at = ?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	refused("an expired ticket", admin, late)
	if n := attached(); n != 0 {
		t.Fatalf("%d terminals attached after refusals only", n)
	}

	// The right ticket attaches, once.
	once := admin.ticket(ws)
	conn, status := dial(admin, once)
	if conn == nil {
		t.Fatalf("attaching with a ticket: %d", status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("id\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if n, err := shells[0].Read(buf); err != nil || string(buf[:n]) != "id\n" {
		t.Fatalf("input reached the shell as %q, %v", buf[:n], err)
	}
	refused("a ticket used before", admin, once)
	_ = conn.Close(websocket.StatusNormalClosure, "")

	// Tickets end with the session: one in hand at sign-out is worth nothing
	// afterwards, with the cookie the browser had.
	held := second.ticket("/api/v1/terminal/sessions/" + id + "/ws")
	cookies := second.hc.Jar.Cookies(mustURL(t, e.srv.URL))
	second.expect(second.do("POST", "/api/v1/auth/logout", nil), 204, "")
	second.hc.Jar.SetCookies(mustURL(t, e.srv.URL), cookies)
	refused("a ticket of a signed-out session", second, held)
	second.expect(second.do("GET", "/api/v1/auth/me", nil), 401, "auth.session_expired")
	// A revoked session: its key and its tickets go with it.
	third := e.client()
	third.expect(third.login("admin", pw), 200, "")
	held = third.ticket(ws)
	if revoked := admin.expect(admin.do("DELETE", "/api/v1/me/sessions", nil), 200, "").body["revoked"]; revoked != float64(1) {
		t.Fatalf("revoked %v", revoked)
	}
	refused("a ticket of a revoked session", third, held)
	third.expect(third.do("GET", "/api/v1/auth/me", nil), 401, "auth.session_expired")
	third.expect(third.do("POST", "/api/v1/auth/tickets", map[string]any{"path": ws}), 401, "auth.session_expired")
	var left int
	_ = e.db.QueryRow(`SELECT count(*) FROM session_tickets WHERE session_id NOT IN (SELECT id FROM sessions)`).Scan(&left)
	if left != 0 {
		t.Fatalf("%d tickets outlived their sessions", left)
	}
}

// Download links and images are fetched by the browser itself: a ticket in
// the address stands in for the session key, for that file only, and the
// route's own checks (step-up for roots beyond the sites) still apply when
// it is used.
func TestDownloadsTakeATicket(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionFileRoots, helperapi.FileRoots{Roots: []helperapi.FileRoot{
		{ID: "sites", Path: "/srv/www", Available: true}, {ID: "backups", Path: "/var/lib/cairn/backups", Available: true}}})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "logo.svg"), []byte("<svg/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.helper.respond(helperapi.ActionFileOpen, helperapi.FileEntry{Name: "logo.svg", Size: 6, ModTime: time.Now().Unix()})
	e.helper.giveFile(helperapi.ActionFileOpen, func() (*os.File, error) { return os.Open(filepath.Join(dir, "logo.svg")) })
	opened := func() int { return strings.Count(strings.Join(e.helper.callsOf(), ","), helperapi.ActionFileOpen) }

	image := "/api/v1/files/download?root=sites&path=shop/app/logo.svg&inline=true"
	if status, _ := admin.link(image); status != 401 {
		t.Fatalf("an image with the cookie alone: %d", status)
	}
	link := admin.ticket(image)
	if status, body := admin.link(link); status != 200 || string(body) != "<svg/>" {
		t.Fatalf("an image with a ticket: %d %q", status, body)
	}
	if status, _ := admin.link(link); status != 401 {
		t.Fatalf("the ticket worked twice: %d", status)
	}
	// The parameters may come in any order; a ticket for one file or one
	// way of showing it is not one for another.
	ticket := strings.SplitN(admin.ticket(image), "ticket=", 2)[1]
	if status, _ := admin.link("/api/v1/files/download?ticket=" + ticket + "&inline=true&path=shop/app/logo.svg&root=sites"); status != 200 {
		t.Fatalf("the same request, parameters reordered: %d", status)
	}
	before := opened()
	for what, path := range map[string]string{
		"another file":       "/api/v1/files/download?root=sites&path=shop/app/wp-config.php&inline=true",
		"another root":       "/api/v1/files/download?root=backups&path=shop/app/logo.svg&inline=true",
		"as an attachment":   "/api/v1/files/download?root=sites&path=shop/app/logo.svg",
		"an extra parameter": image + "&x=1",
		"a doubled path":     image + "&path=shop/app/wp-config.php",
	} {
		ticket := strings.SplitN(admin.ticket(image), "ticket=", 2)[1]
		if status, _ := admin.link(path + "&ticket=" + ticket); status != 401 {
			t.Errorf("a ticket for the image used for %s: %d", what, status)
		}
	}
	if opened() != before {
		t.Fatal("a refused ticket reached the helper")
	}
	// A ticket in hand does not get past the step-up of the root: it is
	// checked when the ticket is used.
	system := "/api/v1/files/download?root=backups&path=db/shop.sql.zst"
	if status, _ := admin.link(admin.ticket(system)); status != 403 {
		t.Fatalf("a ticket for a root that needs step-up, not elevated: %d", status)
	}
	admin.elevate("")
	if status, _ := admin.link(admin.ticket(system)); status != 200 {
		t.Fatalf("the same, elevated: %d", status)
	}

	// Tickets are for the routes that take them, asked for by their path.
	for _, bad := range []string{"", "/api/v1/auth/me", "/api/v1/users", "/api/v1/files/content?root=sites&path=x", "/api/v1/files/../files/download?root=sites&path=x",
		"https://evil.example/api/v1/files/download?root=sites&path=x", "//evil.example/api/v1/files/download", "api/v1/files/download",
		"/api/v1/files/download?root=sites&path=x#frag", "/api/v1/files/download?" + strings.Repeat("x", 17<<10)} {
		admin.expect(admin.do("POST", "/api/v1/auth/tickets", map[string]any{"path": bad}), 400, "validation.invalid")
	}
	// Only the page that holds the session key gets tickets: not the cookie
	// alone, not a ticket, not an API token.
	bare := admin.tab("")
	bare.expect(bare.do("POST", "/api/v1/auth/tickets", map[string]any{"path": image}), 401, "auth.unauthenticated")
	bare.expect(bare.do("POST", "/api/v1/auth/tickets?ticket="+ticket, map[string]any{"path": image}), 401, "auth.unauthenticated")
	token := admin.expect(admin.do("POST", "/api/v1/me/tokens", map[string]any{"name": "ci", "permissions": []string{"files.read"}, "expiresInDays": 1}), 201, "").body["token"].(string)
	if r := e.bearer(token, "POST", "/api/v1/auth/tickets", map[string]any{"path": image}); r.status != 403 || r.code() != "auth.forbidden" {
		t.Fatalf("a ticket for an API token: %d %s", r.status, r.code())
	}
	// A token sends its header itself and needs no ticket.
	if r := e.bearer(token, "GET", image, nil); r.status != 200 {
		t.Fatalf("download with an API token: %d %s", r.status, r.raw)
	}
	// A session cannot pile up tickets.
	var last reply
	for range 80 {
		if last = admin.do("POST", "/api/v1/auth/tickets", map[string]any{"path": image}); last.status != 200 {
			break
		}
	}
	admin.expect(last, 429, "auth.rate_limited")
	if raw, _ := json.Marshal(last.params()); !strings.Contains(string(raw), "retryAfterSeconds") {
		t.Fatalf("rate limit %s", raw)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'file.download' AND result = 'ok'`).Scan(&n)
	if n != 4 {
		t.Fatalf("downloads audited: %d", n)
	}
}

// The device cookie reaches a website on the panel's host name like the
// session cookie does. Alone it must not make a known device, or whoever
// holds it could guess the account's password past the username's lock.
func TestDeviceCookieAloneIsNotAKnownDevice(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin("admin")
	owner := e.clientFrom("127.0.0.2")
	r := owner.expect(owner.login("admin", pw), 200, "")
	if k, _ := r.body["deviceKey"].(string); len(k) < 40 {
		t.Fatalf("the first sign-in of a browser tells no device key: %v", r.body)
	}
	owner.expect(owner.do("POST", "/api/v1/auth/logout", nil), 204, "")
	// Signing in again from the known device makes no new one.
	if again := owner.expect(owner.login("admin", pw), 200, ""); again.body["deviceKey"] != nil {
		t.Fatalf("a known device was given another key: %v", again.body)
	}
	owner.expect(owner.do("POST", "/api/v1/auth/logout", nil), 204, "")
	var device *http.Cookie
	for _, c := range owner.hc.Jar.Cookies(mustURL(t, e.srv.URL)) {
		if c.Name == auth.DeviceCookieName {
			device = c
		}
	}
	if device == nil {
		t.Fatal("no device cookie")
	}
	// Guesses spread over many addresses lock the username.
	for i := range 5 {
		sprayer := e.clientFrom(fmt.Sprintf("127.0.1.%d", i+1))
		sprayer.expect(sprayer.login("admin", "definitely wrong"), 401, "auth.invalid_credentials")
	}
	// A holder of the cookie alone is locked out like anyone else, with
	// the right password too ...
	thief := e.clientFrom("127.0.1.66")
	thief.hc.Jar.SetCookies(mustURL(t, e.srv.URL), []*http.Cookie{device})
	thief.expect(thief.login("admin", pw), 429, "auth.rate_limited")
	thief.device = device.Value
	thief.expect(thief.login("admin", pw), 429, "auth.rate_limited")
	// ... and the owner's browser, which has the key as well, gets in.
	owner.expect(owner.login("admin", pw), 200, "")
}
