// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

func TestSiteNginxReadsOnlySitesItServes(t *testing.T) {
	e := newSiteEnv(t)
	api := helperapi.SiteSpec{Code: "api", Type: helperapi.SiteProxy, Domains: []string{"api.example.com"},
		Proxy: &helperapi.ProxySpec{URL: "http://127.0.0.1:3000"}, Enabled: true}
	if op, log := e.run(helperapi.ActionSiteApply, api); op.State != helperapi.OpSucceeded {
		t.Fatalf("proxy: %+v\n%s", op.Error, log)
	}
	if e.f.ran("usermod --append --groups cw-api www-data") {
		t.Fatal("nginx joined the group of a site it only proxies to")
	}
	// A proxy that becomes a static site gives nginx its files, and back.
	static := api
	static.Type, static.Proxy = helperapi.SiteStatic, nil
	if op, log := e.run(helperapi.ActionSiteApply, static); op.State != helperapi.OpSucceeded {
		t.Fatalf("static: %+v\n%s", op.Error, log)
	}
	if !e.f.ran("usermod --append --groups cw-api www-data") {
		t.Fatalf("nginx cannot read the static site: %v", e.f.cmds)
	}
	if op, log := e.run(helperapi.ActionSiteApply, api); op.State != helperapi.OpSucceeded {
		t.Fatalf("proxy again: %+v\n%s", op.Error, log)
	}
	if !e.f.ran("gpasswd --delete www-data cw-api") {
		t.Fatalf("nginx kept reading a site it only proxies to: %v", e.f.cmds)
	}
	// Applying again changes nothing.
	n := len(e.f.cmds)
	e.run(helperapi.ActionSiteApply, api)
	for _, c := range e.f.cmds[n:] {
		if strings.HasPrefix(c, "usermod") || strings.HasPrefix(c, "gpasswd") {
			t.Fatalf("membership changed again: %s", c)
		}
	}
}

// linkUIDs makes the site's owner and runner distinct users (the fake
// system gives every user the test's own uid).
func linkUIDs(t *testing.T, owner, runner int) {
	t.Helper()
	inner := lookupUser
	lookupUser = func(name string) (int, int, error) {
		uid, gid, err := inner(name)
		switch {
		case strings.HasSuffix(name, "-run"):
			uid = runner
		case strings.HasPrefix(name, "cw-"):
			uid = owner
		}
		return uid, gid, err
	}
	t.Cleanup(func() { lookupUser = inner })
}

func TestSiteAlignLinks(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("changes owners")
	}
	const owner, runner, other = 64101, 64102, 64201
	e := newSiteEnv(t)
	linkUIDs(t, owner, runner)
	spec := helperapi.SiteSpec{Code: "lara", Type: helperapi.SitePHP, Domains: []string{"lara.example.com"}, PHP: "8.4",
		Root: "public", Writable: []string{"storage/app/public"}, Enabled: true}
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}
	app := filepath.Join(e.st.Root, "srv/www/lara/app")
	mk := func(p string, uid int, dir bool) {
		t.Helper()
		full := filepath.Join(app, p)
		var err error
		if dir {
			err = os.MkdirAll(full, 0o750)
		} else {
			err = os.WriteFile(full, []byte("x"), 0o600)
		}
		if err == nil {
			err = os.Lchown(full, uid, -1)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, p string, uid int) {
		t.Helper()
		full := filepath.Join(app, p)
		if err := os.Symlink(target, full); err != nil {
			t.Fatal(err)
		}
		if err := os.Lchown(full, uid, -1); err != nil {
			t.Fatal(err)
		}
	}
	mk("public", owner, true)
	mk("releases/1", owner, true)
	mk("storage/app/public/uploads", runner, true)
	mk("secret.txt", owner, false)
	outside := filepath.Join(e.st.Root, "srv/www/other")
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(outside, other, -1); err != nil {
		t.Fatal(err)
	}
	// Laravel's link, made by the owner (absolute, to the runner's
	// directory) and by root (relative).
	link(filepath.Join(app, "storage/app/public"), "public/storage", owner)
	link("../storage/app/public", "public/media", 0)
	link("releases/1", "current", 0)
	// What must stay as it is: a link to another site's directory, a link
	// of another user, a link already right, a dangling link, and a link
	// to a root file.
	link(outside, "public/other", owner)
	mk("storage/app/public/uploads/x", runner, false)
	link(filepath.Join(app, "secret.txt"), "storage/app/public/uploads/y", other)
	link("../secret.txt", "public/same", owner)
	link("nowhere", "public/dangling", 0)
	link("/etc/passwd", "storage/app/public/passwd", runner)
	// A link with a second name is not the site's to change.
	link("../storage/app/public", "public/twice", 0)
	if err := os.Link(filepath.Join(app, "public/twice"), filepath.Join(e.st.Root, "srv/www/lara/twice")); err != nil {
		t.Fatal(err)
	}

	var log bytes.Buffer
	if err := e.st.alignLinks(t.Context(), &log, "lara"); err != nil {
		t.Fatalf("align: %v\n%s", err, log.String())
	}
	for p, want := range map[string]int{
		"public/storage": runner, "public/media": runner, "current": owner,
		"public/other": owner, "storage/app/public/uploads/y": other, "public/same": owner,
		"public/dangling": 0, "storage/app/public/passwd": runner, "public/twice": 0,
	} {
		info, err := os.Lstat(filepath.Join(app, p))
		if err != nil {
			t.Fatal(err)
		}
		if got := uidOf(info); got != want || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s: owner %d, want %d", p, got, want)
		}
	}
	// What the links point to is untouched.
	for p, want := range map[string]int{"storage/app/public": runner, "releases/1": owner, "secret.txt": owner} {
		if info, _ := os.Lstat(filepath.Join(app, p)); uidOf(info) != want {
			t.Errorf("target %s changed owner: %d", p, uidOf(info))
		}
	}
	if info, _ := os.Stat(outside); uidOf(info) != other {
		t.Error("the other site's directory changed owner")
	}
	if !strings.Contains(log.String(), "app/public/storage now belongs to cw-lara-run") {
		t.Errorf("log:\n%s", log.String())
	}

	// Saving a site that allows links aligns them; one that does not
	// leaves them alone.
	link(filepath.Join(app, "storage/app/public"), "public/later", owner)
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}
	if info, _ := os.Lstat(filepath.Join(app, "public/later")); uidOf(info) != owner {
		t.Fatal("links aligned for a site that does not allow it")
	}
	spec.AllowSymlinks = true
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}
	if info, _ := os.Lstat(filepath.Join(app, "public/later")); uidOf(info) != runner {
		t.Fatal("links not aligned on apply")
	}
}

func TestSiteRefreshRewritesExistingSites(t *testing.T) {
	e := newSiteEnv(t)
	specs := []helperapi.SiteSpec{
		{Code: "blog", Type: helperapi.SitePHP, Domains: []string{"blog.example.com"}, PHP: "8.4", Enabled: true},
		{Code: "files", Type: helperapi.SiteStatic, Domains: []string{"files.example.com"}, AllowSymlinks: true, Enabled: true},
		{Code: "api", Type: helperapi.SiteProxy, Domains: []string{"api.example.com"}, Proxy: &helperapi.ProxySpec{URL: "http://127.0.0.1:3000"}, Enabled: true},
	}
	for _, s := range specs {
		if op, log := e.run(helperapi.ActionSiteApply, s); op.State != helperapi.OpSucceeded {
			t.Fatalf("%s: %+v\n%s", s.Code, op.Error, log)
		}
	}
	// What an earlier version wrote: no opcache isolation, links checked
	// from the web root or not at all, nginx in every site's group; and a
	// site whose PHP line is gone.
	old := map[string]string{
		siteConfPath("blog"):                      "root /srv/www/blog/app;\ndisable_symlinks if_not_owner from=$document_root;\n",
		siteConfPath("files"):                     "root /srv/www/files/app;\ndisable_symlinks off;\n",
		"/etc/php/8.4/fpm/pool.d/cairn-blog.conf": "[cairn-blog]\nuser = cw-blog-run\n",
		"/etc/nginx/cairn/sites/gone.conf":        "# gone\n",
		"/etc/php/8.3/fpm/pool.d/cairn-gone.conf": "[cairn-gone]\n",
	}
	for p, data := range old {
		writeSystemFile(t, e.st.Root, p, data)
	}
	gone, _ := json.Marshal(helperapi.SiteState{Spec: helperapi.SiteSpec{Code: "gone", Type: helperapi.SitePHP,
		Domains: []string{"gone.example.com"}, PHP: "8.3", Enabled: true}})
	writeSystemFile(t, e.st.Root, siteStateDir+"/gone.json", string(gone))
	e.f.mu.Lock()
	e.f.cmds = append(e.f.cmds, "usermod --append --groups cw-api www-data")
	e.f.mu.Unlock()

	var logged bytes.Buffer
	n := len(e.f.cmds)
	e.st.Refresh(t.Context(), slog.New(slog.NewTextHandler(&logged, nil)))
	if !strings.Contains(e.read("/etc/php/8.4/fpm/pool.d/cairn-blog.conf"), "php_admin_flag[opcache.validate_permission] = on") {
		t.Errorf("pool not refreshed:\n%s", e.read("/etc/php/8.4/fpm/pool.d/cairn-blog.conf"))
	}
	for _, code := range []string{"blog", "files"} {
		if conf := e.read(siteConfPath(code)); !strings.Contains(conf, "disable_symlinks if_not_owner from=/srv/www/"+code+"/app;") {
			t.Errorf("%s not refreshed:\n%s", code, conf)
		}
	}
	if e.read("/etc/php/8.3/fpm/pool.d/cairn-gone.conf") != old["/etc/php/8.3/fpm/pool.d/cairn-gone.conf"] {
		t.Error("a site that cannot be rendered was changed")
	}
	if !strings.Contains(logged.String(), "site=gone") {
		t.Errorf("the site left as it was is not reported:\n%s", logged.String())
	}
	if !e.f.ran("gpasswd --delete www-data cw-api") {
		t.Errorf("nginx kept the proxy site's group: %v", e.f.cmds[n:])
	}
	// One check and one reload per service, the pool before nginx.
	var checks []string
	for _, c := range e.f.cmds[n:] {
		if strings.HasSuffix(c, " -t") {
			checks = append(checks, c)
		}
	}
	if strings.Join(checks, ",") != "/usr/sbin/php-fpm8.4 -t,/usr/sbin/nginx -t" {
		t.Errorf("checks: %v", checks)
	}

	// Nothing to do the next time: nothing is checked or reloaded.
	n = len(e.f.cmds)
	e.st.Refresh(t.Context(), slog.New(slog.DiscardHandler))
	if len(e.f.cmds) != n {
		t.Errorf("an up-to-date refresh ran %v", e.f.cmds[n:])
	}

	// A refresh nginx refuses puts every file back.
	writeSystemFile(t, e.st.Root, siteConfPath("blog"), old[siteConfPath("blog")])
	e.f.failPrefix = "/usr/sbin/nginx -t"
	e.st.Refresh(t.Context(), slog.New(slog.DiscardHandler))
	if got := e.read(siteConfPath("blog")); got != old[siteConfPath("blog")] {
		t.Errorf("a refused refresh left:\n%s", got)
	}
}

// nginx's workers keep the groups they started with: taking nginx out of
// a group reloads it even when no file of the site changes.
func TestSiteRefreshReloadsNginxWhenOnlyItsGroupsChange(t *testing.T) {
	e := newSiteEnv(t)
	api := helperapi.SiteSpec{Code: "api", Type: helperapi.SiteProxy, Domains: []string{"api.example.com"},
		Proxy: &helperapi.ProxySpec{URL: "http://127.0.0.1:3000"}, Enabled: true}
	if op, log := e.run(helperapi.ActionSiteApply, api); op.State != helperapi.OpSucceeded {
		t.Fatalf("proxy: %+v\n%s", op.Error, log)
	}
	// An earlier version had put nginx into every site's group.
	e.f.mu.Lock()
	e.f.cmds = append(e.f.cmds, "usermod --append --groups cw-api www-data")
	e.f.mu.Unlock()
	processAlive = func(string) (int, bool) { return 4242, true }
	signals := 0
	oldSignal := signalProcess
	signalProcess = func(int, syscall.Signal) error { signals++; return nil }
	t.Cleanup(func() { signalProcess = oldSignal })

	e.st.Refresh(t.Context(), slog.New(slog.DiscardHandler))
	if !e.f.ran("gpasswd --delete www-data cw-api") || signals != 1 {
		t.Fatalf("after its groups changed nginx got %d reloads: %v", signals, e.f.cmds)
	}
	// Up to date now: nothing is reloaded again.
	e.st.Refresh(t.Context(), slog.New(slog.DiscardHandler))
	if signals != 1 {
		t.Fatalf("an up-to-date refresh reloaded nginx (%d signals)", signals)
	}
}

func TestSiteStartRefreshHoldsWebLock(t *testing.T) {
	e := newSiteEnv(t)
	release, ok := e.o.TryLock(LockWeb)
	if !ok {
		t.Fatal("web lock busy")
	}
	var logged bytes.Buffer
	e.st.StartRefresh(t.Context(), e.o, slog.New(slog.NewTextHandler(&logged, nil)))
	if !strings.Contains(logged.String(), "site refresh skipped") {
		t.Fatalf("refresh ran while the web lock was held:\n%s", logged.String())
	}
	release()
}
