// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// serveModel follows what nginx and each PHP-FPM master have loaded: a
// service takes up its files when it is reloaded ("systemctl reload" on
// the fake systemd), and nginx must never send a site's requests to a
// socket no master listens on.
type serveModel struct {
	t     *testing.T
	root  string
	pools map[string]string // PHP-FPM service -> its pool directory
	nginx map[string]string // site -> the socket nginx sends its PHP to
	fpm   map[string][]string
	// reloads are the services in the order they were reloaded.
	reloads []string
}

func newServeModel(t *testing.T, e *siteEnv, pools map[string]string) *serveModel {
	m := &serveModel{t: t, root: e.st.Root, pools: pools, fpm: map[string][]string{}}
	m.loadNginx()
	for svc := range pools {
		m.loadFPM(svc)
	}
	e.f.systemd, e.f.isActive = true, "active"
	e.f.hook = func(cmd string, _ io.Writer) {
		if svc, ok := strings.CutPrefix(cmd, "systemctl reload "); ok {
			m.reload(svc)
		}
	}
	t.Cleanup(func() { e.f.hook = nil })
	return m
}

func (m *serveModel) loadNginx() {
	m.nginx = map[string]string{}
	files, _ := filepath.Glob(filepath.Join(m.root, "/etc/nginx/cairn/sites/*.conf"))
	for _, f := range files {
		data, _ := os.ReadFile(f)
		for _, l := range strings.Split(string(data), "\n") {
			if s, ok := strings.CutPrefix(strings.TrimSpace(l), "fastcgi_pass unix:"); ok {
				m.nginx[strings.TrimSuffix(filepath.Base(f), ".conf")] = strings.TrimSuffix(s, ";")
			}
		}
	}
}

func (m *serveModel) loadFPM(svc string) {
	m.fpm[svc] = nil
	files, _ := filepath.Glob(filepath.Join(m.root, m.pools[svc], "*.conf"))
	for _, f := range files {
		data, _ := os.ReadFile(f)
		for _, l := range strings.Split(string(data), "\n") {
			if s, ok := strings.CutPrefix(l, "listen = "); ok {
				m.fpm[svc] = append(m.fpm[svc], s)
			}
		}
	}
	// Two masters listening on one socket: the second does not start.
	seen := map[string]string{}
	for other, socks := range m.fpm {
		for _, s := range socks {
			if was, ok := seen[s]; ok && was != other {
				m.t.Errorf("after reloading %s: %s and %s both listen on %s", svc, was, other, s)
			}
			seen[s] = other
		}
	}
}

func (m *serveModel) reload(svc string) {
	m.reloads = append(m.reloads, svc)
	if svc == "nginx" {
		m.loadNginx()
	} else if _, ok := m.pools[svc]; ok {
		m.loadFPM(svc)
	}
	m.check("reloading " + svc)
}

func (m *serveModel) check(when string) {
	for site, sock := range m.nginx {
		served := false
		for _, socks := range m.fpm {
			served = served || contains(socks, sock)
		}
		if !served {
			m.t.Errorf("after %s: nginx sends the requests of %s to %s, where no PHP-FPM listens", when, site, sock)
		}
	}
}

// installPHP82 adds PHP 8.2 (sury's on Debian 13) next to the system's 8.4.
func installPHP82(t *testing.T, e *siteEnv) {
	t.Helper()
	writeSystemFile(t, e.st.Root, "/usr/sbin/php-fpm8.2", "#!binary")
	if _, installed := e.st.phpFPM("8.2"); !installed {
		t.Fatal("PHP 8.2 is not installed for sites")
	}
}

var twoLines = map[string]string{"php8.4-fpm": "/etc/php/8.4/fpm/pool.d", "php8.2-fpm": "/etc/php/8.2/fpm/pool.d"}

// Two PHP lines run side by side, each with sites; a site moves from one to
// the other and back. The lines share /run/php, so each pool's socket
// names its line, and the new line listens before nginx moves and the old
// one lets go only after.
func TestSitePHPLinesSideBySide(t *testing.T) {
	e := newSiteEnv(t)
	installPHP82(t, e)
	php := func(code, line string) helperapi.SiteSpec {
		return helperapi.SiteSpec{Code: code, Type: helperapi.SitePHP, Domains: []string{code + ".example.com"}, PHP: line, Enabled: true}
	}
	for _, s := range []helperapi.SiteSpec{php("blog", "8.4"), php("shop", "8.2"), php("wiki", "8.4")} {
		if op, log := e.run(helperapi.ActionSiteApply, s); op.State != helperapi.OpSucceeded {
			t.Fatalf("apply %s: %+v\n%s", s.Code, op.Error, log)
		}
	}
	if got := e.read("/etc/php/8.2/fpm/pool.d/cairn-shop.conf"); !strings.Contains(got, "listen = /run/php/cairn-shop-php8.2.sock\n") {
		t.Fatalf("pool on 8.2:\n%s", got)
	}
	m := newServeModel(t, e, twoLines)
	if len(m.nginx) != 3 || m.nginx["shop"] != "/run/php/cairn-shop-php8.2.sock" {
		t.Fatalf("nginx sends PHP to %v", m.nginx)
	}
	m.check("applying the sites")

	for _, step := range []struct{ line, from, to string }{{"8.2", "php8.4-fpm", "php8.2-fpm"}, {"8.4", "php8.2-fpm", "php8.4-fpm"}} {
		m.reloads = nil
		if op, log := e.run(helperapi.ActionSiteApply, php("blog", step.line)); op.State != helperapi.OpSucceeded {
			t.Fatalf("move blog to %s: %+v\n%s", step.line, op.Error, log)
		}
		if want := []string{step.to, "nginx", step.from}; strings.Join(m.reloads, " ") != strings.Join(want, " ") {
			t.Errorf("moving blog to %s reloaded %v, want %v", step.line, m.reloads, want)
		}
		if want := "/run/php/cairn-blog-php" + step.line + ".sock"; m.nginx["blog"] != want {
			t.Errorf("blog on %s: nginx sends PHP to %s, want %s", step.line, m.nginx["blog"], want)
		}
		old := strings.TrimSuffix(strings.TrimPrefix(step.from, "php"), "-fpm")
		if e.exists("/etc/php/" + old + "/fpm/pool.d/cairn-blog.conf") {
			t.Errorf("the pool on %s is left", old)
		}
		// The other sites of both lines keep their sockets.
		if m.nginx["shop"] != "/run/php/cairn-shop-php8.2.sock" || m.nginx["wiki"] != "/run/php/cairn-wiki-php8.4.sock" {
			t.Errorf("the other sites moved: %v", m.nginx)
		}
	}

	// The new line's PHP-FPM does not come up with the pool: nginx does not
	// move, and the site stays on its line.
	m.reloads = nil
	waitListening = func(_ context.Context, socks []string, _ time.Duration) error {
		if len(socks) == 1 && socks[0] == filepath.Join(e.st.Root, "/run/php/cairn-blog-php8.2.sock") {
			return errors.New("connection refused")
		}
		return nil
	}
	if op, _ := e.run(helperapi.ActionSiteApply, php("blog", "8.2")); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeSiteReloadFailed {
		t.Fatalf("a line that does not listen: %+v", op.Error)
	}
	waitListening = func(context.Context, []string, time.Duration) error { return nil }
	// nginx never moved; the previous files were reloaded everywhere.
	if got := strings.Join(m.reloads, " "); got != "php8.2-fpm php8.2-fpm nginx php8.4-fpm" {
		t.Errorf("reloads %s", got)
	}
	if m.nginx["blog"] != "/run/php/cairn-blog-php8.4.sock" || e.exists("/etc/php/8.2/fpm/pool.d/cairn-blog.conf") || !e.exists("/etc/php/8.4/fpm/pool.d/cairn-blog.conf") {
		t.Errorf("after the failed move: nginx %v", m.nginx)
	}

	// A site leaving PHP: nginx stops sending to its pool before it goes.
	m.reloads = nil
	static := php("wiki", "")
	static.Type = helperapi.SiteStatic
	if op, log := e.run(helperapi.ActionSiteApply, static); op.State != helperapi.OpSucceeded {
		t.Fatalf("wiki to static: %+v\n%s", op.Error, log)
	}
	if want := "nginx php8.4-fpm"; strings.Join(m.reloads, " ") != want {
		t.Errorf("wiki to static reloaded %v, want %s", m.reloads, want)
	}
}

// earlierPool rewrites a site's files as an earlier version wrote them: the
// pool on the socket without the line in its name.
func earlierPool(t *testing.T, e *siteEnv, code, line string) {
	t.Helper()
	newSock := "/run/php/cairn-" + code + "-php" + line + ".sock"
	oldSock := "/run/php/cairn-" + code + ".sock"
	for _, p := range []string{"/etc/php/" + line + "/fpm/pool.d/cairn-" + code + ".conf", siteConfPath(code)} {
		writeSystemFile(t, e.st.Root, p, strings.ReplaceAll(e.read(p), newSock, oldSock))
	}
}

// After an upgrade, the refresh at the helper's start moves every PHP site
// to the socket that names its line. PHP-FPM drops the old socket when it
// reloads and nginx uses it until it reloads, so the pool listens on both
// until nginx has moved; a second pass then drops the old one.
func TestSiteRefreshMovesPoolsToTheirLineSocket(t *testing.T) {
	old := bridgeSettle
	bridgeSettle = 0
	t.Cleanup(func() { bridgeSettle = old })
	e := newSiteEnv(t)
	installPHP82(t, e)
	for _, s := range []helperapi.SiteSpec{
		{Code: "blog", Type: helperapi.SitePHP, Domains: []string{"blog.example.com"}, PHP: "8.4", Enabled: true},
		{Code: "shop", Type: helperapi.SitePHP, Domains: []string{"shop.example.com"}, PHP: "8.2", Enabled: true},
		{Code: "docs", Type: helperapi.SiteStatic, Domains: []string{"docs.example.com"}, Enabled: true},
	} {
		if op, log := e.run(helperapi.ActionSiteApply, s); op.State != helperapi.OpSucceeded {
			t.Fatalf("apply %s: %+v\n%s", s.Code, op.Error, log)
		}
	}
	current := e.read("/etc/php/8.4/fpm/pool.d/cairn-blog.conf")
	earlierPool(t, e, "blog", "8.4")
	earlierPool(t, e, "shop", "8.2")
	m := newServeModel(t, e, twoLines)
	if m.nginx["blog"] != "/run/php/cairn-blog.sock" {
		t.Fatalf("the earlier files: %v", m.nginx)
	}
	var bridged []string
	e.f.hook = func(cmd string, _ io.Writer) {
		if svc, ok := strings.CutPrefix(cmd, "systemctl reload "); ok {
			if svc == "php8.4-fpm" {
				data, _ := os.ReadFile(filepath.Join(e.st.Root, "/etc/php/8.4/fpm/pool.d/cairn-blog.conf"))
				bridged = append(bridged, poolListen(data, "cairn-blog.old"))
			}
			m.reload(svc)
		}
	}
	e.st.Refresh(t.Context(), slog.New(slog.DiscardHandler))

	// Both lines first, then nginx; in the second pass the same.
	if len(m.reloads) == 6 {
		sort.Strings(m.reloads[:2])
		sort.Strings(m.reloads[3:5])
	}
	if got := strings.Join(m.reloads, " "); got != "php8.2-fpm php8.4-fpm nginx php8.2-fpm php8.4-fpm nginx" {
		t.Errorf("reloads: %s", got)
	}
	if strings.Join(bridged, ",") != "/run/php/cairn-blog.sock," {
		t.Errorf("the pool on the old socket while nginx moved, then without it: %q", bridged)
	}
	if got := e.read("/etc/php/8.4/fpm/pool.d/cairn-blog.conf"); got != current {
		t.Errorf("after the refresh:\n%s\nwant:\n%s", got, current)
	}
	if m.nginx["blog"] != "/run/php/cairn-blog-php8.4.sock" || m.nginx["shop"] != "/run/php/cairn-shop-php8.2.sock" {
		t.Errorf("nginx sends PHP to %v", m.nginx)
	}

	// Without a change, nothing is reloaded.
	m.reloads = nil
	e.st.Refresh(t.Context(), slog.New(slog.DiscardHandler))
	if len(m.reloads) != 0 {
		t.Errorf("a refresh with nothing to do reloaded %v", m.reloads)
	}
}

// A site saved while its pool still listens on the earlier socket (its
// refresh failed) keeps that socket too until its next change.
func TestSiteApplyKeepsEarlierSocketUntilNginxMoved(t *testing.T) {
	e := newSiteEnv(t)
	spec := helperapi.SiteSpec{Code: "blog", Type: helperapi.SitePHP, Domains: []string{"blog.example.com"}, PHP: "8.4", Enabled: true}
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}
	earlierPool(t, e, "blog", "8.4")
	m := newServeModel(t, e, map[string]string{"php8.4-fpm": "/etc/php/8.4/fpm/pool.d"})
	spec.Domains = append(spec.Domains, "www.blog.example.com")
	for _, want := range []string{"/run/php/cairn-blog.sock", ""} {
		if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
			t.Fatalf("apply: %+v\n%s", op.Error, log)
		}
		pool := []byte(e.read("/etc/php/8.4/fpm/pool.d/cairn-blog.conf"))
		if got := poolListen(pool, "cairn-blog.old"); got != want || poolListen(pool, "cairn-blog") != "/run/php/cairn-blog-php8.4.sock" {
			t.Errorf("pool: earlier socket %q, want %q:\n%s", got, want, pool)
		}
		spec.Domains = spec.Domains[:1]
	}
	if m.nginx["blog"] != "/run/php/cairn-blog-php8.4.sock" {
		t.Errorf("nginx sends PHP to %v", m.nginx)
	}
}
