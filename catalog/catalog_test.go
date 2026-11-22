// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
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
	sw, ok := c.Find(id)
	if !ok {
		t.Fatalf("no software %s", id)
	}
	for _, o := range sw.Offers(system(t, release)) {
		if o.Line == line {
			return o
		}
	}
	t.Fatalf("%s has no line %s", id, line)
	return Offer{}
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
				if o.Method != "" {
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
		// Upstream support ended: never installed natively.
		{"php", "debian-12", "7.4", "", "", ReasonEndOfLife, false, ""},
		// ondrej does not build for Ubuntu 26.04 yet.
		{"php", "ubuntu-26.04", "8.4", "", "", ReasonNotBuilt, false, ""},
		{"php", "ubuntu-26.04", "8.5", "system", "", "", true, "php8.5-fpm,php8.5-cli"},
		// A system line stays offered after upstream support ended.
		{"php", "ubuntu-22.04", "8.1", "system", "", "", true, "php8.1-fpm,php8.1-cli"},
		// Debian has no MySQL: the official repository, recommended as the
		// newest LTS line.
		{"mysql", "debian-13", "8.4", "vendor", "mysql", "", true, "mysql-server"},
		{"postgresql", "el-10", "18", "vendor", "pgdg", "", false, "postgresql18-server"},
		{"postgresql", "debian-12", "14", "", "", ReasonEndOfLife, false, ""},
		{"nodejs", "el-10", "24", "standalone", "", "", false, "libatomic"},
		{"nodejs", "el-10", "26", "standalone", "", "", true, "libatomic"},
		{"nodejs", "debian-13", "20", "", "", ReasonEndOfLife, false, ""},
		{"valkey", "el-10", "8.1", "system", "", "", true, "valkey"},
	}
	for _, tc := range cases {
		o := offer(t, c, tc.id, tc.release, tc.line)
		if o.Method != tc.method || o.Repo != tc.repo || o.Reason != tc.reason || o.Recommended != tc.recommended ||
			strings.Join(o.Packages, ",") != tc.packages {
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
