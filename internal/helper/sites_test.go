// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// siteEnv is a site engine on a fake Debian 13 under a temporary root, with
// nginx and PHP 8.4 "installed" and the users and services faked.
type siteEnv struct {
	t  *testing.T
	f  *fakeSystem
	st *Sites
	o  *Ops
	r  *Registry
}

func newSiteEnv(t *testing.T) *siteEnv {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{}
	f.use(t)
	s, o, r := softwareFor(t, cat, "debian-13", trixie, "apt")
	st := &Sites{Software: s, NginxUser: "www-data", Root: s.Root}
	RegisterSites(r, o, st)
	for _, bin := range []string{nginxBinary, "/usr/sbin/php-fpm8.4"} {
		writeSystemFile(t, s.Root, bin, "#!binary")
	}
	uid, gid := os.Getuid(), os.Getgid()
	oldUser, oldGroup, oldAlive := lookupUser, lookupGroup, processAlive
	// Users and groups exist once the fake system ran their useradd or
	// groupadd, and until their userdel or groupdel.
	exists := func(add, del, name string) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		ok := false
		for _, c := range f.cmds {
			switch {
			case strings.HasPrefix(c, add+" ") && strings.HasSuffix(c, " "+name):
				ok = true
			case c == del+" "+name:
				ok = false
			}
		}
		return ok
	}
	lookupUser = func(name string) (int, int, error) {
		if !exists("useradd", "userdel", name) {
			return 0, 0, os.ErrNotExist
		}
		return uid, gid, nil
	}
	lookupGroup = func(name string) (int, error) {
		if !exists("groupadd", "groupdel", name) {
			return 0, os.ErrNotExist
		}
		return gid, nil
	}
	processAlive = func(string) (int, bool) { return 0, false }
	// nginx is in a group from its usermod until its gpasswd --delete.
	oldMember := groupHasMember
	groupHasMember = func(_, group, user string) (bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		in := false
		for _, c := range f.cmds {
			switch c {
			case "usermod --append --groups " + group + " " + user:
				in = true
			case "gpasswd --delete " + user + " " + group:
				in = false
			}
		}
		return in, nil
	}
	t.Cleanup(func() { lookupUser, lookupGroup, processAlive, groupHasMember = oldUser, oldGroup, oldAlive, oldMember })
	return &siteEnv{t: t, f: f, st: st, o: o, r: r}
}

func (e *siteEnv) read(p string) string {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.st.Root, p))
	if err != nil {
		e.t.Fatalf("read %s: %v", p, err)
	}
	return string(data)
}

func (e *siteEnv) exists(p string) bool {
	_, err := os.Lstat(filepath.Join(e.st.Root, p))
	return err == nil
}

func (e *siteEnv) run(action string, req any) (helperapi.Op, string) {
	e.t.Helper()
	ref, err := call[helperapi.OpRef](e.t, e.r, action, req)
	if err != nil {
		e.t.Fatalf("%s: %v", action, err)
	}
	op := waitOp(e.t, e.o, ref.ID, helperapi.OpSucceeded, helperapi.OpFailed)
	got, _ := e.o.Get(ref.ID, 0)
	return op, got.Log
}

func TestSiteApplyPHPSite(t *testing.T) {
	e := newSiteEnv(t)
	spec := helperapi.SiteSpec{Code: "blog", Type: helperapi.SitePHP, Domains: []string{"blog.example.com", "www.blog.example.com"},
		PHP: "8.4", Rewrite: "wordpress", Writable: []string{"wp-content/uploads"}, Enabled: true}
	op, log := e.run(helperapi.ActionSiteApply, spec)
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}
	conf := e.read("/etc/nginx/cairn/sites/blog.conf")
	for _, want := range []string{
		"server_name blog.example.com www.blog.example.com;",
		"root /srv/www/blog/app;",
		"index index.php index.html index.htm;",
		"try_files $uri $uri/ /index.php?$args;",
		"fastcgi_pass unix:/run/php/cairn-blog.sock;",
		"access_log /var/log/nginx/cairn/blog.access.log cairn_json;",
		"disable_symlinks if_not_owner from=/srv/www/blog/app;",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("nginx conf lacks %q:\n%s", want, conf)
		}
	}
	if strings.Count(conf, "location / {") != 1 {
		t.Errorf("the rewrite rule must replace the default location:\n%s", conf)
	}
	pool := e.read("/etc/php/8.4/fpm/pool.d/cairn-blog.conf")
	for _, want := range []string{"[cairn-blog]", "user = cw-blog-run", "group = cw-blog", "listen.owner = www-data",
		"php_admin_value[open_basedir] = /srv/www/blog/", "listen = /run/php/cairn-blog.sock",
		// The line's sites share one opcache.
		"php_admin_flag[opcache.validate_permission] = on", "php_admin_value[opcache.restrict_api] = /nonexistent/"} {
		if !strings.Contains(pool, want) {
			t.Errorf("pool lacks %q:\n%s", want, pool)
		}
	}
	if !strings.Contains(e.read("/etc/nginx/conf.d/cairn.conf"), "include /etc/nginx/cairn/sites/*.conf;") {
		t.Error("global include missing")
	}
	// Users, then the checks of both services before anything reloads.
	for _, c := range []string{"groupadd --system cw-blog", "useradd --system --gid cw-blog --home-dir /srv/www/blog --no-create-home --shell /usr/sbin/nologin cw-blog-run",
		"usermod --append --groups cw-blog www-data", "/usr/sbin/php-fpm8.4 -t", "/usr/sbin/nginx -t"} {
		if !e.f.ran(c) {
			t.Errorf("did not run %q: %v", c, e.f.cmds)
		}
	}
	// Directory layout and modes.
	for p, want := range map[string]os.FileMode{
		"/srv/www/blog":                        0o750,
		"/srv/www/blog/app":                    0o750 | os.ModeSetgid,
		"/srv/www/blog/tmp":                    0o770 | os.ModeSetgid,
		"/srv/www/blog/app/wp-content":         0o750 | os.ModeSetgid,
		"/srv/www/blog/app/wp-content/uploads": 0o770 | os.ModeSetgid,
	} {
		info, err := os.Lstat(filepath.Join(e.st.Root, p))
		if err != nil || info.Mode()&(os.ModePerm|os.ModeSetgid) != want {
			t.Errorf("%s: %v %v, want %v", p, info.Mode(), err, want)
		}
	}
	if !e.exists("/srv/www/blog/app/index.html") || !e.exists("/var/lib/cairn-helper/sites/blog.json") {
		t.Error("placeholder page or state missing")
	}
	var res helperapi.SiteResult
	_ = json.Unmarshal(op.Result, &res)
	if res.Owner != "cw-blog" || res.Runner != "cw-blog-run" || res.Dir != "/srv/www/blog" {
		t.Errorf("result: %+v", res)
	}

	// Another site may not take one of its domains.
	other := helperapi.SiteSpec{Code: "other", Type: helperapi.SiteStatic, Domains: []string{"www.blog.example.com"}, Enabled: true}
	if op, _ := e.run(helperapi.ActionSiteApply, other); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeSiteDomainTaken {
		t.Fatalf("domain conflict: %+v", op.Error)
	}

	// Switching the site to static removes its pool.
	spec.Type, spec.PHP, spec.Rewrite, spec.Writable = helperapi.SiteStatic, "", "spa", nil
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("to static: %+v\n%s", op.Error, log)
	}
	if e.exists("/etc/php/8.4/fpm/pool.d/cairn-blog.conf") || strings.Contains(e.read("/etc/nginx/cairn/sites/blog.conf"), "fastcgi_pass") {
		t.Fatal("pool or PHP location left after switching to static")
	}
}

func TestSiteCheckFailureRestoresEverything(t *testing.T) {
	e := newSiteEnv(t)
	spec := helperapi.SiteSpec{Code: "shop", Type: helperapi.SiteStatic, Domains: []string{"shop.example.com"}, Enabled: true}
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}
	before := e.read("/etc/nginx/cairn/sites/shop.conf")

	e.f.failPrefix = "/usr/sbin/nginx -t"
	spec.Type, spec.Proxy, spec.Root = helperapi.SiteProxy, &helperapi.ProxySpec{URL: "http://127.0.0.1:3000", WebSocket: true}, ""
	op, log := e.run(helperapi.ActionSiteApply, spec)
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeSiteConfigInvalid {
		t.Fatalf("failing check: %+v\n%s", op.Error, log)
	}
	if out, _ := op.Error.Params["output"].(string); !strings.Contains(out, "[emerg]") {
		t.Errorf("the check output is not reported: %v", op.Error.Params)
	}
	if got := e.read("/etc/nginx/cairn/sites/shop.conf"); got != before {
		t.Fatalf("the previous configuration was not put back:\n%s", got)
	}
	var state helperapi.SiteState
	_ = json.Unmarshal([]byte(e.read("/var/lib/cairn-helper/sites/shop.json")), &state)
	if state.Spec.Type != helperapi.SiteStatic {
		t.Fatalf("state changed after a failed apply: %+v", state.Spec)
	}

	// A failed reload also puts the previous files back.
	e.f.failPrefix = ""
	processAlive = func(string) (int, bool) { return 4242, true }
	signals := 0
	oldSignal := signalProcess
	signalProcess = func(int, syscall.Signal) error {
		signals++
		if signals == 1 {
			return syscall.EPERM
		}
		return nil
	}
	t.Cleanup(func() { signalProcess = oldSignal })
	if op, _ := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeSiteReloadFailed {
		t.Fatalf("failing reload: %+v", op.Error)
	}
	if got := e.read("/etc/nginx/cairn/sites/shop.conf"); got != before || signals != 2 {
		t.Fatalf("after a failed reload: %d signals\n%s", signals, got)
	}

	// Now it works: a WebSocket proxy.
	signalProcess = func(int, syscall.Signal) error { return nil }
	op, log = e.run(helperapi.ActionSiteApply, spec)
	conf := e.read("/etc/nginx/cairn/sites/shop.conf")
	if op.State != helperapi.OpSucceeded || !strings.Contains(conf, "proxy_pass http://127.0.0.1:3000;") ||
		!strings.Contains(conf, "Connection $cairn_connection_upgrade") || strings.Contains(conf, "root /srv/www") {
		t.Fatalf("proxy: %+v\n%s\n%s", op.Error, log, conf)
	}
}

func TestSiteStopRemoveAndLogs(t *testing.T) {
	e := newSiteEnv(t)
	spec := helperapi.SiteSpec{Code: "docs", Type: helperapi.SiteStatic, Domains: []string{"docs.example.com"}, Enabled: false}
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded ||
		!strings.Contains(e.read("/etc/nginx/cairn/sites/docs.conf"), "return 503;") {
		t.Fatalf("stopped site: %+v\n%s", op.Error, log)
	}
	writeSystemFile(t, e.st.Root, "/var/log/nginx/cairn/docs.access.log", strings.Repeat("x", 70<<10)+"\nlast line\n")
	got, err := e.st.readLog(helperapi.SiteLogRequest{Code: "docs", Kind: helperapi.SiteLogAccess, Offset: -1})
	if err != nil || !strings.HasSuffix(got.Data, "last line\n") || got.Offset != got.Size || len(got.Data) > tailLogRead {
		t.Fatalf("tail: %d bytes, offset %d of %d, %v", len(got.Data), got.Offset, got.Size, err)
	}
	if again, _ := e.st.readLog(helperapi.SiteLogRequest{Code: "docs", Kind: helperapi.SiteLogAccess, Offset: got.Offset}); again.Data != "" {
		t.Fatalf("nothing new expected: %q", again.Data)
	}

	// Removing keeps the files unless asked.
	if op, _ := e.run(helperapi.ActionSiteRemove, helperapi.SiteRemoveRequest{Code: "docs"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("remove: %+v", op.Error)
	}
	if e.exists("/etc/nginx/cairn/sites/docs.conf") || !e.exists("/srv/www/docs/app/index.html") || e.f.ran("userdel") {
		t.Fatal("remove without files")
	}
	if op, _ := e.run(helperapi.ActionSiteRemove, helperapi.SiteRemoveRequest{Code: "docs"}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeSiteNotFound {
		t.Fatalf("second remove: %+v", op.Error)
	}
	e.run(helperapi.ActionSiteApply, spec)
	if op, _ := e.run(helperapi.ActionSiteRemove, helperapi.SiteRemoveRequest{Code: "docs", DeleteFiles: true}); op.State != helperapi.OpSucceeded {
		t.Fatalf("remove with files: %+v", op.Error)
	}
	if e.exists("/srv/www/docs") || !e.f.ran("userdel cw-docs-run") || !e.f.ran("groupdel cw-docs") || e.exists("/var/log/nginx/cairn/docs.access.log") {
		t.Fatalf("files or users left: %v", e.f.cmds)
	}
}

func TestSiteRefusals(t *testing.T) {
	e := newSiteEnv(t)
	for name, tc := range map[string]struct {
		spec helperapi.SiteSpec
		code string
	}{
		"php line not installed":   {helperapi.SiteSpec{Code: "a1", Type: helperapi.SitePHP, Domains: []string{"a.example.com"}, PHP: "8.3", Enabled: true}, helperapi.CodeSitePHPMissing},
		"rewrite for another type": {helperapi.SiteSpec{Code: "a2", Type: helperapi.SiteStatic, Domains: []string{"b.example.com"}, Rewrite: "wordpress", Enabled: true}, helperapi.CodeSiteRewrite},
		"unknown rewrite":          {helperapi.SiteSpec{Code: "a3", Type: helperapi.SiteStatic, Domains: []string{"c.example.com"}, Rewrite: "nope", Enabled: true}, helperapi.CodeSiteRewrite},
	} {
		if op, _ := e.run(helperapi.ActionSiteApply, tc.spec); op.State != helperapi.OpFailed || op.Error.Code != tc.code {
			t.Errorf("%s: %+v", name, op.Error)
		}
	}
	// A symlink planted by the owner never makes root change anything
	// outside the app directory.
	spec := helperapi.SiteSpec{Code: "evil", Type: helperapi.SitePHP, Domains: []string{"evil.example.com"}, PHP: "8.4", Enabled: true}
	if op, _ := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v", op.Error)
	}
	outside := filepath.Join(e.st.Root, "etc", "important")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.st.Root, "srv/www/evil/app/uploads")); err != nil {
		t.Fatal(err)
	}
	spec.Writable = []string{"uploads"}
	if op, _ := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpFailed {
		t.Fatal("a symlinked writable directory was accepted")
	}
	if info, _ := os.Stat(outside); info.Mode().Perm() != 0o700 {
		t.Fatalf("outside directory changed: %v", info.Mode())
	}
	// Not on this distribution yet.
	e.st.Unsupported = "selinux_pending"
	if op, _ := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeSiteUnsupported {
		t.Fatalf("unsupported: %+v", op.Error)
	}
}

func TestSiteSpecValidation(t *testing.T) {
	good := helperapi.SiteSpec{Code: "my-site", Type: helperapi.SiteStatic, Domains: []string{"example.com", "*.example.com", "203.0.113.7"}, Root: "public", Enabled: true}
	if err := good.Validate(); err != nil {
		t.Fatalf("good spec refused: %v", err)
	}
	bad := map[string]func(s *helperapi.SiteSpec){
		"code with --":      func(s *helperapi.SiteSpec) { s.Code = "a--b" },
		"code upper case":   func(s *helperapi.SiteSpec) { s.Code = "Site" },
		"code too long":     func(s *helperapi.SiteSpec) { s.Code = strings.Repeat("a", 21) },
		"domain with space": func(s *helperapi.SiteSpec) { s.Domains = []string{"a b.com"} },
		"domain injection":  func(s *helperapi.SiteSpec) { s.Domains = []string{"a.com;include/etc"} },
		"bare tld":          func(s *helperapi.SiteSpec) { s.Domains = []string{"com"} },
		"numeric tld":       func(s *helperapi.SiteSpec) { s.Domains = []string{"1.2.3"} },
		"ipv6":              func(s *helperapi.SiteSpec) { s.Domains = []string{"::1"} },
		"duplicate domain":  func(s *helperapi.SiteSpec) { s.Domains = []string{"a.com", "a.com"} },
		"root escapes":      func(s *helperapi.SiteSpec) { s.Root = "../etc" },
		"root absolute":     func(s *helperapi.SiteSpec) { s.Root = "/etc" },
		"root with brace":   func(s *helperapi.SiteSpec) { s.Root = "pub}lic" },
		"index with slash":  func(s *helperapi.SiteSpec) { s.Index = []string{"a/b.html"} },
		"php fields":        func(s *helperapi.SiteSpec) { s.Writable = []string{"uploads"} },
		"proxy on static":   func(s *helperapi.SiteSpec) { s.Proxy = &helperapi.ProxySpec{URL: "http://127.0.0.1"} },
		"php without line":  func(s *helperapi.SiteSpec) { s.Type = helperapi.SitePHP },
		"proxy without url": func(s *helperapi.SiteSpec) { s.Type, s.Root = helperapi.SiteProxy, "" },
		"proxy bad scheme": func(s *helperapi.SiteSpec) {
			s.Type, s.Root, s.Proxy = helperapi.SiteProxy, "", &helperapi.ProxySpec{URL: "unix:/run/x.sock"}
		},
		"proxy injection": func(s *helperapi.SiteSpec) {
			s.Type, s.Root, s.Proxy = helperapi.SiteProxy, "", &helperapi.ProxySpec{URL: "http://a.com/x;return 200"}
		},
		"proxy with userinfo": func(s *helperapi.SiteSpec) {
			s.Type, s.Root, s.Proxy = helperapi.SiteProxy, "", &helperapi.ProxySpec{URL: "http://u:p@a.com"}
		},
		"symlinks on a proxy": func(s *helperapi.SiteSpec) {
			s.Type, s.Root, s.Proxy, s.AllowSymlinks = helperapi.SiteProxy, "", &helperapi.ProxySpec{URL: "http://127.0.0.1"}, true
		},
	}
	// The path is checked escaped, but nginx gets the URL as written.
	for _, u := range []string{"http://a.com/a b", "http://a.com/x{", "http://a.com/x}", `http://a.com/a"b`, `http://a.com/a\b`,
		"http://a.com/$host", "http://a.com/a'b", "http://a.com/?"} {
		bad["proxy "+u] = func(s *helperapi.SiteSpec) {
			s.Type, s.Root, s.Proxy = helperapi.SiteProxy, "", &helperapi.ProxySpec{URL: u}
		}
	}
	for name, mutate := range bad {
		s := good
		s.Domains = append([]string(nil), good.Domains...)
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, u := range []string{"http://127.0.0.1:3000", "https://api.example.com/v1/", "http://localhost:8080", "http://[::1]:9000",
		"http://a.com/a%20b/~x_y.z-1", "http://a.com/a+b,c=d@e!f*(g)"} {
		if !helperapi.ValidProxyURL(u) {
			t.Errorf("proxy URL %s refused", u)
		}
	}
}

func TestSiteSymlinksAndForeignUsers(t *testing.T) {
	e := newSiteEnv(t)
	// Sites serving files refuse links to files of another owner; a proxy
	// serves none.
	for _, spec := range []helperapi.SiteSpec{
		{Code: "files", Type: helperapi.SiteStatic, Domains: []string{"files.example.com"}, Enabled: true},
		{Code: "blog", Type: helperapi.SitePHP, Domains: []string{"blog.example.com"}, PHP: "8.4", Enabled: true},
		{Code: "api", Type: helperapi.SiteProxy, Domains: []string{"api.example.com"}, Proxy: &helperapi.ProxySpec{URL: "http://127.0.0.1:3000"}, Enabled: true},
	} {
		if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
			t.Fatalf("%s: %+v\n%s", spec.Code, op.Error, log)
		}
		conf := e.read(siteConfPath(spec.Code))
		// Checked from below app/: a web root that is itself a link to
		// another site's directory is refused too.
		want := "disable_symlinks if_not_owner from=/srv/www/" + spec.Code + "/app;"
		if has := strings.Contains(conf, want); has != (spec.Type != helperapi.SiteProxy) || strings.Contains(conf, "$document_root") {
			t.Errorf("%s: disable_symlinks %v:\n%s", spec.Code, has, conf)
		}
	}

	// A user of the site's names that is not in the site's group (made for
	// something else) is never taken over.
	e.f.mu.Lock()
	e.f.cmds = append(e.f.cmds, "useradd --system cw-shop-run")
	e.f.mu.Unlock()
	inner := lookupUser
	lookupUser = func(name string) (int, int, error) {
		uid, gid, err := inner(name)
		if name == "cw-shop-run" {
			gid++
		}
		return uid, gid, err
	}
	spec := helperapi.SiteSpec{Code: "shop", Type: helperapi.SiteStatic, Domains: []string{"shop.example.com"}, Enabled: true}
	op, _ := e.run(helperapi.ActionSiteApply, spec)
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeSiteUserConflict || op.Error.Params["user"] != "cw-shop-run" {
		t.Fatalf("foreign user: %+v", op.Error)
	}
	if e.exists("/srv/www/shop") || e.exists(siteConfPath("shop")) {
		t.Fatal("the site was set up with a foreign user")
	}

	// A site whose links cross its own users still has nginx check every
	// link's owner: its links never reach another site's files.
	files := helperapi.SiteSpec{Code: "files", Type: helperapi.SiteStatic, Domains: []string{"files.example.com"}, Root: "public", AllowSymlinks: true, Enabled: true}
	if op, log := e.run(helperapi.ActionSiteApply, files); op.State != helperapi.OpSucceeded {
		t.Fatalf("allow symlinks: %+v\n%s", op.Error, log)
	}
	if conf := e.read(siteConfPath("files")); strings.Contains(conf, "disable_symlinks off") ||
		!strings.Contains(conf, "disable_symlinks if_not_owner from=/srv/www/files/app;") {
		t.Fatalf("allow symlinks:\n%s", conf)
	}
}

func TestSiteCodesEndingInRun(t *testing.T) {
	e := newSiteEnv(t)
	// The runner of site "x" is cw-x-run: a new site "x-run" would get it.
	spec := helperapi.SiteSpec{Code: "x-run", Type: helperapi.SiteStatic, Domains: []string{"x.example.com"}, Enabled: true}
	if err := spec.Validate(); err != nil {
		t.Fatalf("an existing code must still validate: %v", err)
	}
	op, _ := e.run(helperapi.ActionSiteApply, spec)
	if op.State != helperapi.OpFailed || op.Error.Code != "validation.invalid" || op.Error.Params["rule"] != "site_code_new" {
		t.Fatalf("new -run site: %+v", op.Error)
	}
	if helperapi.ValidNewSiteCode("x-run") || !helperapi.ValidNewSiteCode("x-runner") {
		t.Fatal("new site code rule")
	}
	// A site that already has such a code is still loaded and changed.
	state, _ := json.Marshal(helperapi.SiteState{Spec: spec})
	writeSystemFile(t, e.st.Root, siteStateDir+"/x-run.json", string(state))
	if codes := e.st.SiteCodes(); len(codes) != 1 || codes[0] != "x-run" {
		t.Fatalf("existing -run site not loaded: %v", codes)
	}
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("existing -run site: %+v\n%s", op.Error, log)
	}
}

func TestSitePlaceholderIsNewFile(t *testing.T) {
	e := newSiteEnv(t)
	spec := helperapi.SiteSpec{Code: "shop", Type: helperapi.SiteStatic, Domains: []string{"shop.example.com"}, Enabled: true}
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}
	info, err := os.Lstat(filepath.Join(e.st.Root, "srv/www/shop/app/index.html"))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o640 {
		t.Fatalf("placeholder: %v %v", info, err)
	}
}
