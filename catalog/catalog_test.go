// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
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
	// EL 10 ships Valkey only; Debian 12 and Ubuntu 22.04 predate Valkey.
	// unar is in EPEL only, which the panel does not add.
	missing := map[string]bool{"redis/el-10": true, "valkey/debian-12": true, "valkey/ubuntu-22.04": true,
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
		// Upstream support ended: not installed any more, but still known
		// (where it is installed already it stays managed).
		{"php", "debian-12", "7.4", "vendor", "sury", ReasonEndOfLife, false, "php7.4-fpm,php7.4-cli"},
		// ondrej does not build for Ubuntu 26.04 yet.
		{"php", "ubuntu-26.04", "8.4", "", "", ReasonNotBuilt, false, ""},
		{"php", "ubuntu-26.04", "8.5", "system", "", "", true, "php8.5-fpm,php8.5-cli"},
		// A system line stays offered after upstream support ended.
		{"php", "ubuntu-22.04", "8.1", "system", "", "", true, "php8.1-fpm,php8.1-cli"},
		// Debian has no MySQL: the official repository, recommended as the
		// newest LTS line.
		{"mysql", "debian-13", "8.4", "vendor", "mysql", "", true, "mysql-server"},
		{"postgresql", "el-10", "18", "vendor", "pgdg", "", false, "postgresql18-server"},
		{"postgresql", "debian-12", "14", "vendor", "pgdg", ReasonEndOfLife, false, "postgresql-14"},
		{"nodejs", "el-10", "24", "standalone", "", "", false, "libatomic"},
		{"nodejs", "el-10", "26", "standalone", "", "", true, "libatomic"},
		{"nodejs", "debian-13", "20", "standalone", "", ReasonEndOfLife, false, "libatomic1"},
		{"valkey", "el-10", "8.1", "system", "", "", true, "valkey"},
	}
	for _, tc := range cases {
		o := offer(t, c, tc.id, tc.release, tc.line)
		// A line can be installed exactly when nothing speaks against it.
		if o.Method != tc.method || o.Repo != tc.repo || o.Reason != tc.reason || o.Recommended != tc.recommended ||
			strings.Join(o.Packages, ",") != tc.packages || o.Installable != (tc.reason == "") {
			t.Errorf("%s %s on %s: %+v", tc.id, tc.line, tc.release, o)
		}
	}
	if o := offer(t, c, "nginx", "el-9", "1.26"); o.Stream != "nginx:1.26" || !o.Recommended {
		t.Errorf("nginx on el-9: %+v", o)
	}
	// Runtimes are offered for the supported architectures only.
	sw, _ := c.Find("nodejs")
	sys := system(t, "debian-13")
	sys.Arch = "loong64"
	for _, o := range sw.Offers(sys) {
		if o.Method != "" {
			t.Errorf("node on loong64: %+v", o)
		}
	}
}

// The day after a line's upstream support ends, a vendor or prebuilt line
// is no longer installable, but everything that describes it where it is
// installed already (packages, services, FPM, configuration files) stays.
func TestEndOfLifeKeepsTheLineKnown(t *testing.T) {
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
			if after.Installable || !after.EndOfLife || after.Reason != ReasonEndOfLife || after.Recommended || after.EOL != tc.eol {
				t.Errorf("%s %s on %s, the day after: %+v", tc.id, tc.line, rel, after)
			}
			// Apart from that, nothing about the line changes.
			want := before
			want.Installable, want.EndOfLife, want.Reason, want.Recommended = false, true, ReasonEndOfLife, false
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
		if after.Method != MethodSystem || !after.Installable || after.EndOfLife || after.Reason != "" || !reflect.DeepEqual(before, after) {
			t.Errorf("system line %s %s on %s: before %+v, after %+v", tc.id, tc.line, tc.release, before, after)
		}
	}
}

// The recommended line is always one that can be installed, whatever the
// date: exactly one while any line is installable, none afterwards.
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
				var installable, recommended int
				for _, o := range sw.Offers(sys) {
					if o.Installable {
						installable++
					}
					if o.Installable && o.Method == "" {
						t.Errorf("%s %s on %s (%s): installable without a method", sw.ID, o.Line, rel, now.Format(time.DateOnly))
					}
					if o.Recommended {
						recommended++
						if !o.Installable {
							t.Errorf("%s %s on %s (%s): recommended but not installable: %+v", sw.ID, o.Line, rel, now.Format(time.DateOnly), o)
						}
					}
				}
				if want := min(installable, 1); recommended != want {
					t.Errorf("%s on %s (%s): %d installable, %d recommended", sw.ID, rel, now.Format(time.DateOnly), installable, recommended)
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
	base := "id = \"x\"\nname = \"X\"\ncategory = \"runtime\"\n"
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

// Where a line comes from never depends on the date: the first matching
// source describes it before and after its upstream support ends. So that
// the distribution's own packages are still the ones used for a line it
// maintains, the catalog lists a line's system source before any other
// source that serves the same release.
func TestSystemSourceComesFirst(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range platform.Names() {
		chain, err := platform.Chain(rel)
		if err != nil {
			t.Fatal(err)
		}
		for _, arch := range []string{"amd64", "arm64", "riscv64"} {
			sys := System{Chain: chain, Arch: arch}
			for _, sw := range c.Software {
				for _, l := range sw.Lines {
					other := false
					for _, src := range sw.Sources {
						if !slices.Contains(src.Lines, l.Version) || !sys.matches(src) {
							continue
						}
						if src.Method == MethodSystem && other {
							t.Errorf("%s %s on %s/%s: the system source is listed after another source", sw.ID, l.Version, rel, arch)
						}
						other = other || src.Method != MethodSystem
					}
				}
			}
		}
	}

	// A catalog that lists them the other way round keeps its first source
	// across the date all the same.
	cat, err := load(fstest.MapFS{
		"repos/r.toml": {Data: []byte("id = \"r\"\nname = \"R\"\nofficial = true\nreleases = [\"debian-13\"]\n" +
			"[apt]\nuri = \"https://r.example/\"\nsuite = \"s\"\ncomponents = [\"main\"]\n" +
			"key = { url = \"https://r.example/key\", fingerprints = [\"B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8\"] }\n")},
		"software/x.toml": {Data: []byte("id = \"x\"\nname = \"X\"\ncategory = \"runtime\"\nlines = [{ version = \"1\", eol = \"2026-06-30\" }]\n" +
			"[[sources]]\nmethod = \"vendor\"\nrepo = \"r\"\nreleases = [\"debian-13\"]\nlines = [\"1\"]\npackages = [\"x-vendor\"]\n" +
			"[[sources]]\nmethod = \"system\"\nreleases = [\"debian-13\"]\nlines = [\"1\"]\npackages = [\"x\"]\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	sys := System{Chain: []string{"debian-13", "debian"}, Arch: "amd64"}
	for day, installable := range map[string]bool{"2026-06-30": true, "2026-07-01": false} {
		sys.Now = date(t, day)
		if o := cat.Software[0].Offers(sys)[0]; o.Method != MethodVendor || o.Packages[0] != "x-vendor" || o.Installable != installable {
			t.Errorf("%s: %+v", day, o)
		}
	}
}

func TestConfigsAndExtensions(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	php, _ := c.Find("php")
	pkg := func(o Offer, ext string) string {
		for _, e := range php.Extensions {
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
			"software/x.toml": base + "[[sources]]\nmethod = \"system\"\nreleases = [\"el-9\"]\nlines = [\"1\"]\npackages = [\"x\"]\next = \"el\"\n",
		},
		"extension with an unknown family": {
			"software/x.toml": base + "[[extensions]]\nid = \"e\"\nname = \"E\"\nmodules = [\"e\"]\npackages = { apk = \"php-e\" }\n",
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
