// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
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
