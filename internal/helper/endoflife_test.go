// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/platform"
)

// A line that is installed stays managed once its upstream support has
// ended, and it can still be installed anew: the web API asks for an
// acknowledgement, the helper resolves the line like any other. The tests
// set the date the catalog's end-of-life dates are compared with.

// testClock is a clock tests move between operations.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) set(t *testing.T, day string) {
	t.Helper()
	d, err := time.Parse(time.DateOnly, day)
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.t = d.Add(12 * time.Hour)
	c.mu.Unlock()
}

// clockFor makes day today for s.
func clockFor(t *testing.T, s *Software, day string) *testClock {
	t.Helper()
	c := &testClock{}
	c.set(t, day)
	s.now = c.now
	return c
}

func opOutcome(op helperapi.Op) string {
	if op.State == helperapi.OpSucceeded {
		return "succeeded"
	}
	if op.Error != nil {
		return op.State + ":" + op.Error.Code
	}
	return op.State
}

func installedLinesOf(st helperapi.SoftwareStatus, sw string) []string {
	var out []string
	for l := range st.Installed[sw] {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// installableEnded reports whether an ended line resolves for installing.
func installableEnded(t *testing.T, s *Software, software, line string) {
	t.Helper()
	if _, o, err := s.installableOffer(helperapi.SoftwareRequest{Software: software, Line: line}); err != nil || !o.EndOfLife || !o.Installable {
		t.Errorf("install %s %s: %+v %v", software, line, o, err)
	}
}

func TestEndOfLifeVendorPHPStaysManaged(t *testing.T) {
	e := newSiteEnv(t) // Debian 13: PHP 8.4 is the system's, 8.2 comes from sury
	s := e.st.Software
	s.ConfigVersionsDir = filepath.Join(t.TempDir(), "versions")
	clock := clockFor(t, s, "2026-12-31") // the last day of PHP 8.2's upstream support
	fakeCheck(t, func([]string) (string, error) { return "", nil })
	const v82 = "8.2.30-1+0~20261101.80+debian13~1.gbp1a2b3c"
	e.f.mu.Lock()
	for p, v := range map[string]string{"nginx": "1.26.3-3", "libnginx-mod-stream": "1.26.3-3",
		"php8.2-fpm": v82, "php8.2-cli": v82, "php8.4-fpm": "8.4.27-1~deb13u1", "php8.4-cli": "8.4.27-1~deb13u1"} {
		e.f.installed[p] = v
	}
	e.f.isActive = "active"
	e.f.systemctlShow = "LoadState=loaded\nActiveState=active\nCanReload=yes\n"
	e.f.phpModules = "[PHP Modules]\nCore\ncurl\n"
	e.f.mu.Unlock()
	writeSystemFile(t, s.Root, "/usr/sbin/php-fpm8.2", "#!binary")
	writeSystemFile(t, s.Root, "/etc/php/8.2/fpm/php.ini", "memory_limit = 128M\n")
	ctx := context.Background()
	site := func(code, line string, domains ...string) helperapi.SiteSpec {
		return helperapi.SiteSpec{Code: code, Type: helperapi.SitePHP, Domains: domains, PHP: line, Enabled: true}
	}
	withSystemd := func(fn func()) {
		e.f.systemd = true
		defer func() { e.f.systemd = false }()
		fn()
	}
	remove := func() helperapi.Op {
		op, _ := runSoftwareOp(t, e.o, e.r, helperapi.ActionPkgRemove, helperapi.SoftwareRequest{Software: "php", Line: "8.2"})
		return op
	}
	pool := func(line, code string) string { return "/etc/php/" + line + "/fpm/pool.d/cairn-" + code + ".conf" }

	st, err := s.Status(ctx)
	if err != nil || !slices.Equal(installedLinesOf(st, "php"), []string{"8.2", "8.4"}) {
		t.Fatalf("supported: installed lines %v %v", installedLinesOf(st, "php"), err)
	}
	for _, sp := range []helperapi.SiteSpec{site("blog", "8.2", "blog.example.com", "www.blog.example.com"), site("shop", "8.2", "shop.example.com"), site("old", "8.2", "old.example.com")} {
		if op, log := e.run(helperapi.ActionSiteApply, sp); op.State != helperapi.OpSucceeded {
			t.Fatalf("supported: apply %s: %+v\n%s", sp.Code, op.Error, log)
		}
	}
	if op := remove(); opOutcome(op) != "failed:"+helperapi.CodePkgInUse {
		t.Fatalf("supported: removing a line sites use: %s", opOutcome(op))
	}

	// The next day. Nothing on the server changed.
	clock.set(t, "2027-01-01")

	// The line is still installed, with its service.
	withSystemd(func() {
		st, err = s.Status(ctx)
	})
	if err != nil || !slices.Equal(installedLinesOf(st, "php"), []string{"8.2", "8.4"}) || len(st.Installed["php"]["8.2"]) != 2 {
		t.Errorf("status: installed lines %v %v", installedLinesOf(st, "php"), err)
	}
	asked := false
	e.f.mu.Lock()
	for _, c := range e.f.cmds {
		asked = asked || strings.HasPrefix(c, "query systemctl show ") && strings.Contains(c, " php8.2-fpm")
	}
	e.f.mu.Unlock()
	if !asked {
		t.Error("status: the state of php8.2-fpm was not asked for")
	}
	sites, err := call[helperapi.SiteStatus](t, e.r, helperapi.ActionSiteStatus, helperapi.Empty{})
	if err != nil || !slices.Contains(sites.PHP, "8.2") || !slices.Contains(sites.PHP, "8.4") {
		t.Errorf("site status: php lines %v %v", sites.PHP, err)
	}

	// Its service, configuration files and extensions.
	withSystemd(func() {
		op, _ := runLineOp(t, e.o, e.r, helperapi.ActionPkgService, helperapi.ServiceRequest{Software: "php", Line: "8.2", Action: "restart"})
		if op.State != helperapi.OpSucceeded || !e.f.ran("systemctl restart php8.2-fpm") {
			t.Errorf("service restart: %s", opOutcome(op))
		}
	})
	ini := helperapi.ConfigRequest{Software: "php", Line: "8.2", Config: "php.ini"}
	cf, err := call[helperapi.ConfigFile](t, e.r, helperapi.ActionPkgConfigRead, ini)
	if err != nil || cf.Path != "/etc/php/8.2/fpm/php.ini" || cf.Content != "memory_limit = 128M\n" {
		t.Errorf("config read: %+v %v", cf, err)
	}
	op, log := runLineOp(t, e.o, e.r, helperapi.ActionPkgConfigWrite, helperapi.ConfigWriteRequest{Software: "php", Line: "8.2", Config: "php.ini",
		Content: "memory_limit = 256M\n", Base: sha256Hex([]byte("memory_limit = 128M\n"))})
	if op.State != helperapi.OpSucceeded || e.read("/etc/php/8.2/fpm/php.ini") != "memory_limit = 256M\n" {
		t.Errorf("config write: %s\n%s", opOutcome(op), log)
	}
	ext, err := call[helperapi.ExtStatus](t, e.r, helperapi.ActionPkgExtStatus, helperapi.LineRef{Software: "php", Line: "8.2"})
	if err != nil || !slices.ContainsFunc(ext.Extensions, func(x helperapi.ExtensionInfo) bool { return x.ID == "intl" && x.Available }) {
		t.Errorf("extensions: %v %v", ext.Extensions, err)
	}
	op, log = runLineOp(t, e.o, e.r, helperapi.ActionPkgExtApply, helperapi.ExtApplyRequest{Software: "php", Line: "8.2", Install: []string{"intl"}})
	if op.State != helperapi.OpSucceeded || !e.f.ran("apt-get install -y --no-install-recommends php8.2-intl") {
		t.Errorf("extension install: %s\n%s", opOutcome(op), log)
	}

	// Sites on the line can still be changed, stopped and created.
	if op, log := e.run(helperapi.ActionSiteApply, site("blog", "8.2", "blog.example.com")); op.State != helperapi.OpSucceeded ||
		strings.Contains(e.read("/etc/nginx/cairn/sites/blog.conf"), "www.blog.example.com") {
		t.Errorf("site change: %s\n%s", opOutcome(op), log)
	}
	stopped := site("blog", "8.2", "blog.example.com")
	stopped.Enabled = false
	if op, log := e.run(helperapi.ActionSiteApply, stopped); op.State != helperapi.OpSucceeded {
		t.Errorf("site stop: %s\n%s", opOutcome(op), log)
	}
	if op, log := e.run(helperapi.ActionSiteApply, site("fresh", "8.2", "fresh.example.com")); op.State != helperapi.OpSucceeded || !e.exists(pool("8.2", "fresh")) {
		t.Errorf("new site on the line: %s\n%s", opOutcome(op), log)
	}

	// Removing it is refused for the same reason as before: sites use it.
	if op := remove(); opOutcome(op) != "failed:"+helperapi.CodePkgInUse || op.Error.Params["sites"] != "blog fresh old shop" {
		t.Errorf("remove while sites use it: %s %+v", opOutcome(op), op.Error)
	}

	// Moving a site to a supported line takes its pool out of the old
	// line: two pools on one socket would stop the new line's PHP-FPM.
	oldChecks := e.f.count("/usr/sbin/php-fpm8.2 -t")
	op, log = e.run(helperapi.ActionSiteApply, site("shop", "8.4", "shop.example.com"))
	if op.State != helperapi.OpSucceeded || e.exists(pool("8.2", "shop")) || !e.exists(pool("8.4", "shop")) {
		t.Errorf("move to 8.4: %s; old pool left: %v; new pool written: %v\n%s", opOutcome(op), e.exists(pool("8.2", "shop")), e.exists(pool("8.4", "shop")), log)
	}
	if e.f.count("/usr/sbin/php-fpm8.2 -t") != oldChecks+1 {
		t.Errorf("move to 8.4: the old line's configuration was not checked\n%s", log)
	}

	// Deleting a site of the line removes its pool before its user goes: a
	// pool of a user that does not exist stops the line's PHP-FPM.
	op, log = e.run(helperapi.ActionSiteRemove, helperapi.SiteRemoveRequest{Code: "old", DeleteFiles: true})
	if op.State != helperapi.OpSucceeded || e.exists(pool("8.2", "old")) || !e.f.ran("userdel cw-old-run") {
		t.Errorf("delete a site: %s; pool left: %v; userdel ran: %v\n%s", opOutcome(op), e.exists(pool("8.2", "old")), e.f.ran("userdel cw-old-run"), log)
	}

	// The line, and an older ended one, can still be installed anew.
	for _, line := range []string{"8.2", "8.1"} {
		installableEnded(t, s, "php", line)
	}

	// With no site left on it, the line can be removed.
	for _, code := range []string{"blog", "fresh"} {
		if op, log := e.run(helperapi.ActionSiteRemove, helperapi.SiteRemoveRequest{Code: code}); op.State != helperapi.OpSucceeded || e.exists(pool("8.2", code)) {
			t.Errorf("remove site %s: %s\n%s", code, opOutcome(op), log)
		}
	}
	if op := remove(); op.State != helperapi.OpSucceeded || !e.f.ran("apt-get remove -y php8.2-fpm php8.2-cli") {
		t.Errorf("remove: %s %+v", opOutcome(op), op.Error)
	}
	if st, _ := s.Status(ctx); !slices.Equal(installedLinesOf(st, "php"), []string{"8.4"}) {
		t.Errorf("after the removal: installed lines %v", installedLinesOf(st, "php"))
	}
}

func TestEndOfLifeVendorPostgreSQLStaysAnEngine(t *testing.T) {
	// Debian 13: 17 is the system's line, 14 comes from PGDG.
	e := newDBEnv(t, map[string]string{"postgresql-14": "14.20-1.pgdg13+1", "postgresql-17": "17.6-0+deb13u1"})
	clock := clockFor(t, e.d.Software, "2026-11-12") // the last day of PostgreSQL 14's upstream support
	e.reply = func(c dbCall) (string, error) {
		if c.name == "/usr/bin/pg_lsclusters" {
			return pgClusters + "14  main    5433 online postgres /var/lib/postgresql/14/main /var/log/postgresql/postgresql-14-main.log\n", nil
		}
		return "", nil
	}
	ids := func() []string {
		var out []string
		for _, en := range e.status().Engines {
			out = append(out, en.ID)
		}
		return out
	}
	if got := ids(); !slices.Equal(got, []string{"postgresql-17", "postgresql-14"}) {
		t.Fatalf("supported: engines %v", got)
	}
	clock.set(t, "2026-11-13")
	if got := ids(); !slices.Equal(got, []string{"postgresql-17", "postgresql-14"}) {
		t.Errorf("engines %v", got)
	}
	en, err := e.d.engine(context.Background(), "postgresql-14")
	if err != nil || en.service != "postgresql" || en.version != "14.20-1.pgdg13+1" {
		t.Errorf("engine: %+v %v", en, err)
	}
	e.reset()
	if op, log := e.run(helperapi.ActionDBCreate, helperapi.DBCreateRequest{Engine: "postgresql-14", Name: "blog"}); op.State != helperapi.OpSucceeded ||
		!strings.Contains(e.script(), `CREATE DATABASE "blog"`) {
		t.Errorf("create a database: %s\n%s", opOutcome(op), log)
	}
	// The line can still be installed anew.
	installableEnded(t, e.d.Software, "postgresql", "14")
}

func TestEndOfLifeRuntimeStaysManaged(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{}
	f.use(t)
	s, o, r := softwareFor(t, cat, "debian-13", trixie, "apt")
	clock := clockFor(t, s, "2027-04-30") // the last day of Node.js 22's upstream support
	s.Runtimes = &Runtimes{Dir: filepath.Join(s.Root, "opt/cairn/runtimes"), Arch: "amd64"}
	dir := filepath.Join(s.Runtimes.Dir, "nodejs")
	if err := os.MkdirAll(filepath.Join(dir, "22.21.1"), 0o750); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(manifest{Software: "nodejs", Line: "22", Version: "22.21.1"})
	if err := os.WriteFile(filepath.Join(dir, "22.21.1.json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("22.21.1", filepath.Join(dir, "22")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if st, _ := s.Status(ctx); !slices.Equal(installedLinesOf(st, "nodejs"), []string{"22"}) {
		t.Fatalf("supported: installed lines %v", installedLinesOf(st, "nodejs"))
	}
	clock.set(t, "2027-05-01")
	st, _ := s.Status(ctx)
	if !slices.Equal(installedLinesOf(st, "nodejs"), []string{"22"}) || st.Installed["nodejs"]["22"][0].Version != "22.21.1" {
		t.Errorf("installed lines %v", installedLinesOf(st, "nodejs"))
	}
	// Still installable.
	installableEnded(t, s, "nodejs", "22")
	op, _ := runSoftwareOp(t, o, r, helperapi.ActionPkgRemove, helperapi.SoftwareRequest{Software: "nodejs", Line: "22"})
	if op.State != helperapi.OpSucceeded || s.Runtimes.Installed("nodejs", "22") != "" {
		t.Errorf("remove: %s %+v", opOutcome(op), op.Error)
	}
}

// Enterprise Linux, where the PHP lines besides the system's are Remi's.
func TestEndOfLifeRemiPHPStaysManaged(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	const v82 = "8.2.30-1.el9.remi"
	f := &fakeSystem{installed: map[string]string{"php82-php-fpm": v82, "php82-php-cli": v82}}
	f.use(t)
	s, o, r := softwareFor(t, cat, "el-9", platform.OSRelease{ID: "rocky", VersionID: "9.7"}, "dnf")
	clock := clockFor(t, s, "2026-12-31")
	fakeCheck(t, func([]string) (string, error) { return "", nil })
	ctx := context.Background()
	if st, _ := s.Status(ctx); !slices.Equal(installedLinesOf(st, "php"), []string{"8.2"}) {
		t.Fatalf("supported: installed lines %v", installedLinesOf(st, "php"))
	}
	clock.set(t, "2027-01-01")
	if st, _ := s.Status(ctx); !slices.Equal(installedLinesOf(st, "php"), []string{"8.2"}) {
		t.Errorf("installed lines %v", installedLinesOf(st, "php"))
	}
	installableEnded(t, s, "php", "8.2")
	op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgRemove, helperapi.SoftwareRequest{Software: "php", Line: "8.2"})
	if op.State != helperapi.OpSucceeded || !f.ran("dnf remove -y php82-php-fpm php82-php-cli") {
		t.Errorf("remove: %s %+v\n%s", opOutcome(op), op.Error, log)
	}
}

// Every line keeps installing whatever the date: a vendor line still in
// upstream support, one past it, and a system line whatever its upstream
// says.
func TestEndOfLifeLeavesOtherLinesInstallable(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{systemd: true, madison: trixieMadison}
	f.use(t)
	s, o, r := softwareFor(t, cat, "debian-13", trixie, "apt")
	clock := clockFor(t, s, "2027-01-01")
	for _, tc := range []struct {
		day, software, line, method, reason string
	}{
		{"2026-12-31", "php", "8.2", catalog.MethodVendor, ""},
		{"2027-01-01", "php", "8.2", catalog.MethodVendor, ""},
		{"2027-01-01", "php", "8.3", catalog.MethodVendor, ""},
		{"2027-01-01", "php", "8.4", catalog.MethodSystem, ""},
		// Debian maintains its own 8.4 after upstream stops.
		{"2029-06-01", "php", "8.4", catalog.MethodSystem, ""},
		{"2029-06-01", "postgresql", "17", catalog.MethodSystem, ""},
		{"2029-06-01", "postgresql", "18", catalog.MethodVendor, ""},
	} {
		clock.set(t, tc.day)
		req := helperapi.SoftwareRequest{Software: tc.software, Line: tc.line}
		// What is installed is resolved the same way whatever the date.
		if _, known, err := s.knownOffer(req); err != nil || known.Method != tc.method || len(known.Packages) == 0 {
			t.Errorf("%s %s on %s: known %+v %v", tc.software, tc.line, tc.day, known, err)
		}
		_, got, err := s.installableOffer(req)
		switch he, _ := helperapi.AsError(err); {
		case tc.reason == "" && (err != nil || got.Method != tc.method):
			t.Errorf("%s %s on %s: installable %+v %v", tc.software, tc.line, tc.day, got, err)
		case tc.reason != "" && (he == nil || he.Code != helperapi.CodePkgNotAvailable || he.Params["reason"] != tc.reason):
			t.Errorf("%s %s on %s: want %s, got %v", tc.software, tc.line, tc.day, tc.reason, err)
		}
	}
	// A line nobody builds for this release, an unknown line and unknown
	// software are not available either way.
	for _, req := range []helperapi.SoftwareRequest{{Software: "php", Line: "9.9"}, {Software: "nope", Line: "1"}, {Software: "nodejs", Line: "24"}} {
		if _, _, err := s.knownOffer(req); !isCode(err, helperapi.CodePkgNotAvailable) {
			t.Errorf("%v: known: %v", req, err)
		}
		if _, _, err := s.installableOffer(req); !isCode(err, helperapi.CodePkgNotAvailable) {
			t.Errorf("%v: installable: %v", req, err)
		}
	}

	// The system's PHP 8.4 installs after its upstream support ended.
	clock.set(t, "2029-06-01")
	op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "php", Line: "8.4"})
	var res helperapi.InstallResult
	_ = json.Unmarshal(op.Result, &res)
	if op.State != helperapi.OpSucceeded || res.Method != catalog.MethodSystem || !f.ran("systemctl enable --now php8.4-fpm") {
		t.Fatalf("install the system line: %s %+v\n%s", opOutcome(op), op.Error, log)
	}
}

// A PHP line removed outside the panel (its packages gone, its pool
// directory kept) no longer has a PHP-FPM to check or reload, but the pools
// of its sites still go when the sites move or are deleted.
func TestSitePoolOfAnUninstalledLineIsRemoved(t *testing.T) {
	e := newSiteEnv(t)
	clockFor(t, e.st.Software, "2026-12-01")
	writeSystemFile(t, e.st.Root, "/usr/sbin/php-fpm8.2", "#!binary")
	site := func(code, line string) helperapi.SiteSpec {
		return helperapi.SiteSpec{Code: code, Type: helperapi.SitePHP, Domains: []string{code + ".example.com"}, PHP: line, Enabled: true}
	}
	for _, code := range []string{"shop", "old"} {
		if op, log := e.run(helperapi.ActionSiteApply, site(code, "8.2")); op.State != helperapi.OpSucceeded {
			t.Fatalf("apply %s: %+v\n%s", code, op.Error, log)
		}
	}
	if err := os.Remove(filepath.Join(e.st.Root, "/usr/sbin/php-fpm8.2")); err != nil {
		t.Fatal(err)
	}
	fpmRuns := e.f.count("/usr/sbin/php-fpm8.2")

	// The site cannot stay on the line...
	if op, _ := e.run(helperapi.ActionSiteApply, site("shop", "8.2")); opOutcome(op) != "failed:"+helperapi.CodeSitePHPMissing {
		t.Errorf("apply on the missing line: %s", opOutcome(op))
	}
	// ...but it can move away, and its old pool goes.
	op, log := e.run(helperapi.ActionSiteApply, site("shop", "8.4"))
	if op.State != helperapi.OpSucceeded || e.exists("/etc/php/8.2/fpm/pool.d/cairn-shop.conf") || !e.exists("/etc/php/8.4/fpm/pool.d/cairn-shop.conf") {
		t.Errorf("move: %s; old pool left: %v\n%s", opOutcome(op), e.exists("/etc/php/8.2/fpm/pool.d/cairn-shop.conf"), log)
	}
	op, log = e.run(helperapi.ActionSiteRemove, helperapi.SiteRemoveRequest{Code: "old", DeleteFiles: true})
	if op.State != helperapi.OpSucceeded || e.exists("/etc/php/8.2/fpm/pool.d/cairn-old.conf") || !e.f.ran("userdel cw-old-run") {
		t.Errorf("delete: %s; pool left: %v\n%s", opOutcome(op), e.exists("/etc/php/8.2/fpm/pool.d/cairn-old.conf"), log)
	}
	if e.f.count("/usr/sbin/php-fpm8.2") != fpmRuns {
		t.Errorf("ran the PHP-FPM that is not there: %v", e.f.cmds)
	}
}

// When the catalog does not say where a site's PHP line keeps its pools,
// the site's pool cannot be found. Moving or deleting the site then fails
// before anything changes: going on would leave the pool behind, listening
// on the site's socket or running as a user that is deleted next.
func TestSiteOfAnUnknownPHPLineIsNotTouched(t *testing.T) {
	e := newSiteEnv(t)
	clockFor(t, e.st.Software, "2026-12-01")
	spec := helperapi.SiteSpec{Code: "lost", Type: helperapi.SitePHP, Domains: []string{"lost.example.com"}, PHP: "5.6", Enabled: true}
	state, _ := json.Marshal(helperapi.SiteState{Spec: spec, AppliedAt: 1})
	const statePath, confPath, poolPath = siteStateDir + "/lost.json", "/etc/nginx/cairn/sites/lost.conf", "/etc/php/5.6/fpm/pool.d/cairn-lost.conf"
	writeSystemFile(t, e.st.Root, statePath, string(state))
	writeSystemFile(t, e.st.Root, confPath, "# lost\n")
	writeSystemFile(t, e.st.Root, poolPath, "[cairn-lost]\nuser = cw-lost-run\n")
	writeSystemFile(t, e.st.Root, "/srv/www/lost/app/index.php", "<?php\n")
	untouched := func(step string) {
		t.Helper()
		for _, p := range []string{statePath, confPath, poolPath, "/srv/www/lost/app/index.php"} {
			if !e.exists(p) {
				t.Errorf("%s: %s is gone", step, p)
			}
		}
		if e.read(confPath) != "# lost\n" || e.exists("/etc/php/8.4/fpm/pool.d/cairn-lost.conf") || e.f.ran("useradd") || e.f.ran("userdel") || e.f.ran("groupdel") {
			t.Errorf("%s: something changed: %v", step, e.f.cmds)
		}
	}

	moved := spec
	moved.PHP = "8.4"
	op, _ := e.run(helperapi.ActionSiteApply, moved)
	if opOutcome(op) != "failed:"+helperapi.CodeSitePHPUnknown || op.Error.Params["line"] != "5.6" {
		t.Errorf("move: %s %+v", opOutcome(op), op.Error)
	}
	untouched("move")
	static := helperapi.SiteSpec{Code: "lost", Type: helperapi.SiteStatic, Domains: []string{"lost.example.com"}, Enabled: true}
	if op, _ := e.run(helperapi.ActionSiteApply, static); opOutcome(op) != "failed:"+helperapi.CodeSitePHPUnknown {
		t.Errorf("turn into a static site: %s", opOutcome(op))
	}
	untouched("turn into a static site")
	for _, del := range []bool{false, true} {
		op, _ := e.run(helperapi.ActionSiteRemove, helperapi.SiteRemoveRequest{Code: "lost", DeleteFiles: del})
		if opOutcome(op) != "failed:"+helperapi.CodeSitePHPUnknown || op.Error.Params["line"] != "5.6" {
			t.Errorf("remove (delete files: %v): %s %+v", del, opOutcome(op), op.Error)
		}
		untouched("remove")
	}
}
