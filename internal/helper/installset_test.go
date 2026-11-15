// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

const trixieMadison = ` nginx | 1.26.3-3+deb13u10 | http://deb.debian.org/debian trixie/main amd64 Packages
 libnginx-mod-stream | 1.26.3-3+deb13u10 | http://deb.debian.org/debian trixie/main amd64 Packages
 php8.4-fpm | 8.4.27-1~deb13u1 | http://deb.debian.org/debian trixie/main amd64 Packages
 php8.4-cli | 8.4.27-1~deb13u1 | http://deb.debian.org/debian trixie/main amd64 Packages
 mariadb-server | 1:11.8.3-0+deb13u1 | http://deb.debian.org/debian trixie/main amd64 Packages
 mariadb-client | 1:11.8.3-0+deb13u1 | http://deb.debian.org/debian trixie/main amd64 Packages`

func runSetOp(t *testing.T, o *Ops, r *Registry, items ...helperapi.SoftwareRequest) (helperapi.Op, helperapi.InstallSetResult, string) {
	t.Helper()
	ref, err := call[helperapi.OpRef](t, r, helperapi.ActionPkgInstallSet, helperapi.InstallSetRequest{Items: items})
	if err != nil {
		t.Fatalf("install set: %v", err)
	}
	op := waitOp(t, o, ref.ID, helperapi.OpSucceeded, helperapi.OpFailed)
	got, _ := o.Get(ref.ID, 0)
	var res helperapi.InstallSetResult
	_ = json.Unmarshal(op.Result, &res)
	return op, res, got.Log
}

func (f *fakeSystem) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.cmds {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestInstallSetOneTransaction(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{systemd: true, madison: trixieMadison}
	f.use(t)
	s, o, r := softwareFor(t, cat, "debian-13", trixie, "apt")
	p := newProviderServer(t)
	s.Runtimes = p.runtimes(t)
	p.node(t, "24.22.3", nodeArchive(t, "24.22.3"))
	fakeVersion(t, "v24.22.3\n")

	op, res, log := runSetOp(t, o, r,
		helperapi.SoftwareRequest{Software: "nginx", Line: "1.26"},
		helperapi.SoftwareRequest{Software: "php", Line: "8.4"},
		helperapi.SoftwareRequest{Software: "nodejs", Line: "24"},
	)
	if op.State != helperapi.OpSucceeded || len(res.Items) != 3 {
		t.Fatalf("op %s %+v\n%s", op.State, op.Error, log)
	}
	for _, it := range res.Items {
		if it.Error != nil || it.Result == nil {
			t.Fatalf("%s %s: %+v\n%s", it.Software, it.Line, it.Error, log)
		}
	}
	// One index refresh and one transaction for both lines and the library
	// Node.js needs; the runtime downloaded meanwhile.
	if f.count("apt-get update") != 1 || f.count("apt-get install") != 1 ||
		!f.ran("apt-get install -y --no-install-recommends -o Dpkg::Options::=--force-confold nginx=1.26.3-3+deb13u10 libnginx-mod-stream=1.26.3-3+deb13u10 php8.4-fpm=8.4.27-1~deb13u1 php8.4-cli=8.4.27-1~deb13u1 libatomic1") {
		t.Fatalf("commands: %v", f.cmds)
	}
	if !f.ran("systemctl enable --now nginx") || !f.ran("systemctl enable --now php8.4-fpm") {
		t.Fatalf("services: %v", f.cmds)
	}
	if s.Runtimes.Installed("nodejs", "24") != "24.22.3" || !strings.Contains(log, "[Node.js 24] Downloading from") || !strings.Contains(log, "All 3 installed.") {
		t.Fatalf("runtime: %s", log)
	}
}

func TestInstallSetIsolatesAFailingLine(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{systemd: true, madison: trixieMadison, failInstall: "mariadb-server="}
	f.use(t)
	_, o, r := softwareFor(t, cat, "debian-13", trixie, "apt")
	op, res, log := runSetOp(t, o, r,
		helperapi.SoftwareRequest{Software: "nginx", Line: "1.26"},
		helperapi.SoftwareRequest{Software: "mariadb", Line: "11.8"},
	)
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("op %s %+v", op.State, op.Error)
	}
	if res.Items[0].Result == nil || res.Items[0].Error != nil || res.Items[1].Error == nil || res.Items[1].Result != nil {
		t.Fatalf("items: %+v", res.Items)
	}
	// The combined transaction failed; each line was then tried alone.
	if f.count("apt-get install") != 3 || !strings.Contains(log, "installing each line on its own") || !strings.Contains(log, "Not installed: MariaDB 11.8.") {
		t.Fatalf("commands %v\n%s", f.cmds, log)
	}
}

func TestInstallSetRefusals(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{madison: trixieMadison}
	f.use(t)
	_, o, r := softwareFor(t, cat, "debian-13", trixie, "apt")
	for name, req := range map[string]helperapi.InstallSetRequest{
		"empty":     {},
		"duplicate": {Items: []helperapi.SoftwareRequest{{Software: "nginx", Line: "1.26"}, {Software: "nginx", Line: "1.26"}}},
	} {
		if _, err := call[helperapi.OpRef](t, r, helperapi.ActionPkgInstallSet, req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A line not offered here, or a source for a repository line, fails
	// the whole set before anything is installed.
	for _, items := range [][]helperapi.SoftwareRequest{
		{{Software: "nginx", Line: "1.26"}, {Software: "php", Line: "7.4"}},
		{{Software: "nginx", Line: "1.26", Source: "tuna"}},
	} {
		if op, _, _ := runSetOp(t, o, r, items...); op.State != helperapi.OpFailed {
			t.Fatalf("%v: %s", items, op.State)
		}
	}
	if f.ran("apt-get") {
		t.Fatalf("ran %v", f.cmds)
	}
}

func TestLineWriterKeepsLinesWhole(t *testing.T) {
	var mu sync.Mutex
	var out strings.Builder
	a := &lineWriter{mu: &mu, w: &out, prefix: "[a] "}
	b := &lineWriter{mu: &mu, w: &out}
	_, _ = a.Write([]byte("one "))
	_, _ = b.Write([]byte("apt line\n"))
	_, _ = a.Write([]byte("two\nthree"))
	a.Flush()
	if out.String() != "apt line\n[a] one two\n[a] three\n" {
		t.Fatalf("%q", out.String())
	}
}

func TestEstimateParsers(t *testing.T) {
	n, k := parsePrintURIs("'http://deb.debian.org/debian/pool/main/n/nginx/nginx_1.26.3_amd64.deb' nginx_1.26.3_amd64.deb 581672 SHA256:ab\n" +
		"'http://deb.debian.org/debian/pool/main/libz.deb' libz.deb 1000 SHA256:cd\nnot a uri line\n")
	if n != 582672 || k != 2 {
		t.Fatalf("print-uris: %d %d", n, k)
	}
	b, c, ok := parseDNFSummary("Transaction Summary\n================\nInstall  44 Packages\nUpgrade   2 Packages\n\nTotal download size: 49 M\nInstalled size: 194 M\nOperation aborted.\n")
	if !ok || b != 49<<20 || c != 46 {
		t.Fatalf("dnf: %d %d %v", b, c, ok)
	}
	if _, _, ok := parseDNFSummary("Error: Cache-only enabled but no cache for 'baseos'"); ok {
		t.Fatal("no cache is not an estimate")
	}
	if b, _, ok := parseDNFSummary("Dependencies resolved.\nNothing to do.\nComplete!\n"); !ok || b != 0 {
		t.Fatal("nothing to do")
	}
}

func TestProbeEstimatesPackages(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{madison: trixieMadison, printURIs: "'http://deb.debian.org/debian/pool/main/n/nginx/nginx.deb' nginx.deb 581672 SHA256:ab\n"}
	f.use(t)
	s, _, _ := softwareFor(t, cat, "debian-13", trixie, "apt")
	s.Catalog = &catalog.Catalog{Software: cat.Software, Repos: cat.Repos} // no system entries: nothing to probe online
	est := s.estimate(context.Background(), []lineItem{
		{sw: &cat.Software[0], o: catalog.Offer{Method: catalog.MethodSystem, Packages: []string{"nginx"}}},
		{sw: &cat.Software[0], o: catalog.Offer{Method: catalog.MethodVendor, Repo: "sury", Line: "8.3", Packages: []string{"php8.3-fpm"}}},
	}, nil)
	if !est.Known || est.Bytes != 581672 || est.Packages != 1 || !est.Partial {
		t.Fatalf("estimate: %+v", est)
	}
	if !f.ran("query apt-get install --print-uris -qq --no-install-recommends nginx") {
		t.Fatalf("commands: %v", f.cmds)
	}
}

func TestAptFailedHosts(t *testing.T) {
	out := `Hit:1 http://deb.debian.org/debian trixie InRelease
Ign:6 https://apt.postgresql.org/pub/repos/apt trixie-pgdg InRelease
Err:6 https://apt.postgresql.org/pub/repos/apt trixie-pgdg InRelease
  Could not connect to apt.postgresql.org:443 (198.18.30.74), connection timed out
W: Failed to fetch https://apt.postgresql.org/pub/repos/apt/dists/trixie-pgdg/InRelease  Unable to connect to apt.postgresql.org:https:
W: Failed to fetch http://repo.example/debian/dists/x/InRelease  404
W: Some index files failed to download. They have been ignored, or old ones used instead.
`
	if got := strings.Join(aptFailedHosts(out), " "); got != "apt.postgresql.org repo.example" {
		t.Fatalf("hosts: %q", got)
	}
}
