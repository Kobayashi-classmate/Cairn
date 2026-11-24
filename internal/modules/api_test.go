// SPDX-License-Identifier: AGPL-3.0-only

// Package modules_test exercises the HTTP API end to end with every P0
// module wired together as in cairn-web, over real TLS with real cookies.
package modules_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/metrics"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/auditlog"
	backupsmod "github.com/Kobayashi-classmate/Cairn/internal/modules/backups"
	certsmod "github.com/Kobayashi-classmate/Cairn/internal/modules/certs"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/cloudsvc"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/core"
	cronmod "github.com/Kobayashi-classmate/Cairn/internal/modules/cron"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/databases"
	filesmod "github.com/Kobayashi-classmate/Cairn/internal/modules/files"
	firewallmod "github.com/Kobayashi-classmate/Cairn/internal/modules/firewall"
	hostmod "github.com/Kobayashi-classmate/Cairn/internal/modules/host"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/jobcenter"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/logs"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/notifications"
	sitesmod "github.com/Kobayashi-classmate/Cairn/internal/modules/sites"
	softwaremod "github.com/Kobayashi-classmate/Cairn/internal/modules/software"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/sshmod"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/system"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/terminal"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/users"
	"github.com/Kobayashi-classmate/Cairn/internal/notify"
	"github.com/Kobayashi-classmate/Cairn/internal/secrets"
	"github.com/Kobayashi-classmate/Cairn/internal/store"
)

// fakeHelper stands in for the root helper: it records calls and requests
// and answers with canned responses.
type fakeHelper struct {
	mu        sync.Mutex
	err       error
	calls     []string
	requests  map[string]json.RawMessage
	responses map[string]any
	errs      map[string]error // per-action failures
	// fds answer CallFD per action.
	fds map[string]func() (*os.File, error)
}

// CallFD answers like Call, with the file fds gives for the action.
func (f *fakeHelper) CallFD(ctx context.Context, action string, caller helperapi.Caller, req, resp any) (*os.File, error) {
	if err := f.Call(ctx, action, caller, req, resp); err != nil {
		return nil, err
	}
	f.mu.Lock()
	fn := f.fds[action]
	f.mu.Unlock()
	if fn == nil {
		return nil, &helperapi.Error{Code: helperapi.CodeUnavailable}
	}
	return fn()
}

func (f *fakeHelper) giveFile(action string, fn func() (*os.File, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fds == nil {
		f.fds = map[string]func() (*os.File, error){}
	}
	f.fds[action] = fn
}

// callsOf returns the actions called so far.
func (f *fakeHelper) callsOf() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeHelper) called(action string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, action)
}

func (f *fakeHelper) Call(_ context.Context, action string, _ helperapi.Caller, req, resp any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, action)
	if f.requests == nil {
		f.requests = map[string]json.RawMessage{}
	}
	f.requests[action], _ = json.Marshal(req)
	if f.err != nil {
		return f.err
	}
	if err := f.errs[action]; err != nil {
		return err
	}
	if v, ok := f.responses[action]; ok && resp != nil {
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, resp)
	}
	return nil
}

// testResolver answers the names it lists and no others, so tests never
// look names up on the network.
type testResolver struct {
	mu    sync.Mutex
	names map[string][]netip.Addr
}

func (r *testResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ips, ok := r.names[host]; ok {
		return ips, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (r *testResolver) set(host string, ips ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names == nil {
		r.names = map[string][]netip.Addr{}
	}
	for _, ip := range ips {
		r.names[host] = append(r.names[host], netip.MustParseAddr(ip))
	}
}

type env struct {
	t        *testing.T
	srv      *httptest.Server
	db       *sql.DB
	helper   *fakeHelper
	router   *api.Router
	notifier *notify.Notifier
	bus      *events.Bus
	cloud    *cloudsvc.Module
	software *softwaremod.Module
	certs    *certsmod.Module
	sites    *sitesmod.Module
	files    *filesmod.Module
	terminal *terminal.Module
	cron     *cronmod.Module
	backups  *backupsmod.Module
	firewall *firewallmod.Module
	ssh      *sshmod.Module
	dataDir  string
	modules  *module.Registry
	jobs     *jobs.Manager
	resolver *testResolver
}

// newEnv wires the panel like cmd/cairn-web; extra modules are added last.
func newEnv(t *testing.T, extra ...module.Module) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "cairn.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &fakeHelper{}
	messages, err := i18n.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	software, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Wired like cmd/cairn-web.
	bus := &events.Bus{}
	notifyStore := &notify.Store{DB: db, Box: box}
	notifier := notify.New(notifyStore, messages, log)
	bus.Subscribe(notifier.Handle)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { notifier.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	jobManager := &jobs.Manager{DB: db, Dir: filepath.Join(dir, "jobs"), Helper: h, Events: bus, Log: log}
	if err := jobManager.Init(context.Background()); err != nil {
		t.Fatal(err)
	}

	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	router := api.NewRouter(core.Authenticator(db), log)
	registry := module.NewRegistry(db)
	certsMod := &certsmod.Module{Jobs: jobManager, Helper: h, DB: db, Box: box, Events: bus, Log: log}
	filesMod := &filesmod.Module{Jobs: jobManager, Helper: h, DB: db, Log: log, DataDir: dataDir}
	termMod := &terminal.Module{Helper: h, DB: db, Log: log}
	cronMod := &cronmod.Module{Jobs: jobManager, Helper: h, DB: db, Events: bus, Log: log}
	resolver := &testResolver{}
	backupsMod := &backupsmod.Module{Jobs: jobManager, Helper: h, DB: db, Box: box, Events: bus, Log: log, DataDir: dataDir, Resolver: resolver}
	sitesMod := &sitesmod.Module{Catalog: software, Jobs: jobManager, Helper: h, DB: db, Events: bus, Log: log, PanelPort: 28443,
		Certs: certsMod, Backups: backupsMod, Modules: registry}
	firewallMod := &firewallmod.Module{Jobs: jobManager, Helper: h, DB: db, Events: bus, Log: log}
	sshMod := &sshmod.Module{Jobs: jobManager, Helper: h, DB: db, Events: bus, Log: log}
	cloudMod := &cloudsvc.Module{DB: db, Helper: h, Events: bus, Settings: cloudsvc.NewSettings(config.Default().Cloud),
		Log: log, Version: "0.1.0", Family: "debian", Format: "deb", Arch: "amd64"}
	softwareMod := &softwaremod.Module{Catalog: software, Jobs: jobManager, Helper: h, DB: db, Log: log,
		Release: "debian-13", Chain: []string{"debian-13", "debian"}, Arch: "amd64"}
	registry.Add(
		&core.Module{DB: db, Settings: core.NewSettings("en"), Helper: h, Box: box, Events: bus, Log: log, Modules: registry},
		&system.Module{Metrics: metrics.NewCollector(time.Second, 10, log)},
		softwareMod,
		sitesMod,
		certsMod,
		&databases.Module{Jobs: jobManager, Helper: h, DB: db, Box: box, Log: log, DataDir: dataDir,
			Backups: backupsMod, Modules: registry},
		filesMod,
		termMod,
		cronMod,
		backupsMod,
		&logs.Module{Helper: h, Log: log},
		firewallMod,
		sshMod,
		&hostmod.Module{Jobs: jobManager, Helper: h, DB: db, Log: log},
		&jobcenter.Module{Jobs: jobManager, DB: db, Log: log},
		&users.Module{DB: db, Events: bus, Log: log, Revokers: []users.Revoker{termMod}},
		&auditlog.Module{DB: db},
		&notifications.Module{DB: db, Store: notifyStore, Notifier: notifier, Log: log},
		cloudMod,
	)
	registry.Add(extra...)
	if err := registry.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	registry.Mount(router)
	router.Handle("GET /api/v1/openapi.json", router.ServeOpenAPI("Cairn API", "test"),
		api.Require(authz.Self), api.Doc("meta", "This OpenAPI document"))
	srv := httptest.NewUnstartedServer(api.SecurityHeaders(api.Recover(log, router)))
	srv.Config.ConnContext = api.ConnContext
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, db: db, helper: h, router: router, notifier: notifier, bus: bus, cloud: cloudMod, software: softwareMod, certs: certsMod, sites: sitesMod,
		files: filesMod, terminal: termMod, cron: cronMod, backups: backupsMod, firewall: firewallMod, ssh: sshMod, dataDir: dataDir, modules: registry, jobs: jobManager,
		resolver: resolver}
}

// client is one browser tab: its own cookie jar and, once signed in, the
// session key the page keeps and sends with every request.
type client struct {
	e   *env
	hc  *http.Client
	key string
	// device is the device key the page keeps across sign-ins.
	device string
}

// authorize adds what the panel's page sends with every request: the
// session key, or before there is one the constant header on requests that
// change something; with a sign-in, the device key.
func (c *client) authorize(req *http.Request) {
	if c.key != "" {
		req.Header.Set(api.SessionHeader, c.key)
	} else if req.Method != http.MethodGet {
		req.Header.Set(api.CSRFHeader, "1")
	}
	if c.device != "" && strings.HasPrefix(req.URL.Path, "/api/v1/auth/login") {
		req.Header.Set(api.DeviceHeader, c.device)
	}
}

// link follows a download link the way a browser does: with the cookies
// and nothing else.
func (c *client) link(path string) (int, []byte) {
	c.e.t.Helper()
	res, err := c.hc.Get(c.e.srv.URL + path)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, data
}

// ticket trades the session key for a one-time ticket for a GET of path
// and returns the path with the ticket: what the page does before the
// browser opens a WebSocket or follows a download link.
func (c *client) ticket(path string) string {
	c.e.t.Helper()
	r := c.expect(c.do("POST", "/api/v1/auth/tickets", map[string]any{"path": path}), 200, "")
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + api.TicketParam + "=" + r.body["ticket"].(string)
}

func (e *env) client() *client {
	hc := e.srv.Client()
	jar, _ := cookiejar.New(nil)
	hc = &http.Client{Transport: hc.Transport, Jar: jar}
	return &client{e: e, hc: hc}
}

// clientFrom is a browser connecting from another loopback address (the
// whole of 127.0.0.0/8 reaches the test server), for per-address limits.
func (e *env) clientFrom(ip string) *client {
	tr := e.srv.Client().Transport.(*http.Transport).Clone()
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}, Timeout: 5 * time.Second}
	tr.DialContext = dialer.DialContext
	e.t.Cleanup(tr.CloseIdleConnections)
	jar, _ := cookiejar.New(nil)
	return &client{e: e, hc: &http.Client{Transport: tr, Jar: jar}}
}

type reply struct {
	status int
	body   map[string]any
	raw    string
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

func (r reply) obj(key string) map[string]any {
	o, _ := r.body[key].(map[string]any)
	return o
}

func (c *client) do(method, path string, body any, headers ...string) reply {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	c.authorize(req)
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	res, err := c.hc.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer res.Body.Close()
	out := reply{status: res.StatusCode, body: map[string]any{}}
	data, _ := io.ReadAll(res.Body)
	out.raw = string(data)
	if len(data) > 0 {
		_ = json.Unmarshal(data, &out.body)
	}
	// Setup and sign-in answer with the session key, once, and with the
	// device key on a browser not seen before.
	if k, _ := out.body["sessionKey"].(string); k != "" {
		c.key = k
	}
	if k, _ := out.body["deviceKey"].(string); k != "" {
		c.device = k
	}
	return out
}

func (c *client) expect(r reply, status int, code string) reply {
	c.e.t.Helper()
	if r.status != status || r.code() != code {
		c.e.t.Fatalf("want %d %q, got %d %q %v", status, code, r.status, r.code(), r.body)
	}
	return r
}

const pw = "a long enough password"

// setupAdmin completes first-run setup and returns the signed-in client.
func (e *env) setupAdmin(username string) *client {
	e.t.Helper()
	token, err := auth.CreateSetupToken(context.Background(), e.db, auth.SetupTokenTTL)
	if err != nil {
		e.t.Fatal(err)
	}
	c := e.client()
	r := c.do("POST", "/api/v1/setup", map[string]any{"token": token, "username": username, "password": pw, "locale": "en"})
	c.expect(r, 200, "")
	return c
}

func (c *client) login(username, password string) reply {
	return c.do("POST", "/api/v1/auth/login", map[string]any{"username": username, "password": password})
}

func (c *client) elevate(code string) {
	c.e.t.Helper()
	body := map[string]any{"password": pw}
	if code != "" {
		body["code"] = code
	}
	c.expect(c.do("POST", "/api/v1/auth/elevate", body), 200, "")
}

func totp(t *testing.T, secret string, offset time.Duration) string {
	t.Helper()
	code, err := auth.TOTPCode(secret, time.Now().Add(offset))
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestSetupSessionAndLanguage(t *testing.T) {
	e := newEnv(t)
	anon := e.client()
	if r := anon.do("GET", "/api/v1/meta", nil); r.body["setupRequired"] != true {
		t.Fatalf("meta before setup: %v", r.body)
	}
	anon.expect(anon.do("POST", "/api/v1/setup", map[string]any{"token": "nope", "username": "admin", "password": pw}), 400, "setup.invalid_token")
	anon.expect(anon.do("POST", "/api/v1/setup", map[string]any{"token": "x"}, api.CSRFHeader, ""), 403, "request.csrf_failed")

	admin := e.setupAdmin("admin")
	anon.expect(anon.do("POST", "/api/v1/setup", map[string]any{"token": "x", "username": "b", "password": pw}), 409, "setup.already_completed")

	me := admin.expect(admin.do("GET", "/api/v1/auth/me", nil), 200, "")
	if me.obj("user")["role"] != "admin" || me.obj("user")["totpEnabled"] != false {
		t.Fatalf("me: %v", me.body)
	}
	perms := fmt.Sprint(me.body["permissions"])
	for _, p := range []string{"users.manage", "audit.read", "settings.manage", "system.read"} {
		if !strings.Contains(perms, p) {
			t.Fatalf("admin lacks %s: %s", p, perms)
		}
	}

	admin.expect(admin.do("PUT", "/api/v1/me/locale", map[string]any{"locale": "zh-CN"}), 204, "")
	admin.expect(admin.do("PUT", "/api/v1/me/locale", map[string]any{"locale": "xx"}), 400, "locale.unsupported")
	admin.expect(admin.do("PUT", "/api/v1/me/locale", map[string]any{"locale": "en"}, "Origin", "https://evil.example"), 403, "request.csrf_failed")

	if got := admin.expect(admin.do("GET", "/api/v1/auth/me", nil), 200, "").obj("user")["theme"]; got != "system" {
		t.Fatalf("default theme: %v", got)
	}
	admin.expect(admin.do("PUT", "/api/v1/me/theme", map[string]any{"theme": "dark"}), 204, "")
	admin.expect(admin.do("PUT", "/api/v1/me/theme", map[string]any{"theme": "sepia"}), 400, "validation.invalid")
	if got := admin.expect(admin.do("GET", "/api/v1/auth/me", nil), 200, "").obj("user")["theme"]; got != "dark" {
		t.Fatalf("theme after change: %v", got)
	}

	admin.expect(admin.do("PUT", "/api/v1/settings/default-locale", map[string]any{"locale": "zh-CN"}), 204, "")
	if r := admin.do("GET", "/api/v1/settings", nil); r.body["defaultLocale"] != "zh-CN" {
		t.Fatalf("settings: %v", r.body)
	}
	e.helper.err = &helperapi.Error{Code: helperapi.CodeUnavailable}
	admin.expect(admin.do("PUT", "/api/v1/settings/default-locale", map[string]any{"locale": "en"}), 502, "helper.unavailable")
	e.helper.err = nil

	admin.expect(admin.do("POST", "/api/v1/auth/logout", nil), 204, "")
	admin.expect(admin.do("GET", "/api/v1/auth/me", nil), 401, "auth.unauthenticated")
	anon.expect(anon.do("GET", "/api/v1/nope", nil), 404, "request.not_found")
}

func TestLoginThrottling(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin("admin")
	c := e.client()
	for i := 0; i < 5; i++ {
		c.expect(c.login("admin", "definitely wrong"), 401, "auth.invalid_credentials")
	}
	r := c.expect(c.login("admin", pw), 429, "auth.rate_limited")
	if s, _ := r.params()["retryAfterSeconds"].(float64); s < 1 || s > 60 {
		t.Fatalf("retryAfterSeconds: %v", r.params())
	}
}

// Knowing an administrator's name must not let anyone keep them out: a
// browser they signed in from before is exempt from the username's lock,
// which still stops guessing spread over many addresses; failures of
// signed-in sessions count per session.
func TestThrottleKeepsOwnersIn(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	owner := e.clientFrom("127.0.0.2")
	owner.expect(owner.login("admin", pw), 200, "")
	owner.expect(owner.do("POST", "/api/v1/auth/logout", nil), 204, "")
	// A sprayer: a few guesses from each of many addresses, under the
	// limit of each address.
	for i := 0; i < 5; i++ {
		sprayer := e.clientFrom(fmt.Sprintf("127.0.1.%d", i+1))
		sprayer.expect(sprayer.login("admin", "definitely wrong"), 401, "auth.invalid_credentials")
	}
	// The username is locked now, also for the right password from a new
	// browser ...
	sprayer := e.clientFrom("127.0.1.10")
	if r := sprayer.expect(sprayer.login("admin", pw), 429, "auth.rate_limited"); r.params()["retryAfterSeconds"].(float64) < 30 {
		t.Fatalf("username lock %v", r.params())
	}
	// ... but not on the owner's browser.
	owner.expect(owner.login("admin", pw), 200, "")
	// A known device of another account does not count.
	admin.elevate("")
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "eve", "password": pw, "role": "auditor"}), 201, "")
	eve := e.clientFrom("127.0.1.11")
	eve.expect(eve.login("eve", pw), 200, "")
	eve.expect(eve.login("admin", pw), 429, "auth.rate_limited")
	// IPv6 addresses of one /64 share their address's counter.
	if auth.IPKey("2001:db8:1:2::5") != auth.IPKey("2001:db8:1:2:ffff::1") || auth.IPKey("2001:db8:1:2::5") == auth.IPKey("2001:db8:1:3::5") ||
		auth.IPKey("::ffff:198.51.100.7") != auth.IPKey("198.51.100.7") {
		t.Fatalf("address keys %s %s", auth.IPKey("2001:db8:1:2::5"), auth.IPKey("::ffff:198.51.100.7"))
	}

	// A session guessing its password locks itself, not the account.
	for i := 0; i < 5; i++ {
		owner.expect(owner.do("POST", "/api/v1/auth/elevate", map[string]any{"password": "definitely wrong"}), 400, "auth.wrong_password")
	}
	owner.expect(owner.do("POST", "/api/v1/auth/elevate", map[string]any{"password": pw}), 429, "auth.rate_limited")
	owner.expect(owner.do("PUT", "/api/v1/me/password", map[string]any{"currentPassword": pw, "newPassword": "another long password"}), 429, "auth.rate_limited")
	admin.elevate("")
	admin.expect(admin.do("PUT", "/api/v1/me/password", map[string]any{"currentPassword": pw, "newPassword": pw}), 204, "")
}

// A refused (rate-limited) second step does not use up the challenge.
func TestThrottledSecondStepKeepsTheChallenge(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	admin.elevate("")
	secret, _ := admin.expect(admin.do("POST", "/api/v1/me/totp/begin", nil), 200, "").body["secret"].(string)
	admin.expect(admin.do("POST", "/api/v1/me/totp/confirm", map[string]any{"code": totp(t, secret, 0)}), 200, "")
	c := e.clientFrom("127.0.2.1")
	token := c.expect(c.login("admin", pw), 200, "").body["mfaToken"].(string)
	// The address gets locked meanwhile (by others' failures from it).
	if err := auth.RecordFailure(context.Background(), e.db, slices.Repeat([]string{auth.IPKey("127.0.2.1")}, 5)...); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		c.expect(c.do("POST", "/api/v1/auth/login/mfa", map[string]any{"mfaToken": token, "code": "000000"}), 429, "auth.rate_limited")
	}
	if err := auth.ResetFailures(context.Background(), e.db, auth.IPKey("127.0.2.1")); err != nil {
		t.Fatal(err)
	}
	c.expect(c.do("POST", "/api/v1/auth/login/mfa", map[string]any{"mfaToken": token, "code": totp(t, secret, 30*time.Second)}), 200, "")
}

func TestPasswordChangeAndSessions(t *testing.T) {
	e := newEnv(t)
	a := e.setupAdmin("admin")
	b := e.client()
	b.expect(b.login("admin", pw), 200, "")

	list := a.expect(a.do("GET", "/api/v1/me/sessions", nil), 200, "")
	sessions, _ := list.body["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("want 2 sessions, got %v", list.body)
	}

	a.expect(a.do("PUT", "/api/v1/me/password", map[string]any{"currentPassword": "wrong password!", "newPassword": "another long password"}), 400, "auth.wrong_password")
	r := a.expect(a.do("PUT", "/api/v1/me/password", map[string]any{"currentPassword": pw, "newPassword": "short"}), 400, "validation.invalid")
	if r.params()["field"] != "newPassword" {
		t.Fatalf("field should be newPassword: %v", r.params())
	}
	a.expect(a.do("PUT", "/api/v1/me/password", map[string]any{"currentPassword": pw, "newPassword": "another long password"}), 204, "")
	// The other browser is signed out; this one stays.
	b.expect(b.do("GET", "/api/v1/auth/me", nil), 401, "auth.session_expired")
	a.expect(a.do("GET", "/api/v1/auth/me", nil), 200, "")
	a.expect(a.do("DELETE", "/api/v1/me/sessions/0000", nil), 404, "request.not_found")
}

func TestTOTPEnrollmentLoginAndStepUp(t *testing.T) {
	e := newEnv(t)
	a := e.setupAdmin("admin")

	// Enrollment requires step-up.
	a.expect(a.do("POST", "/api/v1/me/totp/begin", nil), 403, "auth.step_up_required")
	a.expect(a.do("POST", "/api/v1/auth/elevate", map[string]any{"password": "wrong password!"}), 400, "auth.wrong_password")
	a.elevate("")
	if r := a.do("GET", "/api/v1/auth/me", nil); r.body["elevatedUntil"].(float64) <= float64(time.Now().Unix()) {
		t.Fatalf("not elevated: %v", r.body)
	}
	begin := a.expect(a.do("POST", "/api/v1/me/totp/begin", nil), 200, "")
	secret, _ := begin.body["secret"].(string)
	if !strings.HasPrefix(begin.body["uri"].(string), "otpauth://totp/") || secret == "" {
		t.Fatalf("begin: %v", begin.body)
	}
	a.expect(a.do("POST", "/api/v1/me/totp/confirm", map[string]any{"code": "000000"}), 400, "auth.mfa_invalid_code")
	conf := a.expect(a.do("POST", "/api/v1/me/totp/confirm", map[string]any{"code": totp(t, secret, -30*time.Second)}), 200, "")
	codes, _ := conf.body["recoveryCodes"].([]any)
	if len(codes) != 10 {
		t.Fatalf("recovery codes: %v", conf.body)
	}

	// Sign-in now takes two steps.
	c := e.client()
	r := c.expect(c.login("admin", pw), 200, "")
	if r.body["mfaRequired"] != true || r.body["user"] != nil {
		t.Fatalf("expected an MFA challenge without a session: %v", r.body)
	}
	token := r.body["mfaToken"].(string)
	c.expect(c.do("GET", "/api/v1/auth/me", nil), 401, "auth.unauthenticated")
	c.expect(c.do("POST", "/api/v1/auth/login/mfa", map[string]any{"mfaToken": token, "code": "123456"}), 400, "auth.mfa_invalid_code")
	c.expect(c.do("POST", "/api/v1/auth/login/mfa", map[string]any{"mfaToken": "bogus", "code": "123456"}), 400, "auth.mfa_challenge_expired")
	ok := c.expect(c.do("POST", "/api/v1/auth/login/mfa", map[string]any{"mfaToken": token, "code": totp(t, secret, 0)}), 200, "")
	if ok.obj("user")["username"] != "admin" {
		t.Fatalf("mfa login: %v", ok.body)
	}
	// The challenge is single-use.
	c.expect(c.do("POST", "/api/v1/auth/login/mfa", map[string]any{"mfaToken": token, "code": totp(t, secret, 30*time.Second)}), 400, "auth.mfa_challenge_expired")

	// A recovery code works once.
	d := e.client()
	token = d.expect(d.login("admin", pw), 200, "").body["mfaToken"].(string)
	d.expect(d.do("POST", "/api/v1/auth/login/mfa", map[string]any{"mfaToken": token, "recoveryCode": codes[0]}), 200, "")
	f := e.client()
	token = f.expect(f.login("admin", pw), 200, "").body["mfaToken"].(string)
	f.expect(f.do("POST", "/api/v1/auth/login/mfa", map[string]any{"mfaToken": token, "recoveryCode": codes[0]}), 400, "auth.mfa_invalid_code")

	// With TOTP on, step-up needs a code as well.
	c.expect(c.do("DELETE", "/api/v1/me/totp", nil), 403, "auth.step_up_required")
	c.expect(c.do("POST", "/api/v1/auth/elevate", map[string]any{"password": pw}), 400, "auth.mfa_required")
	c.elevate(totp(t, secret, 30*time.Second))
	regen := c.expect(c.do("POST", "/api/v1/me/recovery-codes", nil), 200, "")
	if n, _ := regen.body["recoveryCodes"].([]any); len(n) != 10 {
		t.Fatalf("regenerate: %v", regen.body)
	}
	c.expect(c.do("DELETE", "/api/v1/me/totp", nil), 204, "")
	if r := c.do("GET", "/api/v1/auth/me", nil); r.obj("user")["totpEnabled"] != false {
		t.Fatalf("totp still enabled: %v", r.body)
	}
}

func TestUserManagementAndRoles(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")

	newUser := map[string]any{"username": "ops", "password": pw, "role": "operator"}
	admin.expect(admin.do("POST", "/api/v1/users", newUser), 403, "auth.step_up_required")
	admin.elevate("")
	created := admin.expect(admin.do("POST", "/api/v1/users", newUser), 201, "")
	opsID := int64(created.obj("user")["id"].(float64))
	admin.expect(admin.do("POST", "/api/v1/users", newUser), 409, "users.username_taken")
	r := admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "eve", "password": pw, "role": "root"}), 400, "validation.invalid")
	if r.params()["field"] != "role" || r.params()["rule"] != "enum" {
		t.Fatalf("role validation: %v", r.params())
	}
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "aud", "password": pw, "role": "auditor"}), 201, "")

	// Operator: overview and settings yes; users, audit and changing settings no.
	ops := e.client()
	ops.expect(ops.login("ops", pw), 200, "")
	ops.expect(ops.do("GET", "/api/v1/system/overview", nil), 200, "")
	if host := ops.expect(ops.do("GET", "/api/v1/system/host", nil), 200, "").body; host["addresses"] == nil || host["hostname"] == nil {
		t.Fatalf("host %v", host)
	}
	ops.expect(ops.do("GET", "/api/v1/settings", nil), 200, "")
	ops.expect(ops.do("GET", "/api/v1/users", nil), 403, "auth.forbidden")
	ops.expect(ops.do("GET", "/api/v1/audit", nil), 403, "auth.forbidden")
	ops.expect(ops.do("PUT", "/api/v1/settings/default-locale", map[string]any{"locale": "en"}), 403, "auth.forbidden")

	// Auditor reads the audit log but cannot manage users.
	aud := e.client()
	aud.expect(aud.login("aud", pw), 200, "")
	aud.expect(aud.do("GET", "/api/v1/audit", nil), 200, "")
	aud.expect(aud.do("DELETE", fmt.Sprintf("/api/v1/users/%d", opsID), nil), 403, "auth.forbidden")

	// Safeguards on oneself.
	me := admin.do("GET", "/api/v1/auth/me", nil).obj("user")
	selfPath := fmt.Sprintf("/api/v1/users/%d", int64(me["id"].(float64)))
	admin.expect(admin.do("PATCH", selfPath, map[string]any{"role": "operator"}), 409, "users.cannot_modify_self")
	admin.expect(admin.do("PATCH", selfPath, map[string]any{"disabled": true}), 409, "users.cannot_modify_self")
	admin.expect(admin.do("DELETE", selfPath, nil), 409, "users.cannot_modify_self")
	admin.expect(admin.do("PATCH", "/api/v1/users/9999", map[string]any{"disabled": true}), 404, "request.not_found")

	// Promote, disable (signs out), reset password, delete.
	opsPath := fmt.Sprintf("/api/v1/users/%d", opsID)
	up := admin.expect(admin.do("PATCH", opsPath, map[string]any{"role": "admin"}), 200, "")
	if up.obj("user")["role"] != "admin" {
		t.Fatalf("promote: %v", up.body)
	}
	admin.expect(admin.do("PATCH", opsPath, map[string]any{"disabled": true}), 200, "")
	ops.expect(ops.do("GET", "/api/v1/auth/me", nil), 401, "auth.session_expired")
	ops.expect(ops.login("ops", pw), 401, "auth.invalid_credentials")
	admin.expect(admin.do("PATCH", opsPath, map[string]any{"disabled": false}), 200, "")
	admin.expect(admin.do("PUT", opsPath+"/password", map[string]any{"password": "short"}), 400, "validation.invalid")
	admin.expect(admin.do("PUT", opsPath+"/password", map[string]any{"password": "brand new password"}), 204, "")
	ops.expect(ops.login("ops", "brand new password"), 200, "")
	admin.expect(admin.do("DELETE", opsPath+"/totp", nil), 204, "")
	admin.expect(admin.do("DELETE", opsPath, nil), 204, "")
	ops.expect(ops.do("GET", "/api/v1/auth/me", nil), 401, "auth.session_expired")

	list := admin.expect(admin.do("GET", "/api/v1/users", nil), 200, "")
	if n := len(list.body["users"].([]any)); n != 2 {
		t.Fatalf("want admin and auditor left, got %d", n)
	}
}

func TestAuditListing(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	admin.elevate("")
	for i := 0; i < 3; i++ {
		admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": fmt.Sprintf("user%d", i), "password": pw, "role": "operator"}), 201, "")
	}
	r := admin.expect(admin.do("GET", "/api/v1/audit?action=users.&limit=2", nil), 200, "")
	entries := r.body["entries"].([]any)
	first := entries[0].(map[string]any)
	if len(entries) != 2 || first["action"] != "users.create" || r.body["nextBefore"] == nil {
		t.Fatalf("page 1: %v", r.body)
	}
	if p := first["params"].(map[string]any); p["username"] != "user2" || p["role"] != "operator" {
		t.Fatalf("params: %v", p)
	}
	next := int64(r.body["nextBefore"].(float64))
	r = admin.expect(admin.do("GET", fmt.Sprintf("/api/v1/audit?action=users.&limit=2&before=%d", next), nil), 200, "")
	if len(r.body["entries"].([]any)) != 1 || r.body["nextBefore"] != nil {
		t.Fatalf("page 2: %v", r.body)
	}
	// LIKE wildcards in the filter are literal.
	r = admin.expect(admin.do("GET", "/api/v1/audit?action=%25", nil), 200, "")
	if len(r.body["entries"].([]any)) != 0 {
		t.Fatalf("wildcard not escaped: %v", r.body)
	}
	r = admin.expect(admin.do("GET", "/api/v1/audit?user=admin&action=auth.elevate", nil), 200, "")
	if len(r.body["entries"].([]any)) != 1 {
		t.Fatalf("user filter: %v", r.body)
	}
	// Result and time range are filtered on the server, over all entries.
	if _, err := e.db.Exec(`INSERT INTO audit_log(ts, user_id, username, action, params, result, remote_addr, source)
		VALUES(?, 1, 'admin', 'users.create', '{}', 'users.username_taken', '127.0.0.1', 'web')`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	r = admin.expect(admin.do("GET", "/api/v1/audit?action=users.create&result=failed", nil), 200, "")
	if es := r.body["entries"].([]any); len(es) != 1 || es[0].(map[string]any)["result"] != "users.username_taken" {
		t.Fatalf("failed filter: %v", r.body)
	}
	r = admin.expect(admin.do("GET", "/api/v1/audit?action=users.create&result=ok", nil), 200, "")
	if len(r.body["entries"].([]any)) != 3 {
		t.Fatalf("ok filter: %v", r.body)
	}
	now := time.Now().Unix()
	if r = admin.expect(admin.do("GET", fmt.Sprintf("/api/v1/audit?action=users.&since=%d", now+60), nil), 200, ""); len(r.body["entries"].([]any)) != 0 {
		t.Fatalf("since: %v", r.body)
	}
	if r = admin.expect(admin.do("GET", fmt.Sprintf("/api/v1/audit?action=users.&since=%d&until=%d", now-60, now+60), nil), 200, ""); len(r.body["entries"].([]any)) != 4 {
		t.Fatalf("range: %v", r.body)
	}
	admin.expect(admin.do("GET", "/api/v1/audit?result=maybe", nil), 400, "validation.invalid")
	// One entry by id.
	id := int64(first["id"].(float64))
	if one := admin.expect(admin.do("GET", fmt.Sprintf("/api/v1/audit/%d", id), nil), 200, ""); one.body["action"] != "users.create" {
		t.Fatalf("one: %v", one.body)
	}
	admin.expect(admin.do("GET", "/api/v1/audit/999999", nil), 404, "request.not_found")
	// Secrets never end up in the audit log.
	var leaked int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE params LIKE ?`, "%"+pw+"%").Scan(&leaked)
	if leaked != 0 {
		t.Fatal("password found in audit params")
	}
}

func TestEveryRouteDeclaresAPermission(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registering a route without a permission must panic")
		}
	}()
	api.NewRouter(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).
		Handle("GET /api/v1/unguarded", func(http.ResponseWriter, *http.Request) error { return nil })
}
