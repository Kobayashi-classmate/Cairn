// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// nginxMap is one "map $from $to { ... }" block of the panel-wide file.
type nginxMap struct {
	from, to, fallback string
	rules              []nginxMapRule
}

type nginxMapRule struct {
	re    *regexp.Regexp
	value string
}

var (
	mapHeader = regexp.MustCompile(`^map (\$\w+) (\$\w+) \{$`)
	mapRule   = regexp.MustCompile(`^    "~(.*)" (\S*);$`)
	nginxVar  = regexp.MustCompile(`\$\w+`)
)

// parseCookieMaps reads the maps of siteCookieMaps the way nginx would.
func parseCookieMaps(t *testing.T) []nginxMap {
	t.Helper()
	var maps []nginxMap
	var cur *nginxMap
	for _, line := range strings.Split(siteCookieMaps(), "\n") {
		switch {
		case strings.HasPrefix(line, "#") || line == "":
		case mapHeader.MatchString(line):
			m := mapHeader.FindStringSubmatch(line)
			maps = append(maps, nginxMap{from: m[1], to: m[2]})
			cur = &maps[len(maps)-1]
		case line == "}":
			cur = nil
		case cur != nil && strings.HasPrefix(line, "    default "):
			cur.fallback = strings.TrimSuffix(strings.TrimPrefix(line, "    default "), ";")
		case cur != nil && mapRule.MatchString(line):
			m := mapRule.FindStringSubmatch(line)
			cur.rules = append(cur.rules, nginxMapRule{re: regexp.MustCompile(m[1]), value: strings.Trim(m[2], `"`)})
		default:
			t.Fatalf("unexpected line in the cookie maps: %q", line)
		}
	}
	return maps
}

// siteCookie evaluates $cairn_site_cookie for a request's Cookie header:
// in each map the first matching expression gives the value, with its
// named captures filled in.
func siteCookie(t *testing.T, maps []nginxMap, header string) string {
	t.Helper()
	vars := map[string]string{"$http_cookie": header}
	for _, m := range maps {
		src, ok := vars[m.from]
		if !ok {
			t.Fatalf("map %s reads %s, which nothing before it defines", m.to, m.from)
		}
		out := vars[m.fallback]
		for _, r := range m.rules {
			match := r.re.FindStringSubmatch(src)
			if match == nil {
				continue
			}
			caps := map[string]string{}
			for i, name := range r.re.SubexpNames() {
				if name != "" {
					caps["$"+name] = match[i]
				}
			}
			out = nginxVar.ReplaceAllStringFunc(r.value, func(v string) string {
				c, ok := caps[v]
				if !ok {
					t.Fatalf("map %s uses %s, which its expression does not capture", m.to, v)
				}
				return c
			})
			break
		}
		vars[m.to] = out
	}
	out, ok := vars["$cairn_site_cookie"]
	if !ok {
		t.Fatal("the maps do not define $cairn_site_cookie")
	}
	return out
}

// A site's code gets the request's cookies without the panel's own, which
// the browser sends to a site on the panel's host name as well; the site's
// own cookies arrive untouched.
func TestSiteCookieMapsTakeOutThePanelCookies(t *testing.T) {
	// The maps go by the prefix of the names the panel uses.
	for _, name := range []string{auth.CookieName, auth.DeviceCookieName} {
		if !strings.HasPrefix(name, panelCookiePrefix) {
			t.Fatalf("panel cookie %s does not start with %s", name, panelCookiePrefix)
		}
	}
	maps := parseCookieMaps(t)
	if len(maps) != panelCookiePasses+1 {
		t.Fatalf("%d maps", len(maps))
	}
	session, device := auth.CookieName+"=c2Vzc2lvbg", auth.DeviceCookieName+"=ZGV2aWNl"
	for header, want := range map[string]string{
		"":                             "",
		"site=1":                       "site=1",
		"site=1; theme=dark; sid=a=b=": "site=1; theme=dark; sid=a=b=",
		session + "; site=1":           "site=1",
		"site=1; " + session:           "site=1",
		"a=1; " + session + "; b=2":    "a=1; b=2",
		session:                        "",
		session + "; " + device:        "",
		device + "; a=1; " + session + "; b=2; c=3": "a=1; b=2; c=3",
		"a=1; " + device + "; " + session + "; b=2": "a=1; b=2",
		"a=1; " + session + "; b=2; " + device:      "a=1; b=2",
		"a=1;" + session + ";b=2":                   "a=1;b=2",
		session + ";site=1":                         "site=1",
		" " + session + "; site=1":                  "site=1",
		panelCookiePrefix + "future=1; site=1":      "site=1",
		"site=1; " + panelCookiePrefix + "=":        "site=1",
		"site=1; " + panelCookiePrefix:              "site=1",
		// Names and values that only contain the prefix are the site's own.
		"x" + session + "; site=1":                          "x" + session + "; site=1",
		"site=" + session:                                   "site=" + session,
		"a=1; note=see " + panelCookiePrefix + "session; b": "a=1; note=see " + panelCookiePrefix + "session; b",
		"a=1; __host-cairn_session=x":                       "a=1; __host-cairn_session=x",
		"a=1; Host-cairn_session=x":                         "a=1; Host-cairn_session=x",
		// More panel cookies than a browser can hold: nothing is passed on.
		session + "; a=1; " + device + "; " + session + "; " + device + "; b=2": "",
		"a=1; " + strings.Repeat(session+"; ", panelCookiePasses+1) + "b=2":     "",
	} {
		got := siteCookie(t, maps, header)
		if got != want {
			t.Errorf("Cookie: %q\n  the site gets %q\n  want          %q", header, got, want)
		}
		if strings.Contains(header, "; "+panelCookiePrefix) || strings.HasPrefix(strings.TrimSpace(header), panelCookiePrefix) {
			if strings.Contains(got, session) || strings.Contains(got, device) {
				t.Errorf("Cookie: %q: a panel cookie reached the site: %q", header, got)
			}
		}
	}
}

// Every way a site's code is reached gets the filtered header: a reverse
// proxy, an app behind nginx, PHP through FastCGI. An existing site gets it
// when the helper starts, with the panel-wide file that defines it.
func TestSiteConfigKeepsPanelCookiesFromTheSite(t *testing.T) {
	e := newSiteEnv(t)
	const proxyLine = "proxy_set_header Cookie $cairn_site_cookie;"
	const fastcgiLine = "fastcgi_param HTTP_COOKIE $cairn_site_cookie if_not_empty;"
	fpm := &catalog.FPM{PoolDir: "/etc/php/8.4/fpm/pool.d", SocketDir: "/run/php"}
	render := func(spec helperapi.SiteSpec) string {
		t.Helper()
		conf, _, err := e.st.render(spec, fpm)
		if err != nil {
			t.Fatal(err)
		}
		return string(conf)
	}
	tls := &helperapi.SiteTLS{Cert: "c1"}
	proxy := render(helperapi.SiteSpec{Code: "api", Type: helperapi.SiteProxy, Domains: []string{"example.com"},
		Proxy: &helperapi.ProxySpec{URL: "http://127.0.0.1:3000", WebSocket: true}, TLS: tls, Enabled: true})
	app := render(helperapi.SiteSpec{Code: "app", Type: helperapi.SiteApp, Domains: []string{"example.com"},
		App: &helperapi.AppSpec{Port: 3000, Command: "node a.js"}, TLS: tls, Enabled: true})
	php := render(helperapi.SiteSpec{Code: "blog", Type: helperapi.SitePHP, Domains: []string{"example.com"}, PHP: "8.4", TLS: tls, Enabled: true})
	static := render(helperapi.SiteSpec{Code: "files", Type: helperapi.SiteStatic, Domains: []string{"example.com"}, TLS: tls, Enabled: true})
	// Once per server block (HTTP and HTTPS) that passes requests on.
	for name, conf := range map[string]string{"proxy": proxy, "app": app} {
		if n := strings.Count(conf, "proxy_pass "); n != 2 || strings.Count(conf, proxyLine) != n {
			t.Errorf("%s site: %d proxy_pass, %d filtered Cookie headers:\n%s", name, n, strings.Count(conf, proxyLine), conf)
		}
	}
	if n := strings.Count(php, "fastcgi_pass "); n != 2 || strings.Count(php, fastcgiLine) != n {
		t.Errorf("PHP site: %d fastcgi_pass, %d filtered HTTP_COOKIE:\n%s", n, strings.Count(php, fastcgiLine), php)
	}
	// The parameter replaces the header nginx would pass by itself; it has
	// to come after the stock parameters are included.
	if i, j := strings.Index(php, "include fastcgi_params;"), strings.Index(php, fastcgiLine); i < 0 || j < i {
		t.Errorf("HTTP_COOKIE is set before fastcgi_params is included:\n%s", php)
	}
	// Nothing reads cookies in a site of files only.
	if strings.Contains(static, "cairn_site_cookie") {
		t.Errorf("static site:\n%s", static)
	}
	for _, conf := range []string{proxy, app, php, static} {
		if strings.Contains(conf, "$http_cookie") {
			t.Errorf("a site block passes the unfiltered header:\n%s", conf)
		}
	}

	// Applying a site writes the definition next to it.
	spec := helperapi.SiteSpec{Code: "api", Type: helperapi.SiteProxy, Domains: []string{"api.example.com"},
		Proxy: &helperapi.ProxySpec{URL: "http://127.0.0.1:3000"}, Enabled: true}
	if op, log := e.run(helperapi.ActionSiteApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}
	global := e.read("/etc/nginx/conf.d/cairn.conf")
	if !strings.Contains(global, "map $http_cookie $cairn_cookie_1 {") || !strings.Contains(global, " $cairn_site_cookie {") ||
		strings.Index(global, "$cairn_site_cookie {") > strings.Index(global, "include /etc/nginx/cairn/sites/*.conf;") {
		t.Fatalf("the panel-wide file does not define the filtered header before the sites:\n%s", global)
	}
	if !strings.Contains(e.read(siteConfPath("api")), proxyLine) {
		t.Fatalf("applied site:\n%s", e.read(siteConfPath("api")))
	}

	// A site from an earlier version, and the panel-wide file of that
	// version: the refresh at the helper's start rewrites both together, or
	// nginx would refuse the site's block for a variable nobody defines.
	writeSystemFile(t, e.st.Root, siteConfPath("api"), "# earlier version\nlocation / { proxy_pass http://127.0.0.1:3000; }\n")
	writeSystemFile(t, e.st.Root, "/etc/nginx/conf.d/cairn.conf", "# earlier version\ninclude /etc/nginx/cairn/sites/*.conf;\n")
	e.st.Refresh(t.Context(), slog.New(slog.DiscardHandler))
	if !strings.Contains(e.read(siteConfPath("api")), proxyLine) {
		t.Errorf("the site was not refreshed:\n%s", e.read(siteConfPath("api")))
	}
	if e.read("/etc/nginx/conf.d/cairn.conf") != global {
		t.Errorf("the panel-wide file was not refreshed with the site:\n%s", e.read("/etc/nginx/conf.d/cairn.conf"))
	}
	// A refresh nginx refuses puts both back.
	writeSystemFile(t, e.st.Root, siteConfPath("api"), "# earlier version\n")
	writeSystemFile(t, e.st.Root, "/etc/nginx/conf.d/cairn.conf", "# earlier version\n")
	e.f.failPrefix = "/usr/sbin/nginx -t"
	e.st.Refresh(t.Context(), slog.New(slog.DiscardHandler))
	if e.read(siteConfPath("api")) != "# earlier version\n" || e.read("/etc/nginx/conf.d/cairn.conf") != "# earlier version\n" {
		t.Errorf("a refused refresh left:\n%s\n%s", e.read(siteConfPath("api")), e.read("/etc/nginx/conf.d/cairn.conf"))
	}
}
