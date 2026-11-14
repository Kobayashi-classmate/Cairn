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
	missing := map[string]bool{"redis/el-10": true, "valkey/debian-12": true, "valkey/ubuntu-22.04": true}
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
		{"nodejs", "el-10", "24", "standalone", "", "", false, ""},
		{"nodejs", "el-10", "26", "standalone", "", "", true, ""},
		{"nodejs", "debian-13", "20", "", "", ReasonEndOfLife, false, ""},
		{"valkey", "el-10", "8.0", "system", "", "", true, "valkey"},
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

func TestCatalogRejectsBadData(t *testing.T) {
	base := `id = "x"
name = "X"
category = "runtime"
lines = [{ version = "1" }]
`
	for name, files := range map[string]map[string]string{
		"unapproved repository": {
			"repos/r.toml":    "id = \"r\"\nname = \"R\"\nofficial = false\nreleases = [\"el-9\"]\n",
			"software/x.toml": base,
		},
		"unknown line": {
			"software/x.toml": base + "[[sources]]\nmethod = \"system\"\nreleases = [\"el-9\"]\nlines = [\"2\"]\npackages = [\"x\"]\n",
		},
		"repository not serving the release": {
			"repos/r.toml":    "id = \"r\"\nname = \"R\"\nofficial = true\nreleases = [\"el-9\"]\n",
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
