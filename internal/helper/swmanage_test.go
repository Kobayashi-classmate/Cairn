// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// fakeCheck replaces the configuration check with fn, recording argv.
func fakeCheck(t *testing.T, fn func(argv []string) (string, error)) *[][]string {
	t.Helper()
	old, oldPause := checkCommand, pause
	t.Cleanup(func() { checkCommand, pause = old, oldPause })
	var seen [][]string
	checkCommand = func(_ context.Context, argv []string) (string, error) {
		seen = append(seen, argv)
		return fn(argv)
	}
	pause = func(time.Duration) {}
	return &seen
}

func lineSoftware(t *testing.T, f *fakeSystem) (*Software, *Ops, *Registry) {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	f.use(t)
	s, o, r := softwareFor(t, cat, "debian-13", trixie, "apt")
	s.ConfigVersionsDir = filepath.Join(t.TempDir(), "versions")
	return s, o, r
}

func runLineOp(t *testing.T, o *Ops, r *Registry, action string, req any) (helperapi.Op, string) {
	t.Helper()
	ref, err := call[helperapi.OpRef](t, r, action, req)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	op := waitOp(t, o, ref.ID, helperapi.OpSucceeded, helperapi.OpFailed)
	res, _ := o.Get(ref.ID, 0)
	return op, res.Log
}

func writeTestFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil { //nolint:gosec // G306: not the temporary file's 0600, to see the mode kept
		t.Fatal(err)
	}
}

func TestConfigFileWriteCheckAndRestore(t *testing.T) {
	f := &fakeSystem{systemd: true, isActive: "active", installed: map[string]string{"nginx": "1.26.3-3", "libnginx-mod-stream": "1.26.3-3"}}
	s, o, r := lineSoftware(t, f)
	conf := filepath.Join(s.Root, "etc/nginx/nginx.conf")
	writeTestFile(t, conf, "worker_processes 1;\n")
	checkOK := true
	checks := fakeCheck(t, func([]string) (string, error) {
		if checkOK {
			return "nginx: the configuration file /etc/nginx/nginx.conf syntax is ok\n", nil
		}
		return "nginx: [emerg] unknown directive \"bogus\"\n", errors.New("exit status 1")
	})
	ref := helperapi.ConfigRequest{Software: "nginx", Line: "1.26", Config: "nginx.conf"}

	cur, err := call[helperapi.ConfigFile](t, r, helperapi.ActionPkgConfigRead, ref)
	if err != nil || cur.Content != "worker_processes 1;\n" || !cur.Editable || cur.Path != "/etc/nginx/nginx.conf" || len(cur.Versions) != 0 {
		t.Fatalf("read: %+v %v", cur, err)
	}

	// Saved, checked and nginx reloaded; the previous file is kept.
	op, log := runLineOp(t, o, r, helperapi.ActionPkgConfigWrite, helperapi.ConfigWriteRequest{
		Software: "nginx", Line: "1.26", Config: "nginx.conf", Content: "worker_processes 2;\n", Base: cur.SHA256})
	if op.State != helperapi.OpSucceeded || !f.ran("systemctl reload nginx") {
		t.Fatalf("write: %+v\n%s\n%v", op.Error, log, f.cmds)
	}
	if data, _ := os.ReadFile(conf); string(data) != "worker_processes 2;\n" {
		t.Fatalf("content: %q", data)
	}
	if info, _ := os.Stat(conf); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode not kept: %v", info.Mode())
	}
	if len(*checks) != 1 || (*checks)[0][0] != filepath.Join(s.Root, "/usr/sbin/nginx") || (*checks)[0][1] != "-t" {
		t.Fatalf("check: %v", *checks)
	}
	next, _ := call[helperapi.ConfigFile](t, r, helperapi.ActionPkgConfigRead, ref)
	if len(next.Versions) != 1 {
		t.Fatalf("versions: %+v", next.Versions)
	}
	old, err := call[helperapi.ConfigFile](t, r, helperapi.ActionPkgConfigRead, helperapi.ConfigRequest{
		Software: "nginx", Line: "1.26", Config: "nginx.conf", Version: next.Versions[0].ID})
	if err != nil || old.Content != "worker_processes 1;\n" || old.SavedAt == 0 {
		t.Fatalf("version: %+v %v", old, err)
	}

	// A failed check puts the previous file back.
	checkOK = false
	op, _ = runLineOp(t, o, r, helperapi.ActionPkgConfigWrite, helperapi.ConfigWriteRequest{
		Software: "nginx", Line: "1.26", Config: "nginx.conf", Content: "bogus;\n", Base: next.SHA256})
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodePkgConfigInvalid || !strings.Contains(op.Error.Params["output"].(string), "unknown directive") {
		t.Fatalf("bad check: %+v", op.Error)
	}
	if data, _ := os.ReadFile(conf); string(data) != "worker_processes 2;\n" {
		t.Fatalf("not restored: %q", data)
	}

	// A file changed since it was read is refused.
	op, _ = runLineOp(t, o, r, helperapi.ActionPkgConfigWrite, helperapi.ConfigWriteRequest{
		Software: "nginx", Line: "1.26", Config: "nginx.conf", Content: "x;\n", Base: cur.SHA256})
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodePkgConfigChanged {
		t.Fatalf("stale base: %+v", op.Error)
	}

	// Only the newest versions are kept.
	checkOK = true
	for i := range helperapi.ConfigVersionsKept + 2 {
		got, _ := call[helperapi.ConfigFile](t, r, helperapi.ActionPkgConfigRead, ref)
		op, _ = runLineOp(t, o, r, helperapi.ActionPkgConfigWrite, helperapi.ConfigWriteRequest{
			Software: "nginx", Line: "1.26", Config: "nginx.conf", Content: strings.Repeat("#\n", i+1), Base: got.SHA256})
		if op.State != helperapi.OpSucceeded {
			t.Fatalf("save %d: %+v", i, op.Error)
		}
	}
	if got, _ := call[helperapi.ConfigFile](t, r, helperapi.ActionPkgConfigRead, ref); len(got.Versions) != helperapi.ConfigVersionsKept {
		t.Fatalf("kept %d versions", len(got.Versions))
	}

	// root_access = "never" keeps the files read-only.
	s.RootAccess = "never"
	got, _ := call[helperapi.ConfigFile](t, r, helperapi.ActionPkgConfigRead, ref)
	op, _ = runLineOp(t, o, r, helperapi.ActionPkgConfigWrite, helperapi.ConfigWriteRequest{
		Software: "nginx", Line: "1.26", Config: "nginx.conf", Content: "y;\n", Base: got.SHA256})
	if got.Editable || op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodePkgConfigLocked {
		t.Fatalf("locked: %v %+v", got.Editable, op.Error)
	}

	// Unknown files and lines that are not installed are refused.
	if _, err := call[helperapi.ConfigFile](t, r, helperapi.ActionPkgConfigRead, helperapi.ConfigRequest{Software: "nginx", Line: "1.26", Config: "other.conf"}); !isCode(err, helperapi.CodePkgConfigUnknown) {
		t.Fatalf("unknown config: %v", err)
	}
	if _, err := call[helperapi.ConfigFile](t, r, helperapi.ActionPkgConfigRead, helperapi.ConfigRequest{Software: "php", Line: "8.4", Config: "php.ini"}); !isCode(err, helperapi.CodePkgNotInstalled) {
		t.Fatalf("not installed: %v", err)
	}
}

func isCode(err error, code string) bool {
	he, ok := helperapi.AsError(err)
	return ok && he.Code == code
}

// unitSim gives one unit of the fake system a state that follows the
// systemctl verbs sent to it; onRestart and onStart decide where it ends.
type unitSim struct {
	mu        sync.Mutex
	name      string
	state     string
	onRestart func() (string, error)
	onStart   func() string
	verbs     []string
}

func (u *unitSim) use(t *testing.T) {
	t.Helper()
	innerRun, innerQuery := runCommand, queryCommand
	t.Cleanup(func() { runCommand, queryCommand = innerRun, innerQuery })
	runCommand = func(ctx context.Context, log io.Writer, name string, args ...string) error {
		if name == "systemctl" && len(args) == 2 && args[1] == u.name {
			u.mu.Lock()
			defer u.mu.Unlock()
			u.verbs = append(u.verbs, args[0])
			switch args[0] {
			case "restart":
				st, err := u.onRestart()
				u.state = st
				return err
			case "start":
				u.state = u.onStart()
				if u.state != "active" {
					return errors.New("exit status 1")
				}
				return nil
			case "reset-failed":
				if u.state == "failed" {
					u.state = "inactive"
				}
				return nil
			}
		}
		return innerRun(ctx, log, name, args...)
	}
	queryCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "systemctl" && len(args) == 2 && args[0] == "is-active" && args[1] == u.name {
			u.mu.Lock()
			defer u.mu.Unlock()
			return u.state, nil
		}
		return innerQuery(ctx, name, args...)
	}
}

func (u *unitSim) sent() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.verbs)
}

// redisRestartEnv is a running redis-server whose unit does not restart
// by itself (as on EL): it fails with any file but the original one, and
// starts with that one only while startOK.
func redisRestartEnv(t *testing.T) (*Ops, *Registry, string, *unitSim, *bool) {
	t.Helper()
	f := &fakeSystem{systemd: true, isActive: "active", installed: map[string]string{"redis-server": "5:8.0.2-3"},
		journal: "Bad directive or wrong number of arguments"}
	s, o, r := lineSoftware(t, f)
	conf := filepath.Join(s.Root, "etc/redis/redis.conf")
	writeTestFile(t, conf, "port 6379\n")
	fakeCheck(t, func([]string) (string, error) { return "", nil })
	good := func() bool { b, _ := os.ReadFile(conf); return string(b) == "port 6379\n" }
	startOK := true
	u := &unitSim{name: "redis-server", state: "active",
		onRestart: func() (string, error) {
			if good() {
				return "active", nil
			}
			return "failed", errors.New("exit status 1")
		},
		onStart: func() string {
			if good() && startOK {
				return "active"
			}
			return "failed"
		}}
	u.use(t)
	return o, r, conf, u, &startOK
}

func saveRedisConf(t *testing.T, o *Ops, r *Registry, content string) (helperapi.Op, string) {
	t.Helper()
	cur, err := call[helperapi.ConfigFile](t, r, helperapi.ActionPkgConfigRead, helperapi.ConfigRequest{Software: "redis", Line: "8.0", Config: "redis.conf"})
	if err != nil {
		t.Fatal(err)
	}
	return runLineOp(t, o, r, helperapi.ActionPkgConfigWrite, helperapi.ConfigWriteRequest{
		Software: "redis", Line: "8.0", Config: "redis.conf", Content: content, Base: cur.SHA256})
}

func TestConfigFileRestartFailureRestores(t *testing.T) {
	o, r, conf, u, _ := redisRestartEnv(t)
	checks := fakeCheck(t, func([]string) (string, error) { return "", nil })
	op, log := saveRedisConf(t, o, r, "port nope\n")
	if len(*checks) != 0 {
		t.Fatal("redis.conf has no check")
	}
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodePkgConfigApplyFailed || op.Error.Params["service"] != "redis-server" ||
		!strings.Contains(op.Error.Params["output"].(string), "Bad directive") {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}
	if data, _ := os.ReadFile(conf); string(data) != "port 6379\n" {
		t.Fatalf("not restored: %q", data)
	}
	// The service ran before the save, so it runs again with the
	// previous file: nothing else would start it.
	if u.state != "active" || !slices.Equal(u.sent(), []string{"restart", "reset-failed", "start"}) {
		t.Fatalf("service after the rollback: %q, verbs %v\n%s", u.state, u.sent(), log)
	}
}

func TestConfigFileRestoredButServiceDown(t *testing.T) {
	o, r, conf, u, startOK := redisRestartEnv(t)
	*startOK = false
	op, log := saveRedisConf(t, o, r, "port nope\n")
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodePkgConfigNotRunning || op.Error.Params["service"] != "redis-server" {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}
	if data, _ := os.ReadFile(conf); string(data) != "port 6379\n" {
		t.Fatalf("not restored: %q", data)
	}
	if u.state == "active" || !slices.Contains(u.sent(), "start") {
		t.Fatalf("service: %q, verbs %v", u.state, u.sent())
	}
}

func TestConfigFileServiceStoppedStaysStopped(t *testing.T) {
	o, r, conf, u, _ := redisRestartEnv(t)
	u.state = "inactive"
	// A service that is not running reads the file when it starts; the
	// save does not start it.
	op, log := saveRedisConf(t, o, r, "port 6380\n")
	if op.State != helperapi.OpSucceeded || len(u.sent()) != 0 {
		t.Fatalf("save: %+v verbs %v\n%s", op.Error, u.sent(), log)
	}
	if data, _ := os.ReadFile(conf); string(data) != "port 6380\n" {
		t.Fatalf("content: %q", data)
	}
}

// The service account owns /etc/redis and replaces its configuration
// file with a link to a root-only file: the panel neither shows nor
// replaces that file.
func TestConfigFileRefusesPlantedLink(t *testing.T) {
	f := &fakeSystem{systemd: true, isActive: "active", installed: map[string]string{"redis-server": "5:8.0.2-3"}}
	s, o, r := lineSoftware(t, f)
	fakeCheck(t, func([]string) (string, error) { return "", nil })
	if err := os.Rename(serviceDir(t, s.Root), filepath.Join(s.Root, "etc/redis")); err != nil {
		t.Fatal(err)
	}
	v := newConfTarget(t)
	conf := filepath.Join(s.Root, "etc/redis/redis.conf")
	if err := os.Symlink(v.path, conf); err != nil {
		t.Fatal(err)
	}
	ref := helperapi.ConfigRequest{Software: "redis", Line: "8.0", Config: "redis.conf"}
	got, err := call[helperapi.ConfigFile](t, r, helperapi.ActionPkgConfigRead, ref)
	if !isCode(err, helperapi.CodePkgConfigUnsafe) || strings.Contains(got.Content, "secret") {
		t.Fatalf("read through the link: %q %v", got.Content, err)
	}
	// Even with the right base (the web process may know the content).
	op, log := runLineOp(t, o, r, helperapi.ActionPkgConfigWrite, helperapi.ConfigWriteRequest{
		Software: "redis", Line: "8.0", Config: "redis.conf", Content: "* * * * * root id\n", Base: sha256Hex([]byte(v.content))})
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodePkgConfigUnsafe || op.Error.Params["reason"] != confLink {
		t.Fatalf("write through the link: %+v\n%s", op.Error, log)
	}
	v.untouched(t)
	if fi, err := os.Lstat(conf); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link: %v %v", fi, err)
	}
}

func TestLineServiceActions(t *testing.T) {
	f := &fakeSystem{systemd: true, isActive: "active", installed: map[string]string{"nginx": "1.26.3-3"},
		systemctlShow: "LoadState=loaded\nActiveState=active\nCanReload=yes\n"}
	_, o, r := lineSoftware(t, f)
	fakeCheck(t, func([]string) (string, error) { return "", nil })
	op, _ := runLineOp(t, o, r, helperapi.ActionPkgService, helperapi.ServiceRequest{Software: "nginx", Line: "1.26", Action: "reload"})
	if op.State != helperapi.OpSucceeded || !f.ran("systemctl reload nginx") {
		t.Fatalf("reload: %+v %v", op.Error, f.cmds)
	}
	op, _ = runLineOp(t, o, r, helperapi.ActionPkgService, helperapi.ServiceRequest{Software: "nginx", Line: "1.26", Action: "restart"})
	if op.State != helperapi.OpSucceeded || !f.ran("systemctl restart nginx") {
		t.Fatalf("restart: %+v", op.Error)
	}
	// A unit that cannot reload is refused, and a service that does not
	// come back fails the operation.
	f.systemctlShow = "LoadState=loaded\nActiveState=active\nCanReload=no\n"
	op, _ = runLineOp(t, o, r, helperapi.ActionPkgService, helperapi.ServiceRequest{Software: "nginx", Line: "1.26", Action: "reload"})
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodePkgServiceNoReload {
		t.Fatalf("no reload: %+v", op.Error)
	}
	f.isActive = "failed"
	op, _ = runLineOp(t, o, r, helperapi.ActionPkgService, helperapi.ServiceRequest{Software: "nginx", Line: "1.26", Action: "start"})
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodePkgServiceFailed {
		t.Fatalf("failed start: %+v", op.Error)
	}
	// Only the four actions are accepted.
	if _, err := call[helperapi.OpRef](t, r, helperapi.ActionPkgService, helperapi.ServiceRequest{Software: "nginx", Line: "1.26", Action: "kill"}); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestPHPExtensions(t *testing.T) {
	f := &fakeSystem{systemd: true, isActive: "active",
		installed:  map[string]string{"php8.4-fpm": "8.4.27-1~deb13u1", "php8.4-cli": "8.4.27-1~deb13u1", "php8.4-redis": "6.1.0-1", "php8.4-opcache": "8.4.27-1~deb13u1"},
		phpModules: "[PHP Modules]\nCore\ncurl\nexif\nfileinfo\nredis\nsodium\n\n[Zend Modules]\nZend OPcache\n"}
	_, o, r := lineSoftware(t, f)
	fakeCheck(t, func([]string) (string, error) { return "", nil })
	st, err := call[helperapi.ExtStatus](t, r, helperapi.ActionPkgExtStatus, helperapi.LineRef{Software: "php", Line: "8.4"})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]helperapi.ExtensionInfo{}
	for _, e := range st.Extensions {
		byID[e.ID] = e
	}
	if e := byID["redis"]; !e.Installed || !e.Loaded || e.Builtin || !e.Available {
		t.Fatalf("redis: %+v", e)
	}
	if e := byID["opcache"]; !e.Installed || !e.Loaded || e.Builtin {
		t.Fatalf("opcache: %+v", e)
	}
	if e := byID["intl"]; e.Installed || e.Loaded || !e.Available {
		t.Fatalf("intl: %+v", e)
	}
	if e := byID["sodium"]; !e.Builtin || !e.Available || e.Installed {
		t.Fatalf("sodium is in php-common on Debian: %+v", e)
	}
	if !slices.Contains(st.Modules, "zend opcache") || !slices.Contains(st.Modules, "core") {
		t.Fatalf("modules: %v", st.Modules)
	}

	// Install and remove in one go, then PHP-FPM reloads.
	f.removeSim = "Remv php8.4-redis [6.1.0-1]\n"
	op, log := runLineOp(t, o, r, helperapi.ActionPkgExtApply, helperapi.ExtApplyRequest{Software: "php", Line: "8.4", Install: []string{"intl"}, Remove: []string{"redis"}})
	if op.State != helperapi.OpSucceeded || !f.ran("apt-get remove -y php8.4-redis") ||
		!f.ran("apt-get install -y --no-install-recommends php8.4-intl") || !f.ran("systemctl reload php8.4-fpm") {
		t.Fatalf("apply: %+v\n%s\n%v", op.Error, log, f.cmds)
	}
	var res helperapi.ExtApplyResult
	_ = json.Unmarshal(op.Result, &res)
	if res.Reloaded != "php8.4-fpm" || len(res.Removed) != 1 {
		t.Fatalf("result: %+v", res)
	}

	// Removing an extension never takes other packages along.
	f.installed["php8.4-xml"] = "8.4.27-1~deb13u1"
	f.removeSim = "Remv php8.4-xml [8.4.27-1~deb13u1]\nRemv php8.4-fpm [8.4.27-1~deb13u1]\n"
	op, _ = runLineOp(t, o, r, helperapi.ActionPkgExtApply, helperapi.ExtApplyRequest{Software: "php", Line: "8.4", Remove: []string{"xml"}})
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodePkgExtNeeded || op.Error.Params["by"] != "php8.4-fpm" {
		t.Fatalf("guard: %+v", op.Error)
	}
	if _, ok := f.installed["php8.4-xml"]; !ok {
		t.Fatal("removed despite the guard")
	}
	op, _ = runLineOp(t, o, r, helperapi.ActionPkgExtApply, helperapi.ExtApplyRequest{Software: "php", Line: "8.4", Install: []string{"nope"}})
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodePkgExtUnknown {
		t.Fatalf("unknown: %+v", op.Error)
	}
	if _, err := call[helperapi.ExtStatus](t, r, helperapi.ActionPkgExtStatus, helperapi.LineRef{Software: "nginx", Line: "1.26"}); err == nil {
		t.Fatal("nginx has no extensions")
	}
}

func TestProbePlanVersions(t *testing.T) {
	f := &fakeSystem{madison: strings.Join([]string{
		" nginx | 1.26.3-3+deb13u1 | http://deb.debian.org/debian trixie/main amd64 Packages",
		" libnginx-mod-stream | 1.26.3-3+deb13u1 | http://deb.debian.org/debian trixie/main amd64 Packages",
	}, "\n")}
	s, _, _ := lineSoftware(t, f)
	sw, o, err := s.installableOffer(helperapi.SoftwareRequest{Software: "nginx", Line: "1.26"})
	if err != nil {
		t.Fatal(err)
	}
	p := s.plan(context.Background(), sw, o)
	if p.Method != "system" || !p.RepoReady || len(p.Packages) != 2 || p.Packages[0].Version != "1.26.3-3+deb13u1" || len(p.Services) != 1 {
		t.Fatalf("plan: %+v", p)
	}
	// A vendor repository that is not added yet: versions are unknown.
	sw, o, _ = s.installableOffer(helperapi.SoftwareRequest{Software: "php", Line: "8.3"})
	p = s.plan(context.Background(), sw, o)
	if p.Method != "vendor" || p.Repo != "sury" || p.RepoReady || p.Packages[0].Version != "" {
		t.Fatalf("vendor plan: %+v", p)
	}
}
