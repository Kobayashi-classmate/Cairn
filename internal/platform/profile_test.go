// SPDX-License-Identifier: AGPL-3.0-only

package platform

import (
	"slices"
	"strings"
	"testing"
)

func osr(t *testing.T, text string) OSRelease {
	t.Helper()
	o, err := ParseOSRelease(strings.NewReader(text))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return o
}

func TestParseOSRelease(t *testing.T) {
	o := osr(t, `# comment
PRETTY_NAME="Debian GNU/Linux 13 (trixie)"
NAME='Debian GNU/Linux'
VERSION_ID="13"
ID=debian
ID_LIKE="RHEL fedora"
QUOTED="a \"b\" \$c"
`)
	if o.ID != "debian" || o.VersionID != "13" || o.PrettyName != "Debian GNU/Linux 13 (trixie)" || o.Name != "Debian GNU/Linux" {
		t.Fatalf("unexpected parse result: %+v", o)
	}
	if len(o.IDLike) != 2 || o.IDLike[0] != "rhel" {
		t.Fatalf("ID_LIKE not lower-cased and split: %v", o.IDLike)
	}
	if _, err := ParseOSRelease(strings.NewReader(`ID="broken`)); err == nil {
		t.Fatal("expected error for unterminated quote")
	}
	if o := osr(t, "VERSION_ID=1\n"); o.ID != "linux" {
		t.Fatalf("ID should default to linux, got %q", o.ID)
	}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		name        string
		release     string
		wantProfile string
		wantTier    int
		wantFamily  string
		unsupported bool
	}{
		{"debian 12", "ID=debian\nVERSION_ID=12", "debian-12", Tier1, "debian", false},
		{"debian 13", "ID=debian\nVERSION_ID=\"13\"", "debian-13", Tier1, "debian", false},
		{"debian 14 is unverified but allowed", "ID=debian\nVERSION_ID=14", "debian", TierUnverified, "debian", false},
		{"debian 11 is too old", "ID=debian\nVERSION_ID=11", "", 0, "", true},
		{"ubuntu 22.04", "ID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"22.04\"", "ubuntu-22.04", Tier1, "debian", false},
		{"ubuntu 24.04", "ID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"24.04\"", "ubuntu-24.04", Tier1, "debian", false},
		{"ubuntu 26.04", "ID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"26.04\"", "ubuntu-26.04", Tier1, "debian", false},
		{"ubuntu 20.04 is too old", "ID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"20.04\"", "", 0, "", true},
		{"rocky 9.4", "ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\nVERSION_ID=\"9.4\"", "el-9", Tier1, "el", false},
		{"alma 10.0", "ID=\"almalinux\"\nID_LIKE=\"rhel centos fedora\"\nVERSION_ID=\"10.0\"", "el-10", Tier1, "el", false},
		{"rhel 8.10", "ID=\"rhel\"\nID_LIKE=\"fedora\"\nVERSION_ID=\"8.10\"", "el-8", Tier2, "el", false},
		{"centos stream 9", "ID=\"centos\"\nID_LIKE=\"rhel fedora\"\nVERSION_ID=\"9\"", "centos-9", Tier2, "el", false},
		{"centos 8 is end of life", "ID=\"centos\"\nID_LIKE=\"rhel fedora\"\nVERSION_ID=\"8\"", "", 0, "", true},
		{"centos 7 is too old", "ID=\"centos\"\nID_LIKE=\"rhel fedora\"\nVERSION_ID=\"7\"", "", 0, "", true},
		{"mint falls back to ubuntu, unverified", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"22\"", "ubuntu", TierUnverified, "debian", false},
		{"alpine is unknown", "ID=alpine\nVERSION_ID=3.20.0", "", 0, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Resolve(osr(t, tc.release))
			if tc.unsupported {
				if !IsUnsupported(err) {
					t.Fatalf("want UnsupportedError, got profile=%v err=%v", p, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Name != tc.wantProfile || p.Tier != tc.wantTier || p.Family != tc.wantFamily {
				t.Fatalf("got name=%s tier=%d family=%s, want %s/%d/%s",
					p.Name, p.Tier, p.Family, tc.wantProfile, tc.wantTier, tc.wantFamily)
			}
		})
	}
}

func TestInheritanceMergesNestedTables(t *testing.T) {
	p, err := Load("centos-9")
	if err != nil {
		t.Fatal(err)
	}
	if p.PackageManager != "dnf" || p.MAC != "selinux" || p.Tier != Tier2 {
		t.Fatalf("inherited fields missing: %+v", p)
	}
	if unit, ok := p.Service("ssh"); !ok || unit != "sshd" {
		t.Fatalf("services table not inherited: %v", p.Services)
	}
	u, err := Load("ubuntu-24.04")
	if err != nil {
		t.Fatal(err)
	}
	if u.Firewall != "ufw" || u.WebUser != "www-data" {
		t.Fatalf("ubuntu overrides or debian base missing: %+v", u)
	}
}

func TestAllProfilesLoad(t *testing.T) {
	entries, err := profileFS.ReadDir("profiles")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".toml")
		p, err := Load(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if p.Unsupported == "" && (p.Family == "" || p.PackageManager == "") {
			t.Errorf("%s: family and package_manager must be set", name)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"24.04", "22.04", 1}, {"9", "9.0", 0}, {"8.10", "8.9", 1}, {"12", "13", -1}} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestChain(t *testing.T) {
	got, err := Chain("el-9")
	if err != nil || strings.Join(got, ",") != "el-9,el" {
		t.Fatalf("chain: %v %v", got, err)
	}
	if _, err := Chain("nope"); err == nil {
		t.Fatal("unknown profile accepted")
	}
	if !slices.Contains(Names(), "ubuntu-26.04") {
		t.Fatalf("names: %v", Names())
	}
}
