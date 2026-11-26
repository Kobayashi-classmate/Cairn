// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// installRuntime makes a runtime line look installed in r.
func installRuntime(t *testing.T, r *Runtimes, sw, line, version string) {
	t.Helper()
	dir := filepath.Join(r.Dir, sw)
	if err := os.MkdirAll(filepath.Join(dir, version, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, version+".json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(version, filepath.Join(dir, line)); err != nil {
		t.Fatal(err)
	}
}

func appSpec(code string, port int) helperapi.SiteSpec {
	return helperapi.SiteSpec{Code: code, Type: helperapi.SiteApp, Domains: []string{code + ".example.com"}, Enabled: true,
		Writable: []string{"data"},
		App: &helperapi.AppSpec{Runtime: "nodejs", Line: "22", Port: port, WebSocket: true, MemoryMB: 256, CPUPercent: 50,
			Command: `node server.js --name "my app" --port $PORT --load 100% \path`,
			Env:     []helperapi.EnvVar{{Name: "DATABASE_URL", Value: `postgres://u:p"w@db/x`}, {Name: "SECRET", Value: "a$b`c\\d"}}}}
}

func TestAppSiteUnit(t *testing.T) {
	e := newSiteEnv(t)
	e.f.systemd = true
	e.st.Software.Runtimes = &Runtimes{Dir: filepath.Join(e.st.Root, "opt/cairn/runtimes")}
	installRuntime(t, e.st.Software.Runtimes, "nodejs", "22", "22.21.1")
	e.st.PanelPort = 28443

	spec := appSpec("api", 20001)
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	op, log := e.run(helperapi.ActionSiteApply, spec)
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}
	unit := e.read("/etc/systemd/system/cairn-app-api.service")
	for _, want := range []string{
		"User=cw-api-run", "Group=cw-api", "WorkingDirectory=/srv/www/api/app",
		`ExecStart=/bin/sh -c "node server.js --name \"my app\" --port $$PORT --load 100%% \\path"`,
		`"PORT=20001"`, `"PATH=/opt/cairn/runtimes/nodejs/22/bin:/usr/local/sbin`, "EnvironmentFile=-/etc/cairn/apps/api.env",
		"MemoryMax=256M", "CPUQuota=50%", "ProtectSystem=strict", "ReadWritePaths=/srv/www/api/tmp /srv/www/api/app/data", "NoNewPrivileges=yes",
		// It listens on its own port only.
		"SocketBindAllow=tcp:20001\nSocketBindDeny=any\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	// Values stay out of the world-readable unit.
	if strings.Contains(unit, "DATABASE_URL") {
		t.Error("app variables in the unit")
	}
	env := e.read("/etc/cairn/apps/api.env")
	if !strings.Contains(env, `DATABASE_URL="postgres://u:p\"w@db/x"`) || !strings.Contains(env, "SECRET=\"a\\$b\\`c\\\\d\"") {
		t.Errorf("env file:\n%s", env)
	}
	if info, _ := os.Stat(filepath.Join(e.st.Root, "etc/cairn/apps/api.env")); info.Mode().Perm() != 0o600 {
		t.Errorf("env file mode %v", info.Mode())
	}
	conf := e.read("/etc/nginx/cairn/sites/api.conf")
	if !strings.Contains(conf, "proxy_pass http://127.0.0.1:20001;") || !strings.Contains(conf, "proxy_set_header Upgrade") {
		t.Errorf("nginx conf:\n%s", conf)
	}
	for _, c := range []string{"systemd-analyze verify /etc/systemd/system/cairn-app-api.service", "systemctl daemon-reload",
		"systemctl enable cairn-app-api.service", "systemctl restart cairn-app-api.service"} {
		if !e.f.ran(c) {
			t.Errorf("did not run %q: %v", c, e.f.cmds)
		}
	}

	// Ports are unique across apps, streams and the panel.
	for name, port := range map[string]int{"another app": 20001, "the panel": 28443} {
		if op, _ := e.run(helperapi.ActionSiteApply, appSpec("web", port)); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeAppPortTaken {
			t.Errorf("%s: %+v", name, op.Error)
		}
	}
	// A runtime line that is not installed.
	other := appSpec("web", 20002)
	other.App.Line = "24"
	if op, _ := e.run(helperapi.ActionSiteApply, other); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeAppRuntimeMissing {
		t.Errorf("runtime: %+v", op.Error)
	}

	// Status and log come from systemd.
	e.f.systemctlShow = "ActiveState=active\nSubState=running\nMainPID=4242\nNRestarts=2\nMemoryCurrent=52428800\nCPUUsageNSec=[not set]\nResult=success\nLoadState=loaded\n"
	st, err := e.st.AppStatus(t.Context(), "api")
	if err != nil || st.ActiveState != "active" || st.MainPID != 4242 || st.Restarts != 2 || st.MemoryBytes != 52428800 || st.CPUNanos != 0 {
		t.Fatalf("status: %+v %v", st, err)
	}
	e.f.journal = "2026-11-14T10:00:00+0000 host cairn-app-api[42]: listening on 20001\n-- cursor: s=abc;i=1\n"
	lg, err := e.st.AppLog(t.Context(), helperapi.AppLogRequest{Code: "api"})
	if err != nil || len(lg.Lines) != 1 || lg.Cursor != "s=abc;i=1" {
		t.Fatalf("log: %+v %v", lg, err)
	}
	// Lines with colours come as they are, not as "[N blob data]".
	if !e.f.ran("query journalctl --unit cairn-app-api.service --no-pager --all --output short-iso --show-cursor --lines 300") {
		t.Fatalf("journalctl arguments: %v", e.f.cmds)
	}

	// No longer an app: the unit stops and its files go.
	spec.Type, spec.App, spec.Writable = helperapi.SiteStatic, nil, nil
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("to static: %+v\n%s", op.Error, log)
	}
	if !e.f.ran("systemctl disable --now cairn-app-api.service") || e.exists("/etc/systemd/system/cairn-app-api.service") || e.exists("/etc/cairn/apps/api.env") {
		t.Fatal("app left behind")
	}

	// Apps need systemd.
	e.f.systemd = false
	if op, _ := e.run(helperapi.ActionSiteApply, appSpec("web", 20003)); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeAppNoSystemd {
		t.Fatalf("without systemd: %+v", op.Error)
	}
}

func TestAppSpecValidation(t *testing.T) {
	for name, mutate := range map[string]func(a *helperapi.AppSpec){
		"newline in command":    func(a *helperapi.AppSpec) { a.Command = "node a.js\nUser=root" },
		"empty command":         func(a *helperapi.AppSpec) { a.Command = " " },
		"privileged port":       func(a *helperapi.AppSpec) { a.Port = 80 },
		"runtime without line":  func(a *helperapi.AppSpec) { a.Line = "" },
		"unknown runtime":       func(a *helperapi.AppSpec) { a.Runtime = "ruby" },
		"bad env name":          func(a *helperapi.AppSpec) { a.Env = []helperapi.EnvVar{{Name: "A-B", Value: "x"}} },
		"newline in env value":  func(a *helperapi.AppSpec) { a.Env = []helperapi.EnvVar{{Name: "A", Value: "x\ny"}} },
		"work dir escapes":      func(a *helperapi.AppSpec) { a.WorkDir = "../etc" },
		"tiny memory":           func(a *helperapi.AppSpec) { a.MemoryMB = 4 },
		"health path injection": func(a *helperapi.AppSpec) { a.HealthPath = "/h;evil" },
	} {
		s := appSpec("api", 20001)
		mutate(s.App)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStreamApplyAndRemove(t *testing.T) {
	e := newSiteEnv(t)
	tcp := helperapi.StreamSpec{Code: "mysql", Protocol: helperapi.StreamTCP, Port: 3307, Upstreams: []string{"10.0.0.5:3306", "[fd00::5]:3306"}, Balance: "hash", Enabled: true}
	if op, _ := e.run(helperapi.ActionStreamApply, tcp); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeStreamModuleMissing {
		t.Fatalf("no module: %+v", op.Error)
	}
	writeSystemFile(t, e.st.Root, "/etc/nginx/modules-enabled/50-mod-stream.conf", "load_module modules/ngx_stream_module.so;\n")
	if op, log := e.run(helperapi.ActionStreamApply, tcp); op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}
	conf := e.read("/etc/nginx/cairn/stream/mysql.conf")
	for _, want := range []string{"upstream cairn_stream_mysql {", "hash $remote_addr consistent;", "server [fd00::5]:3306;", "listen 3307;", "listen [::]:3307;", "proxy_pass cairn_stream_mysql;"} {
		if !strings.Contains(conf, want) {
			t.Errorf("stream conf lacks %q:\n%s", want, conf)
		}
	}
	if !strings.Contains(e.read("/etc/nginx/modules-enabled/99-cairn-stream.conf"), "include /etc/nginx/cairn/stream/*.conf;") {
		t.Error("stream block missing")
	}
	// The same port over UDP is another socket; over TCP it is taken.
	dns := helperapi.StreamSpec{Code: "dns", Protocol: helperapi.StreamUDP, Port: 3307, Upstreams: []string{"10.0.0.53:53"}, Enabled: true}
	if op, _ := e.run(helperapi.ActionStreamApply, dns); op.State != helperapi.OpSucceeded || !strings.Contains(e.read("/etc/nginx/cairn/stream/dns.conf"), "listen 3307 udp;") {
		t.Fatalf("udp: %+v", op.Error)
	}
	dup := tcp
	dup.Code = "other"
	if op, _ := e.run(helperapi.ActionStreamApply, dup); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeStreamPortTaken {
		t.Fatalf("taken: %+v", op.Error)
	}
	// Stopped: no listener; removed: gone.
	tcp.Enabled = false
	e.run(helperapi.ActionStreamApply, tcp)
	if e.exists("/etc/nginx/cairn/stream/mysql.conf") {
		t.Fatal("stopped stream still listens")
	}
	if op, _ := e.run(helperapi.ActionStreamRemove, helperapi.StreamRef{Code: "dns"}); op.State != helperapi.OpSucceeded || e.exists("/etc/nginx/cairn/stream/dns.conf") {
		t.Fatalf("remove: %+v", op.Error)
	}
	if st := e.st.Status(t.Context()); len(st.Streams) != 1 || st.Streams["mysql"].Enabled {
		t.Fatalf("status: %+v", st.Streams)
	}
	for name, s := range map[string]helperapi.StreamSpec{
		"port 443":       {Code: "a1", Protocol: "tcp", Port: 443, Upstreams: []string{"a.example.com:443"}},
		"app port range": {Code: "a2", Protocol: "tcp", Port: 20005, Upstreams: []string{"a.example.com:443"}},
		"bad upstream":   {Code: "a3", Protocol: "tcp", Port: 9000, Upstreams: []string{"a.example.com"}},
		"injection":      {Code: "a4", Protocol: "tcp", Port: 9000, Upstreams: []string{"a.example.com:1;x"}},
		"sctp":           {Code: "a5", Protocol: "sctp", Port: 9000, Upstreams: []string{"a.example.com:1"}},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The helper checks a stream's upstreams itself, against this host's
// addresses as it sees them, unless the web process says whoever set them
// may forward anywhere; a stopped stream forwards nothing.
func TestStreamUpstreamsCheckedByHelper(t *testing.T) {
	e := newSiteEnv(t)
	writeSystemFile(t, e.st.Root, "/etc/nginx/modules-enabled/50-mod-stream.conf", "load_module modules/ngx_stream_module.so;\n")
	old := hostAddrs
	hostAddrs = func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("192.168.1.10")}, nil }
	t.Cleanup(func() { hostAddrs = old })
	port := 4400
	stream := func(up string) helperapi.StreamSpec {
		port++
		return helperapi.StreamSpec{Code: fmt.Sprintf("s%d", port), Protocol: helperapi.StreamTCP, Port: port, Upstreams: []string{"10.0.0.5:3306", up}, Enabled: true}
	}
	for up, code := range map[string]string{
		"127.0.0.1:3306": helperapi.CodeStreamLocalUpstream, "192.168.1.10:3306": helperapi.CodeStreamLocalUpstream,
		"[::ffff:192.168.1.10]:3306": helperapi.CodeStreamLocalUpstream, "169.254.169.254:80": helperapi.CodeStreamMetadataUpstream,
		"db.example.com:3306": helperapi.CodeStreamUpstreamName,
	} {
		s := stream(up)
		if op, _ := e.run(helperapi.ActionStreamApply, s); op.State != helperapi.OpFailed || op.Error.Code != code || op.Error.Params["upstream"] != up {
			t.Errorf("%s: %+v", up, op.Error)
		}
		if e.exists(streamConfPath(s.Code)) {
			t.Errorf("%s: the stream was written", up)
		}
		// Stopped, it may keep the upstream.
		s.Enabled = false
		if op, log := e.run(helperapi.ActionStreamApply, s); op.State != helperapi.OpSucceeded {
			t.Errorf("%s stopped: %+v\n%s", up, op.Error, log)
		}
		s.Enabled, s.AnyUpstream = true, true
		if op, log := e.run(helperapi.ActionStreamApply, s); op.State != helperapi.OpSucceeded || !strings.Contains(e.read(streamConfPath(s.Code)), "server "+up+";") {
			t.Errorf("%s with AnyUpstream: %+v\n%s", up, op.Error, log)
		}
	}
	// Other hosts of the network.
	if op, log := e.run(helperapi.ActionStreamApply, stream("192.168.1.11:3306")); op.State != helperapi.OpSucceeded {
		t.Errorf("another host: %+v\n%s", op.Error, log)
	}
	// Without this host's addresses nothing is let through.
	hostAddrs = func() ([]netip.Addr, error) { return nil, errors.New("netlink: permission denied") }
	if op, _ := e.run(helperapi.ActionStreamApply, stream("192.168.1.12:3306")); op.State != helperapi.OpFailed {
		t.Errorf("without the host's addresses: %+v", op)
	}
}

// The PHP of every site runs in its line's PHP-FPM service: each installed
// line's service gets a drop-in that keeps it off the apps' ports and the
// panel's, following the apps as they come and go.
func TestPHPKeptOffAppPorts(t *testing.T) {
	e := newSiteEnv(t)
	e.f.systemd, e.f.isActive = true, "active"
	e.st.Software.Runtimes = &Runtimes{Dir: filepath.Join(e.st.Root, "opt/cairn/runtimes")}
	installRuntime(t, e.st.Software.Runtimes, "nodejs", "22", "22.21.1")
	installPHP82(t, e)
	e.st.PanelPort = 8443
	const d84, d82 = "/etc/systemd/system/php8.4-fpm.service.d/cairn-app-ports.conf", "/etc/systemd/system/php8.2-fpm.service.d/cairn-app-ports.conf"
	reloads := func() int {
		e.f.mu.Lock()
		defer e.f.mu.Unlock()
		n := 0
		for _, c := range e.f.cmds {
			if c == "systemctl daemon-reload" {
				n++
			}
		}
		return n
	}
	rules := func(p string) string {
		var out []string
		for _, l := range strings.Split(e.read(p), "\n") {
			if strings.HasPrefix(l, "SocketBind") {
				out = append(out, l)
			}
		}
		return strings.Join(out, " ")
	}
	apply := func(s helperapi.SiteSpec) {
		t.Helper()
		if op, log := e.run(helperapi.ActionSiteApply, s); op.State != helperapi.OpSucceeded {
			t.Fatalf("apply %s: %+v\n%s", s.Code, op.Error, log)
		}
	}
	apply(helperapi.SiteSpec{Code: "blog", Type: helperapi.SitePHP, Domains: []string{"blog.example.com"}, PHP: "8.4", Enabled: true})
	for _, p := range []string{d84, d82} {
		if got := rules(p); got != "SocketBindDeny=tcp:20000-29999 SocketBindDeny=tcp:8443" {
			t.Errorf("%s: %s", p, got)
		}
	}
	if reloads() != 1 {
		t.Errorf("systemd reloaded %d times for the new drop-ins", reloads())
	}
	// An app outside the range adds its port; one inside it is covered.
	apply(appSpec("api", 3000))
	apply(appSpec("web", 20005))
	if got := rules(d84); got != "SocketBindDeny=tcp:20000-29999 SocketBindDeny=tcp:3000 SocketBindDeny=tcp:8443" || rules(d82) != got {
		t.Errorf("with the apps: %s", got)
	}
	n := reloads()
	apply(helperapi.SiteSpec{Code: "blog", Type: helperapi.SitePHP, Domains: []string{"blog.example.com", "www.blog.example.com"}, PHP: "8.4", Enabled: true})
	if reloads() != n {
		t.Error("systemd reloaded with the drop-ins unchanged")
	}
	if op, _ := e.run(helperapi.ActionSiteRemove, helperapi.SiteRemoveRequest{Code: "api"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("remove: %+v", op.Error)
	}
	if got := rules(d84); got != "SocketBindDeny=tcp:20000-29999 SocketBindDeny=tcp:8443" {
		t.Errorf("after the app went: %s", got)
	}
	// A line removed outside the panel loses its drop-in; one left out by
	// an earlier version comes back with the refresh at the helper's start.
	if err := os.Remove(filepath.Join(e.st.Root, "/usr/sbin/php-fpm8.2")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(e.st.Root, d84)); err != nil {
		t.Fatal(err)
	}
	e.st.Refresh(t.Context(), slog.New(slog.DiscardHandler))
	if e.exists(d82) || !e.exists(d84) {
		t.Errorf("after the refresh: 8.2 drop-in %v, 8.4 drop-in %v", e.exists(d82), e.exists(d84))
	}
}

// An app's unit written by an earlier version is rewritten when the helper
// starts, and systemd reloaded; the running app is not restarted for it.
func TestRefreshRewritesAppUnits(t *testing.T) {
	e := newSiteEnv(t)
	e.f.systemd, e.f.isActive = true, "active"
	e.st.Software.Runtimes = &Runtimes{Dir: filepath.Join(e.st.Root, "opt/cairn/runtimes")}
	installRuntime(t, e.st.Software.Runtimes, "nodejs", "22", "22.21.1")
	for _, s := range []helperapi.SiteSpec{appSpec("api", 20001), appSpec("web", 20002)} {
		if op, log := e.run(helperapi.ActionSiteApply, s); op.State != helperapi.OpSucceeded {
			t.Fatalf("apply %s: %+v\n%s", s.Code, op.Error, log)
		}
	}
	unit := e.read(appUnitPath("api"))
	writeSystemFile(t, e.st.Root, appUnitPath("api"), strings.Replace(unit, "SocketBindAllow=tcp:20001\nSocketBindDeny=any\n", "", 1))
	e.f.mu.Lock()
	e.f.cmds = nil
	e.f.mu.Unlock()
	e.st.Refresh(t.Context(), slog.New(slog.DiscardHandler))
	if got := e.read(appUnitPath("api")); got != unit {
		t.Errorf("the unit was not refreshed:\n%s", got)
	}
	for _, c := range []string{"systemd-analyze verify /etc/systemd/system/cairn-app-api.service", "systemd-analyze verify /etc/systemd/system/cairn-app-web.service",
		"systemctl daemon-reload"} {
		if !e.f.ran(c) {
			t.Errorf("did not run %q: %v", c, e.f.cmds)
		}
	}
	if e.f.ran("systemctl restart") {
		t.Errorf("an app was restarted: %v", e.f.cmds)
	}
}

func TestAppSiteFailedChanges(t *testing.T) {
	e := newSiteEnv(t)
	e.f.systemd = true
	e.st.Software.Runtimes = &Runtimes{Dir: filepath.Join(e.st.Root, "opt/cairn/runtimes")}
	installRuntime(t, e.st.Software.Runtimes, "nodejs", "22", "22.21.1")
	spec := appSpec("api", 20001)
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}
	ran := func() int {
		e.f.mu.Lock()
		defer e.f.mu.Unlock()
		return len(e.f.cmds)
	}
	since := func(n int, cmd string) bool {
		e.f.mu.Lock()
		defer e.f.mu.Unlock()
		for _, c := range e.f.cmds[n:] {
			if strings.HasPrefix(c, cmd) {
				return true
			}
		}
		return false
	}

	// Turning it into a static site fails the check: the app's files are
	// back, and so is the app.
	e.f.failPrefix = "/usr/sbin/nginx -t"
	static := spec
	static.Type, static.App, static.Writable = helperapi.SiteStatic, nil, nil
	n := ran()
	if op, _ := e.run(helperapi.ActionSiteApply, static); op.State != helperapi.OpFailed {
		t.Fatalf("failing check: %+v", op.Error)
	}
	if !e.exists("/etc/systemd/system/cairn-app-api.service") || !since(n, "systemctl disable --now cairn-app-api.service") ||
		!since(n, "systemctl enable cairn-app-api.service") || !since(n, "systemctl restart cairn-app-api.service") {
		t.Fatalf("the app was not started again: %v", e.f.cmds[n:])
	}
	// The same for a removal.
	n = ran()
	if op, _ := e.run(helperapi.ActionSiteRemove, helperapi.SiteRemoveRequest{Code: "api"}); op.State != helperapi.OpFailed {
		t.Fatalf("failing remove: %+v", op.Error)
	}
	if !e.exists("/etc/systemd/system/cairn-app-api.service") || !since(n, "systemctl restart cairn-app-api.service") {
		t.Fatalf("the app was not started again after a failed removal: %v", e.f.cmds[n:])
	}

	// The files went in but the unit could not be enabled: the state says
	// what the files are.
	e.f.failPrefix = "systemctl enable"
	spec.App.Port = 20002
	if op, _ := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpFailed {
		t.Fatalf("failing enable: %+v", op.Error)
	}
	var state helperapi.SiteState
	if err := json.Unmarshal([]byte(e.read("/var/lib/cairn-helper/sites/api.json")), &state); err != nil || state.Spec.App.Port != 20002 {
		t.Fatalf("state after a failed start: %+v %v", state.Spec.App, err)
	}
}

func TestStreamPortChecks(t *testing.T) {
	e := newSiteEnv(t)
	writeSystemFile(t, e.st.Root, "/etc/nginx/modules-enabled/50-mod-stream.conf", "load_module modules/ngx_stream_module.so;\n")
	// sshd on 2222 (0x08AE) over TCP; something on UDP 5353 (0x14E9).
	writeSystemFile(t, e.st.Root, "/proc/net/tcp", "  sl  local_address rem_address   st\n   0: 00000000:08AE 00000000:0000 0A\n")
	writeSystemFile(t, e.st.Root, "/proc/net/udp6", "  sl  local_address rem_address   st\n   0: 00000000000000000000000000000000:14E9 00000000000000000000000000000000:0000 07\n")
	// An app site on 3000.
	app, _ := json.Marshal(helperapi.SiteState{Spec: helperapi.SiteSpec{Code: "api", Type: helperapi.SiteApp, Domains: []string{"api.example.com"},
		App: &helperapi.AppSpec{Port: 3000, Command: "node a.js"}, Enabled: true}})
	writeSystemFile(t, e.st.Root, siteStateDir+"/api.json", string(app))

	for name, tc := range map[string]struct {
		spec  helperapi.StreamSpec
		param string
	}{
		"sshd's port":   {helperapi.StreamSpec{Code: "s1", Protocol: helperapi.StreamTCP, Port: 2222, Upstreams: []string{"10.0.0.5:22"}, Enabled: true}, "listening"},
		"a udp socket":  {helperapi.StreamSpec{Code: "s2", Protocol: helperapi.StreamUDP, Port: 5353, Upstreams: []string{"10.0.0.5:53"}, Enabled: true}, "listening"},
		"an app's port": {helperapi.StreamSpec{Code: "s3", Protocol: helperapi.StreamTCP, Port: 3000, Upstreams: []string{"10.0.0.5:80"}, Enabled: true}, "site"},
	} {
		op, _ := e.run(helperapi.ActionStreamApply, tc.spec)
		if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeStreamPortTaken || op.Error.Params[tc.param] == nil {
			t.Errorf("%s: %+v", name, op.Error)
		}
	}
	// Over the other protocol, or stopped, the port is free.
	for _, s := range []helperapi.StreamSpec{
		{Code: "s4", Protocol: helperapi.StreamUDP, Port: 2222, Upstreams: []string{"10.0.0.5:22"}, Enabled: true},
		{Code: "s5", Protocol: helperapi.StreamTCP, Port: 2222, Upstreams: []string{"10.0.0.5:22"}},
	} {
		if op, log := e.run(helperapi.ActionStreamApply, s); op.State != helperapi.OpSucceeded {
			t.Fatalf("%s: %+v\n%s", s.Code, op.Error, log)
		}
	}
	// A stream applied on a port: nginx listens there, and the stream
	// can be applied again.
	own := helperapi.StreamSpec{Code: "s6", Protocol: helperapi.StreamTCP, Port: 9100, Upstreams: []string{"10.0.0.5:80"}, Enabled: true}
	if op, log := e.run(helperapi.ActionStreamApply, own); op.State != helperapi.OpSucceeded {
		t.Fatalf("own: %+v\n%s", op.Error, log)
	}
	writeSystemFile(t, e.st.Root, "/proc/net/tcp", "  sl  local_address rem_address   st\n   0: 00000000:238C 00000000:0000 0A\n")
	own.Upstreams = []string{"10.0.0.6:80"}
	if op, log := e.run(helperapi.ActionStreamApply, own); op.State != helperapi.OpSucceeded {
		t.Fatalf("own again: %+v\n%s", op.Error, log)
	}
}

func TestStreamFailureRemovesSELinuxLabel(t *testing.T) {
	e := newSiteEnv(t)
	writeSystemFile(t, e.st.Root, "/etc/nginx/modules-enabled/50-mod-stream.conf", "load_module modules/ngx_stream_module.so;\n")
	writeSystemFile(t, e.st.Root, seSemanage, "")
	se := &fakeSemanage{ports: map[string]bool{}, fc: map[string]string{}, policy: "unreserved_port_t tcp 1024-32767\n"}
	se.use(t)
	inner := runCommand
	runCommand = func(ctx context.Context, log io.Writer, name string, args ...string) error {
		if strings.HasSuffix(name, "nginx") && len(args) == 1 && args[0] == "-t" {
			return &helperapi.Error{Code: "exec.failed"}
		}
		return inner(ctx, log, name, args...)
	}
	s := helperapi.StreamSpec{Code: "s1", Protocol: helperapi.StreamTCP, Port: 9100, Upstreams: []string{"10.0.0.5:80"}, Enabled: true}
	if op, _ := e.run(helperapi.ActionStreamApply, s); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeSiteConfigInvalid {
		t.Fatalf("failing check: %+v", op.Error)
	}
	if len(se.imports) == 0 || len(se.ports) != 0 {
		t.Fatalf("labels after a failed apply: %v (imports %v)", se.ports, se.imports)
	}
}
