// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/platform"
)

// tier1 are the releases every entry is checked against.
var tier1 = []string{"debian-12", "debian-13", "ubuntu-22.04", "ubuntu-24.04", "ubuntu-26.04", "el-9", "el-10"}

// day is the date the data was last checked; offers depend on it.
var day = time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)

func system(t *testing.T, release string) System {
	t.Helper()
	chain, err := platform.Chain(release)
	if err != nil {
		t.Fatal(err)
	}
	return System{Chain: chain, Arch: "amd64", Now: day}
}

func offer(t *testing.T, c *Catalog, id, release, line string) Offer {
	t.Helper()
	return offerOn(t, c, id, release, line, day)
}

// offerOn is the offer of a line as of a date.
func offerOn(t *testing.T, c *Catalog, id, release, line string, now time.Time) Offer {
	t.Helper()
	sw, ok := c.Find(id)
	if !ok {
		t.Fatalf("no software %s", id)
	}
	sys := system(t, release)
	sys.Now = now
	for _, o := range sw.Offers(sys) {
		if o.Line == line {
			return o
		}
	}
	t.Fatalf("%s has no line %s", id, line)
	return Offer{}
}

func date(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCatalogLoads(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Software) < 12 {
		t.Fatalf("only %d entries", len(c.Software))
	}
	if c.Index.Format != Format || c.Index.Serial <= 0 {
		t.Errorf("index: %+v", c.Index)
	}
	// What the panel looks up by capability is there, once each where the
	// code expects one.
	for cap, want := range map[string]int{CapWebServer: 1, CapPHPFPM: 1, CapMySQL: 2, CapPostgreSQL: 1, CapRedis: 2} {
		if got := len(c.Providing(cap)); got != want {
			t.Errorf("%d entries provide %s, want %d", got, cap, want)
		}
	}
	known := platform.Names()
	for _, sw := range c.Software {
		for _, src := range sw.Sources {
			for _, rel := range src.Releases {
				if rel != "*" && !slices.Contains(known, rel) {
					t.Errorf("%s: unknown release %s", sw.ID, rel)
				}
			}
		}
	}
	for _, r := range c.Repos {
		for _, req := range r.Requires {
			if _, ok := c.Repos[req]; !ok {
				t.Errorf("repository %s requires unknown %s", r.ID, req)
			}
		}
	}
}

// Every entry offers something on every tier-1 release, with exactly one
// recommended line, except where the release itself lacks the software.
func TestEveryReleaseIsCovered(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// Debian 12 and Ubuntu 22.04 predate Valkey, which has no repository of
	// its own.
	// unar is in EPEL only, which the panel does not add.
	missing := map[string]bool{"valkey/debian-12": true, "valkey/ubuntu-22.04": true,
		"unar/el-9": true, "unar/el-10": true}
	for _, sw := range c.Software {
		for _, rel := range tier1 {
			offers := sw.Offers(system(t, rel))
			var available, recommended int
			for _, o := range offers {
				if o.Installable {
					available++
				}
				if o.Recommended {
					recommended++
				}
			}
			key := sw.ID + "/" + rel
			switch {
			case missing[key] && available == 0:
			case missing[key]:
				t.Errorf("%s: expected nothing, got %d lines", key, available)
			case available == 0 || recommended != 1:
				t.Errorf("%s: %d available, %d recommended", key, available, recommended)
			}
		}
	}
}

func TestSourceRules(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// ended marks a line past its upstream support: installable all the
	// same, with no reason given, but never recommended.
	const ended = "ended"
	cases := []struct {
		id, release, line, method, repo, reason string
		recommended                             bool
		packages                                string
	}{
		// The system version is recommended; other lines come from the
		// approved PHP repositories.
		{"php", "debian-13", "8.4", "system", "", "", true, "php8.4-fpm,php8.4-cli"},
		{"php", "debian-13", "8.3", "vendor", "sury", "", false, "php8.3-fpm,php8.3-cli"},
		{"php", "el-9", "8.3", "system", "", "", true, "php-fpm,php-cli"},
		{"php", "el-9", "8.2", "vendor", "remi", "", false, "php82-php-fpm,php82-php-cli"},
		// Upstream support ended: still installable, with a warning.
		{"php", "debian-12", "7.4", "vendor", "sury", ended, false, "php7.4-fpm,php7.4-cli"},
		// ondrej does not build for Ubuntu 26.04 yet.
		{"php", "ubuntu-26.04", "8.4", "", "", ReasonNotBuilt, false, ""},
		{"php", "ubuntu-26.04", "8.5", "system", "", "", true, "php8.5-fpm,php8.5-cli"},
		// A system line stays offered after upstream support ended.
		{"php", "ubuntu-22.04", "8.1", "system", "", "", true, "php8.1-fpm,php8.1-cli"},
		// Debian has no MySQL: the official repository, whose newest LTS
		// line is recommended.
		{"mysql", "debian-13", "9.7", "vendor", "mysql", "", true, "mysql-server"},
		{"mysql", "debian-13", "8.4", "vendor", "mysql", "", false, "mysql-server"},
		// Both have the line: the system's is the default.
		{"mysql", "ubuntu-26.04", "8.4", "system", "", "", false, "mysql-server"},
		{"mariadb", "debian-12", "10.11", "system", "", "", false, "mariadb-server"},
		{"mariadb", "debian-12", "12.3", "vendor", "mariadb", "", true, "mariadb-server"},
		{"mariadb", "el-10", "12.3", "vendor", "mariadb", "", true, "MariaDB-server"},
		{"redis", "el-10", "8.12", "vendor", "redis", "", true, "redis"},
		{"redis", "debian-13", "8.10", "vendor", "redis", "", false, "redis-server,redis-tools"},
		{"redis", "debian-13", "8.0", "system", "", "", false, "redis-server"},
		// nginx.org's stable branch is recommended, never the mainline.
		{"nginx", "debian-13", "1.30", "vendor", "nginx", "", true, "nginx"},
		{"nginx", "debian-13", "1.31", "vendor", "nginx-mainline", "", false, "nginx"},
		{"nginx", "debian-13", "1.26", "system", "", "", false, "nginx,libnginx-mod-stream"},
		// Upstream's newest line is recommended where the system has an
		// older one.
		{"postgresql", "el-10", "19", "vendor", "pgdg", "", true, "postgresql19-server"},
		{"postgresql", "el-10", "16", "system", "", "", false, "postgresql-server"},
		{"postgresql", "debian-12", "14", "vendor", "pgdg", ended, false, "postgresql-14"},
		{"nodejs", "el-10", "24", "standalone", "", "", false, "libatomic"},
		{"nodejs", "el-10", "26", "standalone", "", "", true, "libatomic"},
		{"nodejs", "debian-13", "20", "standalone", "", ended, false, "libatomic1"},
		{"valkey", "el-10", "8.1", "system", "", "", true, "valkey"},
	}
	for _, tc := range cases {
		o := offer(t, c, tc.id, tc.release, tc.line)
		reason := tc.reason
		if reason == ended {
			reason = ""
		}
		// A line can be installed exactly when somebody builds it here.
		if o.Method != tc.method || o.Repo != tc.repo || o.Reason != reason || o.Recommended != tc.recommended ||
			strings.Join(o.Packages, ",") != tc.packages || o.Installable != (tc.reason != ReasonNotBuilt) ||
			o.EndOfLife != (tc.reason == ended) {
			t.Errorf("%s %s on %s: %+v", tc.id, tc.line, tc.release, o)
		}
	}
	// PGDG no longer serves 13 on EL.
	if o := offer(t, c, "postgresql", "el-10", "13"); o.Installable || o.Reason != ReasonNotBuilt {
		t.Errorf("postgresql 13 on el-10: %+v", o)
	}
	// The MySQL repository builds for amd64 only.
	sys := system(t, "debian-13")
	sys.Arch = "arm64"
	mysql, _ := c.Find("mysql")
	if o, _ := mysql.OfferFrom(sys, "8.4", ""); o.Installable || o.Reason != ReasonArch {
		t.Errorf("mysql on arm64: %+v", o)
	}
	if o := offer(t, c, "nginx", "el-9", "1.26"); o.Stream != "nginx:1.26" || o.Recommended {
		t.Errorf("nginx on el-9: %+v", o)
	}
	// Runtimes are offered for the supported architectures only.
	sw, _ := c.Find("nodejs")
	sys = system(t, "debian-13")
	sys.Arch = "loong64"
	for _, o := range sw.Offers(sys) {
		if o.Method != "" {
			t.Errorf("node on loong64: %+v", o)
		}
	}
}

// The day after a line's upstream support ends, a vendor or prebuilt line
// is marked as ended and loses its recommendation; it stays installable,
// and everything that describes it (packages, services, FPM, configuration
// files) stays as it was.
func TestEndOfLifeKeepsTheLineInstallable(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, line, eol, method string
		releases              []string
	}{
		{"php", "8.2", "2026-12-31", MethodVendor, []string{"debian-13", "ubuntu-22.04", "ubuntu-24.04", "el-9", "el-10"}},
		{"postgresql", "15", "2027-11-11", MethodVendor, []string{"debian-13", "ubuntu-24.04", "el-10"}},
		{"nodejs", "22", "2027-04-30", MethodStandalone, tier1},
		{"python", "3.11", "2027-10-31", MethodStandalone, []string{"debian-13", "el-9"}},
	} {
		lastDay := date(t, tc.eol)
		for _, rel := range tc.releases {
			before := offerOn(t, c, tc.id, rel, tc.line, lastDay)
			after := offerOn(t, c, tc.id, rel, tc.line, lastDay.AddDate(0, 0, 1))
			if before.Method != tc.method || !before.Installable || before.EndOfLife || before.Reason != "" {
				t.Errorf("%s %s on %s, last supported day: %+v", tc.id, tc.line, rel, before)
			}
			if !after.Installable || !after.EndOfLife || after.Reason != "" || after.Recommended || after.EOL != tc.eol {
				t.Errorf("%s %s on %s, the day after: %+v", tc.id, tc.line, rel, after)
			}
			// Apart from that, nothing about the line changes.
			want := before
			want.EndOfLife, want.Recommended = true, false
			want.Sources = endedElsewhere(before.Sources)
			if !reflect.DeepEqual(after, want) {
				t.Errorf("%s %s on %s: the offer changed with the date:\n before %+v\n after  %+v", tc.id, tc.line, rel, before, after)
			}
		}
	}
	// PHP keeps what sites and the line's settings need.
	o := offerOn(t, c, "php", "debian-13", "8.2", date(t, "2027-01-01"))
	if o.Repo != "sury" || strings.Join(o.Packages, ",") != "php8.2-fpm,php8.2-cli" || strings.Join(o.Services, ",") != "php8.2-fpm" ||
		o.FPM == nil || o.FPM.PoolDir != "/etc/php/8.2/fpm/pool.d" || o.CLI != "/usr/bin/php8.2" || !o.Extensions || len(o.Configs) != 3 {
		t.Errorf("php 8.2 on debian-13 after its end of life: %+v", o)
	}
	// The distribution maintains its own line: nothing changes for it.
	for _, tc := range []struct{ id, release, line, eol string }{
		{"php", "debian-12", "8.2", "2026-12-31"},
		{"postgresql", "debian-12", "15", "2027-11-11"},
		{"postgresql", "el-9", "15", "2027-11-11"},
	} {
		lastDay := date(t, tc.eol)
		before := offerOn(t, c, tc.id, tc.release, tc.line, lastDay)
		after := offerOn(t, c, tc.id, tc.release, tc.line, lastDay.AddDate(0, 0, 1))
		before.Sources = endedElsewhere(before.Sources)
		if after.Method != MethodSystem || !after.Installable || after.EndOfLife || after.Reason != "" || !reflect.DeepEqual(before, after) {
			t.Errorf("system line %s %s on %s: before %+v, after %+v", tc.id, tc.line, tc.release, before, after)
		}
	}
}

// endedElsewhere is sources as they are once upstream support has ended:
// only the system's still gets updates.
func endedElsewhere(sources []SourceInfo) []SourceInfo {
	out := slices.Clone(sources)
	for i := range out {
		out[i].EndOfLife = out[i].Method != MethodSystem
	}
	return out
}

// The recommended line is always one that can be installed and is still
// supported upstream, whatever the date: exactly one while any line is,
// none afterwards.
func TestRecommendedLineIsInstallable(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	dates := []time.Time{day, date(t, "2100-01-01")}
	for _, sw := range c.Software {
		for _, l := range sw.Lines {
			if l.EOL != "" {
				dates = append(dates, date(t, l.EOL).AddDate(0, 0, 1))
			}
		}
	}
	for i := range c.Software {
		sw := &c.Software[i]
		for _, rel := range tier1 {
			for _, now := range dates {
				sys := system(t, rel)
				sys.Now = now
				var supported, recommended int
				for _, o := range sw.Offers(sys) {
					if o.Installable && !o.EndOfLife {
						supported++
					}
					if o.Installable && o.Method == "" {
						t.Errorf("%s %s on %s (%s): installable without a method", sw.ID, o.Line, rel, now.Format(time.DateOnly))
					}
					if o.Recommended {
						recommended++
						if !o.Installable || o.EndOfLife {
							t.Errorf("%s %s on %s (%s): recommended but not installable or ended: %+v", sw.ID, o.Line, rel, now.Format(time.DateOnly), o)
						}
					}
				}
				if want := min(supported, 1); recommended != want {
					t.Errorf("%s on %s (%s): %d supported, %d recommended", sw.ID, rel, now.Format(time.DateOnly), supported, recommended)
				}
			}
		}
	}
	// Once every Node.js line has ended, the wizard's suites offer none.
	sys := system(t, "debian-13")
	sys.Now = date(t, "2100-01-01")
	for _, su := range c.Suites {
		for _, item := range c.Resolve(su, sys) {
			if item.Software == "nodejs" {
				t.Errorf("suite %s still offers Node.js %s", su.ID, item.Line)
			}
		}
	}

	// An ended long-term line is not preferred over a newer supported one,
	// and an ended line is no fallback either.
	base := "id = \"x\"\nname = \"X\"\ncategory = \"runtime\"\nprovides = [\"x\"]\n"
	source := "[[sources]]\nmethod = \"standalone\"\nprovider = \"x\"\nreleases = [\"*\"]\nlines = [\"3\", \"2\"]\n"
	for name, tc := range map[string]struct{ lines, want string }{
		"ended LTS, newer line supported": {`lines = [{ version = "3", eol = "2030-01-01" }, { version = "2", lts = true, eol = "2026-01-01" }]`, "3"},
		"only the older line supported":   {`lines = [{ version = "3", eol = "2026-01-01" }, { version = "2", eol = "2030-01-01" }]`, "2"},
		"every line ended":                {`lines = [{ version = "3", eol = "2026-01-01" }, { version = "2", lts = true, eol = "2025-01-01" }]`, ""},
	} {
		cat, err := load(fstest.MapFS{"software/x.toml": {Data: []byte(base + tc.lines + "\n" + source)}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := ""
		for _, o := range cat.Software[0].Offers(System{Chain: []string{"debian-13", "debian"}, Arch: "amd64", Now: day}) {
			if o.Method != MethodStandalone {
				t.Errorf("%s: line %s lost its method: %+v", name, o.Line, o)
			}
			if o.Recommended {
				got += o.Line
			}
		}
		if got != tc.want {
			t.Errorf("%s: recommended %q, want %q", name, got, tc.want)
		}
	}
}

// The system repositories' source is a line's default, wherever the
// catalog lists it; the other sources stay offered beside it, and a vendor
// source whose updates for the line have ended is offered last.
func TestSystemSourceIsTheDefault(t *testing.T) {
	cat, err := load(fstest.MapFS{
		"repos/r.toml": {Data: []byte("id = \"r\"\nname = \"R\"\nofficial = true\nreleases = [\"debian-13\"]\n" +
			"[apt]\nuri = \"https://r.example/\"\nsuite = \"s\"\ncomponents = [\"main\"]\n" +
			"key = { url = \"https://r.example/key\", fingerprints = [\"B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8\"] }\n")},
		"repos/q.toml": {Data: []byte("id = \"q\"\nname = \"Q\"\nofficial = true\nreleases = [\"debian-13\"]\n" +
			"[apt]\nuri = \"https://q.example/\"\nsuite = \"s\"\ncomponents = [\"main\"]\n" +
			"key = { url = \"https://q.example/key\", fingerprints = [\"B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8\"] }\n")},
		"software/x.toml": {Data: []byte("id = \"x\"\nname = \"X\"\ncategory = \"runtime\"\nprovides = [\"x\"]\n" +
			"lines = [{ version = \"2\", eol = \"2026-06-30\" }, { version = \"1\", eol = \"2026-06-30\" }]\n" +
			"[[sources]]\nmethod = \"vendor\"\nrepo = \"r\"\nreleases = [\"debian-13\"]\nlines = [\"2\", \"1\"]\npackages = [\"x-vendor\"]\n" +
			"[[sources]]\nmethod = \"system\"\nreleases = [\"debian-13\"]\nlines = [\"1\"]\npackages = [\"x\"]\n" +
			"[[sources]]\nmethod = \"vendor\"\nrepo = \"q\"\nreleases = [\"debian-13\"]\nlines = [\"2\"]\npackages = [\"x-q\"]\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	sw := &cat.Software[0]
	sys := System{Chain: []string{"debian-13", "debian"}, Arch: "amd64"}
	origins := func(o Offer) string {
		var s []string
		for _, src := range o.Sources {
			s = append(s, fmt.Sprintf("%s:%v:%v", src.Origin, src.EndOfLife, src.Default))
		}
		return strings.Join(s, " ")
	}
	for day, ended := range map[string]bool{"2026-06-30": false, "2026-07-01": true} {
		sys.Now = date(t, day)
		offers := sw.Offers(sys)
		two, one := offers[0], offers[1]
		if one.Origin != OriginSystem || one.Packages[0] != "x" || !one.Installable || one.EndOfLife ||
			origins(one) != fmt.Sprintf("system:false:true r:%v:false", ended) {
			t.Errorf("%s: line 1: %+v", day, one)
		}
		if two.Origin != "r" || two.EndOfLife != ended || origins(two) != fmt.Sprintf("r:%v:true q:%v:false", ended, ended) {
			t.Errorf("%s: line 2: %+v", day, two)
		}
		// Another source of the line, asked for by its origin.
		o, ok := sw.OfferFrom(sys, "1", "r")
		if !ok || o.Origin != "r" || o.Packages[0] != "x-vendor" || o.EndOfLife != ended || o.Sources[0].Origin != OriginSystem {
			t.Errorf("%s: line 1 from r: %v %+v", day, ok, o)
		}
		if _, ok := sw.OfferFrom(sys, "1", "q"); ok {
			t.Errorf("%s: line 1 offered from q", day)
		}
	}
	// Built for another architecture only.
	sys.Arch = "arm64"
	cat.Software[0].Sources[1].Arch = []string{"amd64"}
	if o, _ := sw.OfferFrom(sys, "1", ""); o.Origin != "r" {
		t.Errorf("arm64: %+v", o)
	}
	for i := range cat.Software[0].Sources {
		cat.Software[0].Sources[i].Arch = []string{"amd64"}
	}
	if o, _ := sw.OfferFrom(sys, "1", ""); o.Installable || o.Reason != ReasonArch {
		t.Errorf("arm64 without a build: %+v", o)
	}
}

func TestConfigsAndExtensions(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	php, _ := c.Find("php")
	pkg := func(o Offer, ext string) string {
		for _, e := range php.Extensions() {
			if e.ID == ext {
				return e.ExtensionPackage(o)
			}
		}
		t.Fatalf("no extension %s", ext)
		return ""
	}
	cases := []struct {
		release, line, redis, curl, ini, fpm string
	}{
		{"debian-13", "8.4", "php8.4-redis", "php8.4-curl", "/etc/php/8.4/fpm/php.ini", "php8.4-fpm"},
		{"debian-13", "8.3", "php8.3-redis", "php8.3-curl", "/etc/php/8.3/fpm/php.ini", "php8.3-fpm"},
		{"ubuntu-24.04", "8.2", "php8.2-redis", "php8.2-curl", "/etc/php/8.2/fpm/php.ini", "php8.2-fpm"},
		{"el-9", "8.3", "php-pecl-redis6", "", "/etc/php.ini", "php-fpm"},
		{"el-10", "8.4", "php84-php-pecl-redis6", "", "/etc/opt/remi/php84/php.ini", "php84-php-fpm"},
	}
	for _, tc := range cases {
		o := offer(t, c, "php", tc.release, tc.line)
		if !o.Extensions || pkg(o, "redis") != tc.redis || pkg(o, "curl") != tc.curl {
			t.Errorf("php %s on %s: extensions %v redis %q curl %q", tc.line, tc.release, o.Extensions, pkg(o, "redis"), pkg(o, "curl"))
		}
		if len(o.Configs) < 2 || o.Configs[0].ID != "php.ini" || o.Configs[0].Path != tc.ini || o.Configs[0].Service != tc.fpm ||
			o.Configs[0].Apply != ApplyReload || !o.Configs[0].HasCheck || o.Configs[0].Check[0] != o.FPM.Binary {
			t.Errorf("php %s on %s: configs %+v", tc.line, tc.release, o.Configs)
		}
	}
	// EL's own PHP has no imagick (EPEL only); Remi has it.
	if pkg(offer(t, c, "php", "el-9", "8.3"), "imagick") != "" || pkg(offer(t, c, "php", "el-9", "8.2"), "imagick") != "php82-php-pecl-imagick" {
		t.Error("imagick on EL")
	}
	for _, tc := range []struct{ id, release, line, config, path, apply, service string }{
		{"nginx", "el-9", "1.26", "nginx.conf", "/etc/nginx/nginx.conf", ApplyReload, "nginx"},
		{"mariadb", "debian-13", "11.8", "50-server.cnf", "/etc/mysql/mariadb.conf.d/50-server.cnf", ApplyRestart, "mariadb"},
		{"mariadb", "el-10", "10.11", "mariadb-server.cnf", "/etc/my.cnf.d/mariadb-server.cnf", ApplyRestart, "mariadb"},
		{"mysql", "el-9", "8.4", "mysql-server.cnf", "/etc/my.cnf.d/mysql-server.cnf", ApplyRestart, "mysqld"},
		{"postgresql", "debian-13", "18", "postgresql.conf", "/etc/postgresql/18/main/postgresql.conf", ApplyReload, "postgresql"},
		{"redis", "ubuntu-24.04", "7.0", "redis.conf", "/etc/redis/redis.conf", ApplyRestart, "redis-server"},
		{"valkey", "el-10", "8.1", "valkey.conf", "/etc/valkey/valkey.conf", ApplyRestart, "valkey"},
	} {
		o := offer(t, c, tc.id, tc.release, tc.line)
		if len(o.Configs) != 1 || o.Configs[0].ID != tc.config || o.Configs[0].Path != tc.path || o.Configs[0].Apply != tc.apply || o.Configs[0].Service != tc.service {
			t.Errorf("%s %s on %s: %+v", tc.id, tc.line, tc.release, o.Configs)
		}
	}
	// PostgreSQL on EL has no managed service yet, so nothing to edit.
	if o := offer(t, c, "postgresql", "el-10", "18"); len(o.Configs) != 0 || o.Extensions {
		t.Errorf("postgresql on el-10: %+v", o.Configs)
	}
	if o := offer(t, c, "nodejs", "debian-13", "24"); len(o.Configs) != 0 || o.Extensions {
		t.Errorf("nodejs: %+v", o)
	}
}

func TestCatalogRejectsBadData(t *testing.T) {
	base := `id = "x"
name = "X"
category = "runtime"
provides = ["x"]
lines = [{ version = "1" }]
`
	for name, files := range map[string]map[string]string{
		"unapproved repository": {
			"repos/r.toml":    "id = \"r\"\nname = \"R\"\nofficial = false\nreleases = [\"el-9\"]\n[dnf]\nrelease_package = \"r-release\"\n",
			"software/x.toml": base,
		},
		"unknown line": {
			"software/x.toml": base + "[[sources]]\nmethod = \"system\"\nreleases = [\"el-9\"]\nlines = [\"2\"]\npackages = [\"x\"]\n",
		},
		"repository not serving the release": {
			"repos/r.toml":    "id = \"r\"\nname = \"R\"\nofficial = true\nreleases = [\"el-9\"]\n[dnf]\nrelease_package = \"r-release\"\n",
			"software/x.toml": base + "[[sources]]\nmethod = \"vendor\"\nrepo = \"r\"\nreleases = [\"el-10\"]\nlines = [\"1\"]\npackages = [\"x\"]\n",
		},
		"unknown placeholder": {
			"software/x.toml": base + "[[sources]]\nmethod = \"system\"\nreleases = [\"el-9\"]\nlines = [\"1\"]\npackages = [\"x{arch}\"]\n",
		},
		"unknown field": {
			"software/x.toml": base + "script = \"rm -rf /\"\n",
		},
		"file name mismatch": {
			"software/y.toml": base,
		},
		"key over http": {
			"repos/r.toml":    "id = \"r\"\nname = \"R\"\nofficial = true\nreleases = [\"el-9\"]\n[dnf]\nbaseurl = \"https://r.example/\"\nkey = { url = \"http://r.example/key\", fingerprints = [\"B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8\"] }\n",
			"software/x.toml": base,
		},
		"bad fingerprint": {
			"repos/r.toml":    "id = \"r\"\nname = \"R\"\nofficial = true\nreleases = [\"el-9\"]\n[dnf]\nbaseurl = \"https://r.example/\"\nkey = { url = \"https://r.example/key\", fingerprints = [\"ACCC4CF8\"] }\n",
			"software/x.toml": base,
		},
		"config without a service to reload": {
			"software/x.toml": base + "[[sources]]\nmethod = \"system\"\nreleases = [\"el-9\"]\nlines = [\"1\"]\npackages = [\"x\"]\n" +
				"configs = [{ id = \"x.conf\", path = \"/etc/x.conf\", apply = \"reload\" }]\n",
		},
		"config with a relative check": {
			"software/x.toml": base + "[[sources]]\nmethod = \"system\"\nreleases = [\"el-9\"]\nlines = [\"1\"]\npackages = [\"x\"]\nservices = [\"x\"]\n" +
				"configs = [{ id = \"x.conf\", path = \"/etc/x.conf\", check = [\"x\", \"-t\"], apply = \"reload\" }]\n",
		},
		"extension family without extensions": {
			"software/x.toml": base + "[[sources]]\nmethod = \"system\"\nreleases = [\"el-9\"]\nlines = [\"1\"]\npackages = [\"x\"]\n[sources.php]\next = \"el\"\n",
		},
		"unknown recommendation rule": {
			"software/x.toml": base + "recommend = \"newest\"\n",
		},
		"conflict with unknown software": {
			"software/x.toml": base + "conflicts = [\"y\"]\n",
		},
		"repository named like the system": {
			"repos/system.toml": "id = \"system\"\nname = \"S\"\nofficial = true\nreleases = [\"el-9\"]\n[dnf]\nrelease_package = \"s-release\"\n",
			"software/x.toml":   base,
		},
		"no capability": {
			"software/x.toml": strings.Replace(base, "provides = [\"x\"]\n", "", 1),
		},
		"unknown index format": {
			"index.toml":      "format = 2\nserial = 1\n",
			"software/x.toml": base,
		},
		"index without a serial": {
			"index.toml":      "format = 1\n",
			"software/x.toml": base,
		},
		"php part without fpm": {
			"software/x.toml": base + "[[sources]]\nmethod = \"system\"\nreleases = [\"el-9\"]\nlines = [\"1\"]\npackages = [\"x\"]\n[sources.php]\ncli = \"/usr/bin/php\"\n",
		},
		"extension with an unknown family": {
			"software/x.toml": base + "[[php.extensions]]\nid = \"e\"\nname = \"E\"\nmodules = [\"e\"]\npackages = { apk = \"php-e\" }\n",
		},
		"repository without install details": {
			"repos/r.toml":    "id = \"r\"\nname = \"R\"\nofficial = true\nreleases = [\"el-9\"]\n",
			"software/x.toml": base,
		},
	} {
		fsys := fstest.MapFS{}
		for p, content := range files {
			fsys[p] = &fstest.MapFile{Data: []byte(content)}
		}
		if _, err := load(fsys); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
