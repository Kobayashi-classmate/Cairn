// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/platform"
	"github.com/Kobayashi-classmate/Cairn/internal/sources"
)

// mirrorCatalog is a catalog whose mirrors point at the test server: an
// approved operator "mirr" serving Node.js builds under /mirror, and a
// Debian system entry with an official address and one country alias.
func mirrorCatalog(t *testing.T, p *providerServer) *catalog.Catalog {
	t.Helper()
	files := fstest.MapFS{
		"software/nodejs.toml": {Data: []byte(`id = "nodejs"
name = "Node.js"
category = "runtime"
provides = ["x"]
lines = [{ version = "24", lts = true }]
[[sources]]
method = "standalone"
provider = "nodejs"
releases = ["*"]
lines = ["24"]
`)},
		"software/nginx.toml": {Data: []byte(`id = "nginx"
name = "nginx"
category = "web"
provides = ["x"]
lines = [{ version = "1.26" }]
[[sources]]
method = "system"
releases = ["debian-13"]
lines = ["1.26"]
packages = ["nginx"]
`)},
		"mirrors.toml": {Data: []byte(fmt.Sprintf(`[[operator]]
id = "mirr"
name = "Test mirror"
homepage = "https://mirror.example/"
approved = "2026-11-14"

[[rewrite]]
provider = "nodejs"
operator = "mirr"
pattern = '^%s/node/(.*)$'
url = '%s/mirror/node/$1'

[[system]]
distro = "debian"
role = "main"
official = "%s/debian"
suites = ["{codename}", "{codename}-updates"]
mirrors = ["%s/alias/debian"]
`, regexp.QuoteMeta(p.URL), p.URL, p.URL, p.URL))},
	}
	cat, err := catalog.LoadFS(files)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func readManifest(t *testing.T, r *Runtimes, sw, version string) manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.Dir, sw, version+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRuntimeInstallFromMirror(t *testing.T) {
	p := newProviderServer(t)
	cat := mirrorCatalog(t, p)
	archive := nodeArchive(t, "24.22.3")
	p.node(t, "24.22.3", archive)
	p.files["/mirror/node/v24.22.3/node-v24.22.3-linux-x64.tar.gz"] = archive
	fakeVersion(t, "v24.22.3\n")
	newRuntimes := func() *Runtimes {
		r := p.runtimes(t)
		r.Mirrors, r.Prober = &cat.Mirrors, &sources.Prober{HTTP: p.Client(), Sample: 512}
		return r
	}

	// The chosen mirror is used and recorded.
	r := newRuntimes()
	var log strings.Builder
	if _, err := r.Install(context.Background(), &log, "nodejs", "nodejs", "24", "mirr", ""); err != nil {
		t.Fatalf("install from mirror: %v\n%s", err, log.String())
	}
	if m := readManifest(t, r, "nodejs", "24.22.3"); m.Source != "mirr" || !strings.Contains(m.From, "/mirror/node/") || !strings.Contains(m.URL, "/node/v24") {
		t.Fatalf("manifest: %+v", m)
	}

	// A mirror serving another file fails the official checksum; the
	// install falls back to the next source and says so.
	p.files["/mirror/node/v24.22.3/node-v24.22.3-linux-x64.tar.gz"] = nodeArchive(t, "24.22.2")
	r = newRuntimes()
	log.Reset()
	if _, err := r.Install(context.Background(), &log, "nodejs", "nodejs", "24", "mirr", ""); err != nil {
		t.Fatalf("fallback: %v\n%s", err, log.String())
	}
	if !strings.Contains(log.String(), "pkg.checksum_mismatch") || !strings.Contains(log.String(), "trying official") {
		t.Fatalf("fallback not logged:\n%s", log.String())
	}
	if m := readManifest(t, r, "nodejs", "24.22.3"); m.Source != sources.IDOfficial {
		t.Fatalf("manifest after fallback: %+v", m)
	}

	// Without a choice the probe excludes the mismatching mirror.
	r = newRuntimes()
	log.Reset()
	if _, err := r.Install(context.Background(), &log, "nodejs", "nodejs", "24", "", ""); err != nil {
		t.Fatalf("probed install: %v\n%s", err, log.String())
	}
	if !strings.Contains(log.String(), "source mirr: in_sync") && !strings.Contains(log.String(), "source mirr: mismatch") {
		t.Fatalf("probe not logged:\n%s", log.String())
	}

	// Unknown sources, and supplementary mirrors under the official-only
	// policy, are refused.
	for _, tc := range []struct{ source, policy string }{{"nope", ""}, {"mirr", helperapi.PolicyOfficial}} {
		if _, err := newRuntimes().Install(context.Background(), &log, "nodejs", "nodejs", "24", tc.source, tc.policy); code(err) != helperapi.CodePkgSourceUnknown {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func writeSystemFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func release(date, hash string) []byte {
	return []byte(fmt.Sprintf("Origin: Debian\nCodename: trixie\nDate: %s\nSHA256:\n %s 5000 main/binary-amd64/Packages\n %sx 900 main/binary-amd64/Packages.xz\n", date, hash, hash))
}

func TestProbeSlots(t *testing.T) {
	p := newProviderServer(t)
	cat := mirrorCatalog(t, p)
	archive := nodeArchive(t, "24.22.3")
	p.node(t, "24.22.3", archive)
	p.files["/mirror/node/v24.22.3/node-v24.22.3-linux-x64.tar.gz"] = archive
	const now, older = "Sat, 14 Nov 2026 20:03:59 UTC", "Sat, 14 Nov 2026 14:03:59 UTC"
	for _, base := range []string{"/debian", "/alias/debian", "/inuse/debian"} {
		date, hash := now, "same"
		if base == "/inuse/debian" {
			date, hash = older, "stale" // the system's own mirror lags
		}
		for _, suite := range []string{"trixie", "trixie-updates"} {
			p.files[base+"/dists/"+suite+"/InRelease"] = release(date, hash)
			p.files[base+"/dists/"+suite+"/main/binary-amd64/Packages.xz"] = []byte(strings.Repeat("p", 900))
		}
	}
	root := t.TempDir()
	writeSystemFile(t, root, "/etc/apt/sources.list.d/debian.sources", "Types: deb\nURIs: "+p.URL+"/inuse/debian/\nSuites: trixie trixie-updates\nComponents: main\n")
	// Files the panel manages are not system sources.
	writeSystemFile(t, root, "/etc/apt/sources.list.d/cairn-x.sources", "Types: deb\nURIs: "+p.URL+"/vendor\nSuites: trixie\nComponents: main\n")

	r := p.runtimes(t)
	prober := &sources.Prober{HTTP: p.Client(), Sample: 512}
	r.Mirrors, r.Prober = &cat.Mirrors, prober
	s := &Software{Catalog: cat, Manager: "apt", OS: platform.OSRelease{ID: "debian", VersionID: "13", Codename: "trixie"},
		Chain: []string{"debian-13", "debian"}, Arch: "amd64", Root: root, Log: slog.New(slog.DiscardHandler), Runtimes: r, Sources: prober}
	res, err := s.probe(context.Background(), helperapi.Caller{}, helperapi.ProbeRequest{Items: []helperapi.SoftwareRequest{
		{Software: "nodejs", Line: "24"}, {Software: "nginx", Line: "1.26"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Slots) != 2 {
		t.Fatalf("slots: %+v", res.Slots)
	}
	rt, sys := res.Slots[0], res.Slots[1]
	if rt.ID != "runtime:nodejs:24" || !rt.Selectable || rt.Version != "24.22.3" || rt.File != "node-v24.22.3-linux-x64.tar.gz" ||
		len(rt.Candidates) != 2 || rt.Selected == "" || rt.Warning != "" {
		t.Fatalf("runtime slot: %+v", rt)
	}
	for _, c := range rt.Candidates {
		if c.Status != sources.StatusInSync || c.Recommended != (c.ID == rt.Selected) {
			t.Errorf("runtime candidate: %+v", c)
		}
	}
	// The system's main repositories can be chosen for this install. The
	// source in use lags, so the fastest eligible candidate is preselected
	// (the official one: nothing is fast enough here to measure).
	if sys.ID != "system:main" || !sys.Selectable || sys.Warning != helperapi.WarnInUseBehind || len(sys.Candidates) != 3 ||
		sys.Selected != sources.IDOfficial || !sys.Candidates[0].Recommended {
		t.Fatalf("system slot: %+v", sys)
	}
	inUse := sys.Candidates[1]
	if !inUse.InUse || inUse.Status != sources.StatusBehind || inUse.LagSeconds != 6*3600 || inUse.Kind != sources.KindOther || inUse.Recommended {
		t.Errorf("in-use candidate: %+v", inUse)
	}
	if alias := sys.Candidates[2]; alias.Kind != sources.KindOfficialMirror || alias.Status != sources.StatusInSync {
		t.Errorf("official alias: %+v", alias)
	}

	// The official-only policy leaves out the supplementary mirror.
	res, err = s.probe(context.Background(), helperapi.Caller{}, helperapi.ProbeRequest{Policy: helperapi.PolicyOfficial,
		Items: []helperapi.SoftwareRequest{{Software: "nodejs", Line: "24"}}})
	if err != nil || len(res.Slots) != 1 || len(res.Slots[0].Candidates) != 1 || res.Slots[0].Selected != sources.IDOfficial {
		t.Fatalf("official policy: %+v %v", res, err)
	}
	// A source that is not a candidate of the system repositories is
	// refused before anything runs.
	if _, err := s.install(context.Background(), helperapi.Caller{}, helperapi.SoftwareRequest{Software: "nginx", Line: "1.26", Source: "mirr"}, &strings.Builder{}); code(err) != helperapi.CodePkgSourceUnknown {
		t.Fatalf("source for a repository install: %v", err)
	}
}
