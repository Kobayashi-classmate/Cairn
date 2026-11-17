// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/platform"
	"github.com/Kobayashi-classmate/Cairn/internal/sources"
)

func TestParseUpdates(t *testing.T) {
	apt := `NOTE: This is only a simulation!
Inst libssl3t64 [3.5.1-1] (3.5.4-1~deb13u1 Debian-Security:13/stable-security [amd64])
Inst openssl [3.5.1-1] (3.5.4-1~deb13u1 Debian-Security:13/stable-security [amd64]) []
Inst tzdata [2025a-1] (2025b-0+deb13u1 Debian:13.2/stable [all])
Inst linux-image-6.12.63+deb13-amd64 (6.12.63-1 Debian:13.2/stable [amd64])
Inst cairn [0.0.8] (0.0.9 cairn:stable [amd64])
Conf openssl (3.5.4-1~deb13u1 Debian-Security:13/stable-security [amd64])
`
	got := parseAptSimulation(apt)
	if len(got) != 3 || got[0] != (helperapi.PackageUpdate{Name: "libssl3t64", From: "3.5.1-1", To: "3.5.4-1~deb13u1", Security: true}) ||
		got[2].Name != "tzdata" || got[2].Security {
		t.Fatalf("apt: %+v", got)
	}
	ubuntu := parseAptSimulation("Inst curl [8.5.0-2ubuntu10.4] (8.5.0-2ubuntu10.6 Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security [amd64])\n")
	if len(ubuntu) != 1 || !ubuntu[0].Security {
		t.Fatalf("ubuntu: %+v", ubuntu)
	}
	dnf := parseDNFUpgrades("Available Upgrades\nopenssl.x86_64            1:3.2.2-6.el9_5.1          baseos\ncairn.x86_64  0.0.9  cairn\nkernel.x86_64 5.14.0-503.el9 baseos\n")
	if len(dnf) != 2 || dnf[0].Name != "openssl" || dnf[0].To != "1:3.2.2-6.el9_5.1" {
		t.Fatalf("dnf: %+v", dnf)
	}
	if n := nevraName("openssl-libs-1:3.2.2-6.el9_5.1.x86_64"); n != "openssl-libs" {
		t.Fatal(n)
	}
}

func TestVersionCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"6.12.9+deb13-amd64", "6.12.10+deb13-amd64", -1},
		{"6.12.57+deb13-amd64", "6.12.57+deb13-amd64", 0},
		{"5.14.0-503.el9.x86_64", "5.14.0-427.el9.x86_64", 1},
	} {
		if got := versionCompare(c.a, c.b); (got > 0) != (c.want > 0) || (got < 0) != (c.want < 0) {
			t.Errorf("%s vs %s: %d", c.a, c.b, got)
		}
	}
}

func TestSetINI(t *testing.T) {
	conf := "[commands]\n#  upgrade_type = default\nupgrade_type = default\napply_updates = no\n\n[emitters]\nemit_via = stdio\n"
	out := setINI(setINI(conf, "commands", "upgrade_type", "security"), "commands", "apply_updates", "yes")
	if out != "[commands]\n#  upgrade_type = default\nupgrade_type = security\napply_updates = yes\n\n[emitters]\nemit_via = stdio\n" {
		t.Fatalf("got:\n%s", out)
	}
	if out := setINI("[commands]\n[base]\n", "commands", "apply_updates", "yes"); out != "[commands]\napply_updates = yes\n[base]\n" {
		t.Fatalf("insert:\n%q", out)
	}
}

func TestHostFiles(t *testing.T) {
	root := t.TempDir()
	h := &Host{Root: root}
	writeSystemFile(t, root, "/etc/fstab", "UUID=1 / ext4 defaults 0 1\n")
	if err := h.fstabSwap(true); err != nil {
		t.Fatal(err)
	}
	_ = h.fstabSwap(true)
	data, _ := os.ReadFile(filepath.Join(root, "etc/fstab"))
	if string(data) != "UUID=1 / ext4 defaults 0 1\n/swapfile none swap sw 0 0\n" {
		t.Fatalf("fstab:\n%s", data)
	}
	_ = h.fstabSwap(false)
	data, _ = os.ReadFile(filepath.Join(root, "etc/fstab"))
	if string(data) != "UUID=1 / ext4 defaults 0 1\n" {
		t.Fatalf("fstab:\n%s", data)
	}

	writeSystemFile(t, root, "/etc/hosts", "127.0.0.1\tlocalhost\n127.0.1.1\told.example.com old\n::1 localhost ip6-localhost\n")
	if err := h.renameInHosts("old.example.com", "web1.example.com"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(root, "etc/hosts"))
	if string(data) != "127.0.0.1\tlocalhost\n127.0.1.1\tweb1.example.com web1\n::1 localhost ip6-localhost\n" {
		t.Fatalf("hosts:\n%s", data)
	}
	if helperapi.ValidHostname("Web1") || helperapi.ValidHostname("-a") || !helperapi.ValidHostname("web-1.example.com") {
		t.Fatal("hostname rules")
	}
}

func TestSwitchSystemSource(t *testing.T) {
	p := newProviderServer(t)
	cat := mirrorCatalog(t, p)
	const now, older = "Sat, 14 Nov 2026 20:03:59 UTC", "Sat, 14 Nov 2026 14:03:59 UTC"
	for _, base := range []string{"/debian", "/alias/debian", "/inuse/debian"} {
		date, hash := now, "same"
		if base == "/inuse/debian" {
			date, hash = older, "stale"
		}
		for _, suite := range []string{"trixie", "trixie-updates"} {
			p.files[base+"/dists/"+suite+"/InRelease"] = release(date, hash)
			p.files[base+"/dists/"+suite+"/main/binary-amd64/Packages.xz"] = []byte(strings.Repeat("p", 900))
		}
	}
	root := t.TempDir()
	orig := "Types: deb\nURIs: " + p.URL + "/inuse/debian/\nSuites: trixie trixie-updates\nComponents: main\n"
	writeSystemFile(t, root, "/etc/apt/sources.list.d/debian.sources", orig)
	prober := &sources.Prober{HTTP: p.Client(), Sample: 512}
	s := &Software{Catalog: cat, Manager: "apt", OS: platform.OSRelease{ID: "debian", VersionID: "13", Codename: "trixie"},
		Chain: []string{"debian-13", "debian"}, Arch: "amd64", Root: root, Log: slog.New(slog.DiscardHandler), Sources: prober}
	h := &Host{StateDir: t.TempDir(), Software: s, Root: root}

	var updates []string
	failUpdate := false
	oldRun := runCommand
	t.Cleanup(func() { runCommand = oldRun })
	runCommand = func(_ context.Context, log io.Writer, name string, args ...string) error {
		updates = append(updates, name+" "+strings.Join(args, " "))
		if failUpdate {
			_, _ = io.WriteString(log, "\nE: Failed to fetch\n")
		}
		return nil
	}

	slots := h.probeSystemSources(context.Background())
	if len(slots) != 1 || slots[0].Role != "main" {
		t.Fatalf("slots %+v", slots)
	}
	var alias, inUse sources.Candidate
	for _, c := range slots[0].Candidates {
		if strings.Contains(c.URL, "/alias/") {
			alias = c
		}
		if c.InUse {
			inUse = c
		}
	}
	if _, err := h.switchSource(context.Background(), helperapi.Caller{}, helperapi.SysSourceSwitchRequest{Role: "main", Candidate: inUse.ID}, io.Discard); !hasCode(err, helperapi.CodeSysSrcNotEligible) {
		t.Fatalf("stale source in use: %v", err)
	}
	// A source that fails apt-get update is put back.
	failUpdate = true
	if _, err := h.switchSource(context.Background(), helperapi.Caller{}, helperapi.SysSourceSwitchRequest{Role: "main", Candidate: alias.ID}, io.Discard); !hasCode(err, helperapi.CodeSysSrcUpdateFailed) {
		t.Fatalf("failed update: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "etc/apt/sources.list.d/debian.sources")); string(data) != orig {
		t.Fatalf("not put back:\n%s", data)
	}
	if len(h.sourceBackups()) != 0 {
		t.Fatal("backup of a failed switch kept")
	}
	failUpdate = false
	res, err := h.switchSource(context.Background(), helperapi.Caller{}, helperapi.SysSourceSwitchRequest{Role: "main", Candidate: alias.ID}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	b := res.(helperapi.SysSourceBackup)
	data, _ := os.ReadFile(filepath.Join(root, "etc/apt/sources.list.d/debian.sources"))
	if !strings.Contains(string(data), "URIs: "+p.URL+"/alias/debian\n") || len(b.Files) != 1 || b.Files[0] != "/etc/apt/sources.list.d/debian.sources" {
		t.Fatalf("switched %+v:\n%s", b, data)
	}
	if list := h.sourceBackups(); len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("backups %+v", list)
	}
	if _, err := h.restoreSource(context.Background(), helperapi.Caller{}, helperapi.SysSourceRef{ID: b.ID}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "etc/apt/sources.list.d/debian.sources")); string(data) != orig {
		t.Fatalf("restore:\n%s", data)
	}
	if len(updates) < 3 || updates[len(updates)-1] != "apt-get update" {
		t.Fatalf("updates %v", updates)
	}
}

// Debian's cloud images name their mirror through apt's mirror+file
// method; the switch rewrites those entries too.
func TestSwitchMirrorFileSource(t *testing.T) {
	p := newProviderServer(t)
	cat := mirrorCatalog(t, p)
	const now = "Sat, 14 Nov 2026 20:03:59 UTC"
	for _, base := range []string{"/debian", "/alias/debian", "/inuse/debian"} {
		for _, suite := range []string{"trixie", "trixie-updates"} {
			p.files[base+"/dists/"+suite+"/InRelease"] = release(now, "same")
			p.files[base+"/dists/"+suite+"/main/binary-amd64/Packages.xz"] = []byte(strings.Repeat("p", 900))
		}
	}
	root := t.TempDir()
	orig := "Types: deb\nURIs: mirror+file:///etc/apt/mirrors/debian.list\nSuites: trixie trixie-updates\nComponents: main\n"
	writeSystemFile(t, root, "/etc/apt/sources.list.d/debian.sources", orig)
	writeSystemFile(t, root, "/etc/apt/mirrors/debian.list", p.URL+"/inuse/debian\n")
	s := &Software{Catalog: cat, Manager: "apt", OS: platform.OSRelease{ID: "debian", VersionID: "13", Codename: "trixie"},
		Chain: []string{"debian-13", "debian"}, Arch: "amd64", Root: root, Log: slog.New(slog.DiscardHandler),
		Sources: &sources.Prober{HTTP: p.Client(), Sample: 512}}
	h := &Host{StateDir: t.TempDir(), Software: s, Root: root}
	oldRun := runCommand
	t.Cleanup(func() { runCommand = oldRun })
	runCommand = func(context.Context, io.Writer, string, ...string) error { return nil }

	slots := h.probeSystemSources(context.Background())
	var alias string
	inUse := false
	for _, c := range slots[0].Candidates {
		if c.InUse && strings.Contains(c.URL, "/inuse/") {
			inUse = true
		}
		if strings.Contains(c.URL, "/alias/") {
			alias = c.ID
		}
	}
	if !inUse || alias == "" {
		t.Fatalf("candidates %+v", slots[0].Candidates)
	}
	if _, err := h.switchSource(context.Background(), helperapi.Caller{}, helperapi.SysSourceSwitchRequest{Role: "main", Candidate: alias}, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "etc/apt/sources.list.d/debian.sources"))
	if !strings.Contains(string(data), "URIs: "+p.URL+"/alias/debian\n") {
		t.Fatalf("not switched:\n%s", data)
	}
}
