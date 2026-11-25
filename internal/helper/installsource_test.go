// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/platform"
	"github.com/Kobayashi-classmate/Cairn/internal/sources"
)

// sourceCatalog is a catalog whose system repositories are served by the
// test server: Debian's main archive with one country alias, its security
// archive, and Rocky's AppStream with a mirror list. nginx and kv come from
// the system repositories, db from a vendor repository, Node.js is a
// runtime.
func sourceCatalog(t *testing.T, p *providerServer) *catalog.Catalog {
	t.Helper()
	files := fstest.MapFS{
		"software/nodejs.toml": {Data: []byte(`id = "nodejs"
name = "Node.js"
category = "runtime"
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
lines = [{ version = "1.26" }]
[[sources]]
method = "system"
releases = ["debian-13", "el-9"]
lines = ["1.26"]
packages = ["nginx"]
`)},
		"software/kv.toml": {Data: []byte(`id = "kv"
name = "KV"
category = "database"
lines = [{ version = "7.0" }]
[[sources]]
method = "system"
releases = ["debian-13"]
lines = ["7.0"]
packages = ["kv-server"]
`)},
		"software/db.toml": {Data: []byte(`id = "db"
name = "DB"
category = "database"
lines = [{ version = "2.1" }]
[[sources]]
method = "vendor"
repo = "vend"
releases = ["debian-13"]
lines = ["2.1"]
packages = ["db-server"]
`)},
		// A host of its own (vendor hosts tell packages apart), nothing
		// listening: its probe fails at once.
		"repos/vend.toml": {Data: []byte(`id = "vend"
name = "Vendor"
official = true
releases = ["debian-13"]
[apt]
uri = "https://localhost:1/vend"
suite = "{codename}"
components = ["main"]
key = { url = "https://localhost:1/key.asc", fingerprints = ["15058500A0235D97F5D10063B188E2B695BD4743"] }
`)},
		"mirrors.toml": {Data: []byte(fmt.Sprintf(`[[system]]
distro = "debian"
role = "main"
official = "%[1]s/debian"
suites = ["{codename}", "{codename}-updates"]
mirrors = ["%[1]s/alias/debian"]

[[system]]
distro = "debian"
role = "security"
official = "%[1]s/debian-security"
suites = ["{codename}-security"]

[[system]]
distro = "rocky"
role = "main"
official = "%[1]s/rocky/{major}/AppStream/$basearch/os/"
section = "appstream"
mirror_list = "%[1]s/mirrorlist?arch=$basearch"
roots = ["%[1]s/rocky/"]
`, p.URL))},
	}
	cat, err := catalog.LoadFS(files)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// serveAPT publishes in-sync (or older) apt indexes of the main suites at
// base.
func serveAPT(p *providerServer, base string, behind bool) {
	date, hash := "Sat, 14 Nov 2026 20:03:59 UTC", "same"
	if behind {
		date, hash = "Sat, 14 Nov 2026 14:03:59 UTC", "stale"
	}
	for _, suite := range []string{"trixie", "trixie-updates"} {
		p.files[base+"/dists/"+suite+"/InRelease"] = release(date, hash)
		p.files[base+"/dists/"+suite+"/main/binary-amd64/Packages.xz"] = []byte(strings.Repeat("p", 900))
	}
}

const (
	vendSources = "Types: deb\nURIs: https://localhost:1/vend\nSuites: trixie\nComponents: main\n"
	listsMtime  = 1_700_000_000
)

// aptSourceEnv is a Debian 13 system using its own mirror (inuse/debian),
// in deb822 and one-line form, with the security archive, a vendor
// repository and apt lists.
type aptSourceEnv struct {
	p    *providerServer
	f    *fakeSystem
	s    *Software
	o    *Ops
	r    *Registry
	root string
}

func newAPTSourceEnv(t *testing.T) *aptSourceEnv {
	t.Helper()
	p := newProviderServer(t)
	for _, base := range []string{"/debian", "/alias/debian", "/inuse/debian"} {
		serveAPT(p, base, false)
	}
	e := &aptSourceEnv{p: p, f: &fakeSystem{}}
	e.f.use(t)
	e.s, e.o, e.r = softwareFor(t, sourceCatalog(t, p), "debian-13", trixie, "apt")
	e.s.Sources = &sources.Prober{HTTP: p.Client(), Sample: 512}
	e.root = e.s.Root
	writeSystemFile(t, e.root, "/etc/apt/sources.list.d/debian.sources",
		"Types: deb\nURIs: "+p.URL+"/inuse/debian/\nSuites: trixie trixie-updates\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n\n"+
			"Types: deb\nURIs: "+p.URL+"/debian-security\nSuites: trixie-security\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n")
	writeSystemFile(t, e.root, "/etc/apt/sources.list", "# Old style\ndeb [arch=amd64] "+p.URL+"/inuse/debian trixie main\n")
	writeSystemFile(t, e.root, "/etc/apt/sources.list.d/cairn-vend.sources", vendSources)
	for name, content := range map[string]string{
		"127.0.0.1_inuse_debian_dists_trixie_InRelease":             "release",
		"127.0.0.1_debian-security_dists_trixie-security_InRelease": "security release",
		"lock":            "",
		"partial/x.part":  "partial",
		"auxfiles/x.data": "aux",
	} {
		writeSystemFile(t, e.root, "/var/lib/apt/lists/"+name, content)
		_ = os.Chtimes(filepath.Join(e.root, "var/lib/apt/lists", name), time.Unix(listsMtime, 0), time.Unix(listsMtime, 0))
	}
	e.f.madison = " nginx | 1.26.3-3+deb13u10 | " + p.URL + "/inuse/debian trixie/main amd64 Packages\n" +
		" kv-server | 7.0.15-1 | " + p.URL + "/inuse/debian trixie/main amd64 Packages\n"
	e.f.privateMadison = " nginx | 1.26.3-3+deb13u10 | " + p.URL + "/alias/debian trixie/main amd64 Packages\n" +
		" kv-server | 7.0.15-1 | " + p.URL + "/alias/debian trixie/main amd64 Packages\n"
	return e
}

// candidate returns the ID the install probe gives the candidate at the
// address containing part.
func (e *aptSourceEnv) candidate(t *testing.T, part string) string {
	t.Helper()
	res, err := e.s.probe(context.Background(), helperapi.Caller{}, helperapi.ProbeRequest{Items: []helperapi.SoftwareRequest{{Software: "nginx", Line: "1.26"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Slots[0].Candidates {
		if strings.Contains(c.URL, part) {
			return c.ID
		}
	}
	t.Fatalf("no candidate at %s: %+v", part, res.Slots[0].Candidates)
	return ""
}

// snapshot reads every file below dirs of root: content, mode and time.
func snapshot(t *testing.T, root string, dirs ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range dirs {
		_ = filepath.WalkDir(filepath.Join(root, d), func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, _ := de.Info()
			data := ""
			if de.Type().IsRegular() {
				b, _ := os.ReadFile(p) //nolint:gosec // G122: a test's own temporary tree.
				data = string(b)
			}
			out[p] = fmt.Sprintf("%v %d %q", info.Mode(), info.ModTime().UnixNano(), data)
			return nil
		})
	}
	return out
}

func sameSnapshot(t *testing.T, before, after map[string]string) {
	t.Helper()
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s changed: %s -> %s", p, v, after[p])
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Errorf("%s added", p)
		}
	}
}

// configDir returns the private configuration a command names (its index
// directory's parent).
func configDir(cmd string) string {
	for _, f := range strings.Fields(cmd) {
		for _, opt := range []string{"Dir::State::Lists=", "--setopt=reposdir="} {
			if v, ok := strings.CutPrefix(f, opt); ok {
				return filepath.Dir(v)
			}
		}
	}
	return ""
}

func leftovers(t *testing.T, root string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(root, "var/tmp", pkgSourcePrefix+"*"))
	return m
}

func readText(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Each line shows only the slot it is installed from; the system's main
// repositories are selectable, the source in use preselected while it is
// eligible, and the Host page still sees every role.
func TestProbeSlotsPerMethod(t *testing.T) {
	e := newAPTSourceEnv(t)
	p := e.p
	archive := nodeArchive(t, "24.22.3")
	p.node(t, "24.22.3", archive)
	r := p.runtimes(t)
	r.Prober = e.s.Sources
	e.s.Runtimes = r
	probe := func(policy string, items ...helperapi.SoftwareRequest) map[string]helperapi.SourceSlot {
		t.Helper()
		res, err := e.s.probe(context.Background(), helperapi.Caller{}, helperapi.ProbeRequest{Items: items, Policy: policy})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]helperapi.SourceSlot{}
		for _, sl := range res.Slots {
			out[sl.ID] = sl
		}
		if len(res.Plan) != len(items) {
			t.Fatalf("plan: %+v", res.Plan)
		}
		return out
	}
	ids := func(m map[string]helperapi.SourceSlot) string {
		var out []string
		for id, sl := range m {
			out = append(out, id+"="+strings.Join(sl.Software, ","))
		}
		slices.Sort(out)
		return strings.Join(out, " ")
	}
	nginx, kv := helperapi.SoftwareRequest{Software: "nginx", Line: "1.26"}, helperapi.SoftwareRequest{Software: "kv", Line: "7.0"}
	node, db := helperapi.SoftwareRequest{Software: "nodejs", Line: "24"}, helperapi.SoftwareRequest{Software: "db", Line: "2.1"}
	for _, tc := range []struct {
		items []helperapi.SoftwareRequest
		want  string
	}{
		{[]helperapi.SoftwareRequest{node}, "runtime:nodejs:24=nodejs"},
		{[]helperapi.SoftwareRequest{db}, "repo:vend=db"},
		{[]helperapi.SoftwareRequest{nginx}, "system:main=nginx"},
		{[]helperapi.SoftwareRequest{nginx, kv, node, db}, "repo:vend=db runtime:nodejs:24=nodejs system:main=nginx,kv"},
	} {
		if got := ids(probe("", tc.items...)); got != tc.want {
			t.Errorf("%v: %s, want %s", tc.items, got, tc.want)
		}
	}

	// The source in use is eligible: it stays preselected.
	sys := probe("", nginx)["system:main"]
	if !sys.Selectable || sys.Role != "main" || len(sys.Candidates) != 3 || sys.Warning != "" {
		t.Fatalf("system slot: %+v", sys)
	}
	inUse := sys.Candidates[candidateIndex(sys.Candidates, sys.Selected)]
	if !inUse.InUse || !inUse.Recommended || inUse.Kind != sources.KindOther {
		t.Fatalf("selected: %+v", sys)
	}
	for _, c := range sys.Candidates {
		if c.Recommended != (c.ID == sys.Selected) {
			t.Errorf("recommended: %+v", c)
		}
	}
	// The official-only policy leaves out a source of another kind, even
	// the one in use; the fastest official candidate is preselected.
	sys = probe(helperapi.PolicyOfficial, nginx)["system:main"]
	if len(sys.Candidates) != 2 || sys.Candidates[0].ID != sources.IDOfficial || sys.Candidates[1].Kind != sources.KindOfficialMirror ||
		sys.Selected != sources.IDOfficial {
		t.Fatalf("official policy: %+v", sys)
	}
	// The source in use lags: the fastest eligible candidate instead.
	serveAPT(p, "/inuse/debian", true)
	sys = probe("", nginx)["system:main"]
	if sys.Warning != helperapi.WarnInUseBehind || sys.Selected != sources.IDOfficial {
		t.Fatalf("in use behind: %+v", sys)
	}
	// Nothing answers: nothing preselected.
	for _, base := range []string{"/debian", "/alias/debian", "/inuse/debian"} {
		delete(p.files, base+"/dists/trixie/InRelease")
	}
	sys = probe("", nginx)["system:main"]
	if sys.Selected != "" || sys.Warning == "" {
		t.Fatalf("nothing eligible: %+v", sys)
	}

	// The Host page: every role, the one in use selected, as before.
	h := &Host{StateDir: t.TempDir(), Software: e.s, Root: e.root}
	slots := h.probeSystemSources(context.Background())
	if len(slots) != 2 || slots[0].ID != "system:main" || slots[1].ID != "system:security" || !slots[1].Selectable || slots[1].Selected == "" {
		t.Fatalf("host slots: %+v", slots)
	}
}

// Installing from a chosen mirror runs every apt command of the
// transaction with a private source configuration and index directory;
// /etc/apt and the system's lists stay byte for byte, the private copy is
// removed.
func TestInstallFromChosenSourceAPT(t *testing.T) {
	e := newAPTSourceEnv(t)
	alias := e.candidate(t, "/alias/")
	before := snapshot(t, e.root, "etc/apt", "var/lib/apt")
	e.f.cmds = nil
	inspected := false
	e.f.hook = func(cmd string, _ io.Writer) {
		if !strings.HasPrefix(cmd, "apt-get -o") || !strings.HasSuffix(cmd, " update") {
			return
		}
		inspected = true
		dir := configDir(cmd)
		if !strings.HasPrefix(dir, filepath.Join(e.root, "var/tmp", pkgSourcePrefix)) {
			t.Errorf("private directory %q", dir)
		}
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o755 {
			t.Errorf("private directory mode: %v %v", info, err)
		}
		for _, opt := range []string{"Dir::Etc::SourceList=" + dir + "/sources.list", "Dir::Etc::SourceParts=" + dir + "/sources.list.d",
			"Dir::State::Lists=" + dir + "/lists", "Dir::Cache::pkgcache=" + dir + "/pkgcache.bin", "Dir::Cache::srcpkgcache=" + dir + "/srcpkgcache.bin"} {
			if !strings.Contains(cmd, "-o "+opt+" ") {
				t.Errorf("option %s missing: %s", opt, cmd)
			}
		}
		// The main role's entries point at the mirror, in both formats;
		// the security archive and the vendor repository stay.
		deb822 := readText(t, filepath.Join(dir, "sources.list.d/debian.sources"))
		if !strings.Contains(deb822, "URIs: "+e.p.URL+"/alias/debian\nSuites: trixie trixie-updates\n") ||
			!strings.Contains(deb822, "URIs: "+e.p.URL+"/debian-security\n") || !strings.Contains(deb822, "Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg") {
			t.Errorf("debian.sources:\n%s", deb822)
		}
		if got := readText(t, filepath.Join(dir, "sources.list")); got != "# Old style\ndeb [arch=amd64] "+e.p.URL+"/alias/debian trixie main\n" {
			t.Errorf("sources.list:\n%s", got)
		}
		if got := readText(t, filepath.Join(dir, "sources.list.d/cairn-vend.sources")); got != vendSources {
			t.Errorf("vendor file:\n%s", got)
		}
		// The indexes are copies with their dates; no lock, no partial
		// downloads.
		info, err := os.Stat(filepath.Join(dir, "lists/127.0.0.1_inuse_debian_dists_trixie_InRelease"))
		if err != nil || info.ModTime().Unix() != listsMtime {
			t.Errorf("seeded index: %v %v", info, err)
		}
		for _, name := range []string{"lock", "partial", "auxfiles"} {
			if _, err := os.Lstat(filepath.Join(dir, "lists", name)); err == nil {
				t.Errorf("lists/%s copied", name)
			}
		}
	}

	op, log := runSoftwareOp(t, e.o, e.r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "nginx", Line: "1.26", Source: alias})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("install: %+v\n%s", op.Error, log)
	}
	if !inspected {
		t.Fatalf("no private apt-get update: %v", e.f.cmds)
	}
	if !strings.Contains(log, "Package source: 127.0.0.1 ("+e.p.URL+"/alias/debian), for this installation only.") {
		t.Errorf("log:\n%s", log)
	}
	// Only the comparison with the system's own indexes reads those.
	var madison, install bool
	for _, c := range e.f.cmds {
		if strings.HasPrefix(c, "apt-get") && configDir(c) == "" {
			t.Errorf("ran with the system's configuration: %s", c)
		}
		madison = madison || strings.HasPrefix(c, "query apt-cache -o Dir::Etc::SourceList=") && strings.HasSuffix(c, " madison nginx")
		install = install || strings.HasPrefix(c, "apt-get -o Dir::Etc::SourceList=") &&
			strings.HasSuffix(c, " install -y --no-install-recommends -o Dpkg::Options::=--force-confold nginx=1.26.3-3+deb13u10")
	}
	if !madison || !install || e.f.installed["nginx"] != "1.26.3-3+deb13u10" {
		t.Fatalf("commands: %v", e.f.cmds)
	}
	sameSnapshot(t, before, snapshot(t, e.root, "etc/apt", "var/lib/apt"))
	if l := leftovers(t, e.root); len(l) != 0 {
		t.Fatalf("private copy left: %v", l)
	}
}

// Debian's cloud images name their mirror through "mirror+file:": the
// private copy points those entries at the chosen mirror too.
func TestInstallFromChosenSourceMirrorFile(t *testing.T) {
	e := newAPTSourceEnv(t)
	_ = os.Remove(filepath.Join(e.root, "etc/apt/sources.list"))
	writeSystemFile(t, e.root, "/etc/apt/sources.list.d/debian.sources",
		"Types: deb\nURIs: mirror+file:///etc/apt/mirrors/debian.list\nSuites: trixie trixie-updates\nComponents: main\n")
	writeSystemFile(t, e.root, "/etc/apt/mirrors/debian.list", e.p.URL+"/inuse/debian\n")
	alias := e.candidate(t, "/alias/")
	var deb822 string
	e.f.hook = func(cmd string, _ io.Writer) {
		if dir := configDir(cmd); dir != "" && strings.HasSuffix(cmd, " update") {
			deb822 = readText(t, filepath.Join(dir, "sources.list.d/debian.sources"))
		}
	}
	op, log := runSoftwareOp(t, e.o, e.r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "nginx", Line: "1.26", Source: alias})
	if op.State != helperapi.OpSucceeded || !strings.Contains(deb822, "URIs: "+e.p.URL+"/alias/debian\n") {
		t.Fatalf("install %+v:\n%s\n%s", op.Error, deb822, log)
	}
	if got := readText(t, filepath.Join(e.root, "etc/apt/mirrors/debian.list")); got != e.p.URL+"/inuse/debian\n" {
		t.Fatalf("mirror list changed: %s", got)
	}
}

// Choosing the source in use is installing as configured; unknown and
// ineligible sources, and sources the policy leaves out, are refused before
// the package manager runs.
func TestInstallChosenSourceRefusals(t *testing.T) {
	e := newAPTSourceEnv(t)
	inUse, alias := e.candidate(t, "/inuse/"), e.candidate(t, "/alias/")

	op, log := runSoftwareOp(t, e.o, e.r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "nginx", Line: "1.26", Source: inUse})
	if op.State != helperapi.OpSucceeded || !e.f.ran("apt-get update") || !e.f.ran("apt-get install -y") || configDir(strings.Join(e.f.cmds, " ")) != "" {
		t.Fatalf("source in use: %+v %v\n%s", op.Error, e.f.cmds, log)
	}
	if !strings.Contains(log, "the one the system uses") || strings.Contains(log, "for this installation only") {
		t.Errorf("log:\n%s", log)
	}

	e.f.cmds = nil
	for _, tc := range []struct {
		req    helperapi.SoftwareRequest
		code   string
		reason string
	}{
		{helperapi.SoftwareRequest{Software: "nginx", Line: "1.26", Source: "nope"}, helperapi.CodePkgSourceUnknown, ""},
		// The policy allows official addresses only; the one in use is not.
		{helperapi.SoftwareRequest{Software: "nginx", Line: "1.26", Source: inUse, Policy: helperapi.PolicyOfficial}, helperapi.CodePkgSourceUnknown, ""},
		// A vendor repository has no choice.
		{helperapi.SoftwareRequest{Software: "db", Line: "2.1", Source: sources.IDOfficial}, helperapi.CodePkgSourceUnknown, ""},
	} {
		op, _ := runSoftwareOp(t, e.o, e.r, helperapi.ActionPkgInstall, tc.req)
		if op.Error == nil || op.Error.Code != tc.code {
			t.Errorf("%+v: %+v", tc.req, op.Error)
		}
	}
	// The mirror lags by install time: refused, no fallback.
	serveAPT(e.p, "/alias/debian", true)
	op, _ = runSoftwareOp(t, e.o, e.r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "nginx", Line: "1.26", Source: alias})
	if op.Error == nil || op.Error.Code != helperapi.CodePkgSourceUnusable || op.Error.Params["reason"] != unusableStatus ||
		op.Error.Params["status"] != sources.StatusBehind || op.Error.Params["source"] != alias {
		t.Errorf("behind: %+v", op.Error)
	}
	if e.f.ran("apt-get") || len(leftovers(t, e.root)) != 0 {
		t.Fatalf("refused installs ran: %v", e.f.cmds)
	}
}

// The private copy goes whatever happens; a chosen source apt cannot read,
// or one with an older version than the system's own, fails the install.
func TestInstallChosenSourceFailures(t *testing.T) {
	e := newAPTSourceEnv(t)
	alias := e.candidate(t, "/alias/")
	req := helperapi.SoftwareRequest{Software: "nginx", Line: "1.26", Source: alias}
	before := snapshot(t, e.root, "etc/apt", "var/lib/apt")

	e.f.failInstall = "nginx="
	if op, log := runSoftwareOp(t, e.o, e.r, helperapi.ActionPkgInstall, req); op.State != helperapi.OpFailed || !strings.Contains(log, "for this installation only") {
		t.Fatalf("failing install: %s %+v\n%s", op.State, op.Error, log)
	}
	if l := leftovers(t, e.root); len(l) != 0 {
		t.Fatalf("private copy left after a failure: %v", l)
	}
	e.f.failInstall = ""

	e.f.hook = func(cmd string, log io.Writer) {
		if configDir(cmd) != "" && strings.HasSuffix(cmd, " update") {
			_, _ = fmt.Fprintf(log, "W: Failed to fetch %s/alias/debian/dists/trixie/InRelease  404  Not Found\n", e.p.URL)
		}
	}
	installs := e.f.count("apt-get -o Dir::Etc::SourceList=")
	op, _ := runSoftwareOp(t, e.o, e.r, helperapi.ActionPkgInstall, req)
	if op.Error == nil || op.Error.Code != helperapi.CodePkgSourceUnusable || op.Error.Params["reason"] != unusableUpdate ||
		e.f.count("apt-get -o Dir::Etc::SourceList=") != installs+1 {
		t.Fatalf("unreadable source: %+v %v", op.Error, e.f.cmds)
	}
	e.f.hook = nil

	// The system's own indexes know a newer build than the mirror offers.
	e.f.madison = " nginx | 1.26.3-3+deb13u11 | " + e.p.URL + "/inuse/debian trixie/main amd64 Packages\n"
	op, _ = runSoftwareOp(t, e.o, e.r, helperapi.ActionPkgInstall, req)
	if op.Error == nil || op.Error.Code != helperapi.CodePkgSourceUnusable || op.Error.Params["reason"] != unusableOlder ||
		op.Error.Params["version"] != "1.26.3-3+deb13u10" || op.Error.Params["system"] != "1.26.3-3+deb13u11" {
		t.Fatalf("older version: %+v", op.Error)
	}
	// A newer one from the mirror is fine.
	e.f.madison = " nginx | 1.26.3-3+deb13u9 | " + e.p.URL + "/inuse/debian trixie/main amd64 Packages\n"
	if op, log := runSoftwareOp(t, e.o, e.r, helperapi.ActionPkgInstall, req); op.State != helperapi.OpSucceeded {
		t.Fatalf("newer version: %+v\n%s", op.Error, log)
	}
	sameSnapshot(t, before, snapshot(t, e.root, "etc/apt", "var/lib/apt"))
	if l := leftovers(t, e.root); len(l) != 0 {
		t.Fatalf("private copy left: %v", l)
	}
}

// An install set has one transaction and so one main source: its system
// lines that name one name the same, and all of its lines use it.
func TestInstallSetChosenSource(t *testing.T) {
	e := newAPTSourceEnv(t)
	inUse, alias := e.candidate(t, "/inuse/"), e.candidate(t, "/alias/")
	nginx := helperapi.SoftwareRequest{Software: "nginx", Line: "1.26", Source: alias}

	op, _, _ := runSetOp(t, e.o, e.r, nginx, helperapi.SoftwareRequest{Software: "kv", Line: "7.0", Source: inUse})
	if op.Error == nil || op.Error.Code != "validation.invalid" || op.Error.Params["field"] != "source" || op.Error.Params["rule"] != "mismatch" {
		t.Fatalf("two sources: %+v", op.Error)
	}
	op, _, _ = runSetOp(t, e.o, e.r, nginx, helperapi.SoftwareRequest{Software: "db", Line: "2.1", Source: alias})
	if op.Error == nil || op.Error.Code != helperapi.CodePkgSourceUnknown {
		t.Fatalf("source for a vendor line: %+v", op.Error)
	}
	if e.f.ran("apt-get") {
		t.Fatalf("refused sets ran: %v", e.f.cmds)
	}

	op, res, log := runSetOp(t, e.o, e.r, nginx, helperapi.SoftwareRequest{Software: "kv", Line: "7.0"})
	if op.State != helperapi.OpSucceeded || res.Items[0].Result == nil || res.Items[1].Result == nil {
		t.Fatalf("set: %+v %+v\n%s", op.Error, res, log)
	}
	if e.f.count("apt-get -o Dir::Etc::SourceList=") != 2 || e.f.ran("apt-get install") ||
		!strings.Contains(strings.Join(e.f.cmds, "\n"), "nginx=1.26.3-3+deb13u10 kv-server=7.0.15-1") ||
		!strings.Contains(log, "for this installation only") {
		t.Fatalf("one private transaction: %v\n%s", e.f.cmds, log)
	}
	if l := leftovers(t, e.root); len(l) != 0 {
		t.Fatalf("private copy left: %v", l)
	}
}

// dnf gets a private repository directory, with the system repositories
// moved to the mirror, and a private metadata cache seeded from the
// system's; /etc/yum.repos.d and /var/cache/dnf stay as they are.
func TestInstallFromChosenSourceDNF(t *testing.T) {
	p := newProviderServer(t)
	repomd := func(sum string) []byte {
		return []byte(`<repomd><data type="primary"><checksum type="sha256">` + sum + `</checksum><location href="repodata/primary.xml.gz"/><timestamp>1794686639</timestamp><size>10</size></data></repomd>`)
	}
	for _, base := range []string{"/rocky/9/AppStream/x86_64/os/", "/inuse/rocky/9/AppStream/x86_64/os/", "/mirror/rocky/9/AppStream/x86_64/os/"} {
		p.files[base+"repodata/repomd.xml"] = repomd("abc")
	}
	p.files["/mirrorlist?arch=x86_64"] = []byte("http://insecure.example/rocky/9/AppStream/x86_64/os/\n" + p.URL + "/mirror/rocky/9/AppStream/x86_64/os/\n")
	f := &fakeSystem{repoquery: "nginx 2:1.26.3-1.el9\n", privateRepoquery: "nginx 2:1.26.3-1.el9\n"}
	f.use(t)
	rocky := platform.OSRelease{ID: "rocky", VersionID: "9.6"}
	s, o, r := softwareFor(t, sourceCatalog(t, p), "el-9", rocky, "dnf")
	s.Sources = &sources.Prober{HTTP: p.Client(), Sample: 512}
	root := s.Root
	repo := "[baseos]\nname=BaseOS\nbaseurl=" + p.URL + "/inuse/rocky/$releasever/BaseOS/$basearch/os/\ngpgcheck=1\n\n" +
		"[appstream]\nname=AppStream\nbaseurl=" + p.URL + "/inuse/rocky/$releasever/AppStream/$basearch/os/\ngpgcheck=1\n"
	vend := "[cairn-vend]\nbaseurl=https://localhost:1/vend\n"
	writeSystemFile(t, root, "/etc/yum.repos.d/rocky.repo", repo)
	writeSystemFile(t, root, "/etc/yum.repos.d/cairn-vend.repo", vend)
	writeSystemFile(t, root, "/var/cache/dnf/cairn-vend-1234/repodata/repomd.xml", "vendor metadata")
	writeSystemFile(t, root, "/var/cache/dnf/cairn-vend-1234/packages/x.rpm", "package")
	writeSystemFile(t, root, "/var/cache/dnf/metadata_lock.pid", "1")
	before := snapshot(t, root, "etc/yum.repos.d", "var/cache/dnf")

	// The install probe offers the mirrors reached over HTTPS only.
	res, err := s.probe(context.Background(), helperapi.Caller{}, helperapi.ProbeRequest{Items: []helperapi.SoftwareRequest{{Software: "nginx", Line: "1.26"}}})
	if err != nil {
		t.Fatal(err)
	}
	sl := res.Slots[0]
	mirror := ""
	for _, c := range sl.Candidates {
		if strings.Contains(c.URL, "insecure") {
			t.Errorf("an HTTP mirror offered: %+v", c)
		}
		if strings.Contains(c.URL, "/mirror/") {
			mirror = c.ID
		}
	}
	if sl.ID != "system:main" || !sl.Selectable || mirror == "" || !sl.Candidates[candidateIndex(sl.Candidates, sl.Selected)].InUse {
		t.Fatalf("slot: %+v", sl)
	}
	if op, _ := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "nginx", Line: "1.26", Source: "insecure.example"}); op.Error == nil ||
		op.Error.Code != helperapi.CodePkgSourceUnknown {
		t.Fatalf("HTTP mirror: %+v", op.Error)
	}

	inspected := false
	f.hook = func(cmd string, _ io.Writer) {
		if !strings.HasPrefix(cmd, "dnf --setopt=") || !strings.HasSuffix(cmd, " makecache --assumeyes") {
			return
		}
		inspected = true
		dir := configDir(cmd)
		if !strings.Contains(cmd, "--setopt=reposdir="+dir+"/repos --setopt=cachedir="+dir+"/cache --setopt=system_cachedir="+dir+"/cache "+
			"--disableplugin=generate_completion_cache ") {
			t.Errorf("options: %s", cmd)
		}
		got := readText(t, filepath.Join(dir, "repos/rocky.repo"))
		if !strings.Contains(got, "baseurl="+p.URL+"/mirror/rocky/$releasever/AppStream/$basearch/os/\n") ||
			!strings.Contains(got, "baseurl="+p.URL+"/mirror/rocky/$releasever/BaseOS/$basearch/os/\n") {
			t.Errorf("rocky.repo:\n%s", got)
		}
		if got := readText(t, filepath.Join(dir, "repos/cairn-vend.repo")); got != vend {
			t.Errorf("vendor repo:\n%s", got)
		}
		if got := readText(t, filepath.Join(dir, "cache/cairn-vend-1234/repodata/repomd.xml")); got != "vendor metadata" {
			t.Errorf("seeded cache: %q", got)
		}
		for _, name := range []string{"cache/cairn-vend-1234/packages", "cache/metadata_lock.pid"} {
			if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
				t.Errorf("%s copied", name)
			}
		}
	}
	op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "nginx", Line: "1.26", Source: mirror})
	if op.State != helperapi.OpSucceeded || !inspected {
		t.Fatalf("install: %+v %v\n%s", op.Error, f.cmds, log)
	}
	var query, install bool
	for _, c := range f.cmds {
		query = query || strings.HasPrefix(c, "query dnf --setopt=reposdir=") && strings.Contains(c, " repoquery --latest-limit 1 ")
		install = install || strings.HasPrefix(c, "dnf --setopt=reposdir=") && strings.HasSuffix(c, " install -y nginx-2:1.26.3-1.el9")
	}
	if !query || !install || f.installed["nginx"] != "2:1.26.3-1.el9" {
		t.Fatalf("commands: %v", f.cmds)
	}
	sameSnapshot(t, before, snapshot(t, root, "etc/yum.repos.d", "var/cache/dnf"))
	if l := leftovers(t, root); len(l) != 0 {
		t.Fatalf("private copy left: %v", l)
	}

	// The system's cache knows a newer build: refused.
	f.repoquery = "nginx 2:1.26.4-1.el9\n"
	op, _ = runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "nginx", Line: "1.26", Source: mirror})
	if op.Error == nil || op.Error.Params["reason"] != unusableOlder {
		t.Fatalf("older: %+v", op.Error)
	}
	if l := leftovers(t, root); len(l) != 0 {
		t.Fatalf("private copy left: %v", l)
	}
}

// A private copy a stopped helper left behind goes with the next install;
// another user's directory, or a recent one, stays.
func TestRemoveStalePrivateCopies(t *testing.T) {
	base := t.TempDir()
	old := time.Now().Add(-2 * pkgSourceStale)
	for _, name := range []string{pkgSourcePrefix + "old", pkgSourcePrefix + "new", "other"} {
		if err := os.MkdirAll(filepath.Join(base, name, "lists"), 0o750); err != nil {
			t.Fatal(err)
		}
		if name != pkgSourcePrefix+"new" {
			_ = os.Chtimes(filepath.Join(base, name), old, old)
		}
	}
	removeStale(base)
	_, errOld := os.Stat(filepath.Join(base, pkgSourcePrefix+"old"))
	_, errNew := os.Stat(filepath.Join(base, pkgSourcePrefix+"new"))
	_, errOther := os.Stat(filepath.Join(base, "other"))
	if os.Getuid() == 0 && errOld == nil {
		t.Error("stale copy kept")
	}
	if os.Getuid() != 0 && errOld != nil {
		t.Error("a directory root does not own was removed")
	}
	if errNew != nil || errOther != nil {
		t.Errorf("removed: %v %v", errNew, errOther)
	}
}
