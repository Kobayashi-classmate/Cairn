// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

type entry struct {
	name, body, link string
	typ              byte
	mode             int64
}

func tarGz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: typ, Linkname: e.link, Mode: mode, Size: int64(len(e.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func sum256(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func sum512(b []byte) string { s := sha512.Sum512(b); return hex.EncodeToString(s[:]) }

// providerServer imitates the five providers' official endpoints.
type providerServer struct {
	*httptest.Server
	files map[string][]byte
}

func newProviderServer(t *testing.T) *providerServer {
	t.Helper()
	p := &providerServer{files: map[string][]byte{}}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		data, ok := p.files[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *providerServer) runtimes(t *testing.T) *Runtimes {
	t.Helper()
	return &Runtimes{Dir: t.TempDir(), Arch: "amd64", HTTP: p.Client(), Bases: map[string]string{
		"nodejs": p.URL + "/node", "go": p.URL + "/go", "temurin": p.URL + "/adoptium",
		"dotnet": p.URL + "/dotnet", "python-build-standalone": p.URL + "/pbs",
	}}
}

func (p *providerServer) node(t *testing.T, version string, archive []byte) {
	p.files["/node/index.json"] = []byte(fmt.Sprintf(`[{"version":"v%s","files":["linux-x64","linux-arm64"]},{"version":"v24.1.0","files":["linux-x64"]}]`, version))
	file := "node-v" + version + "-linux-x64.tar.gz"
	p.files["/node/v"+version+"/"+file] = archive
	p.files["/node/v"+version+"/SHASUMS256.txt"] = []byte(sum256(archive) + "  " + file + "\n0000  other\n")
}

func fakeVersion(t *testing.T, out string) {
	t.Helper()
	old := versionCommand
	versionCommand = func(_ context.Context, bin string, _ ...string) (string, error) {
		if _, err := os.Stat(bin); err != nil {
			return "", err
		}
		return out, nil
	}
	t.Cleanup(func() { versionCommand = old })
}

func nodeArchive(t *testing.T, v string) []byte {
	return tarGz(t,
		entry{name: "node-v" + v + "-linux-x64/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "node-v" + v + "-linux-x64/bin/node", body: "#!binary", mode: 0o4755},
		entry{name: "node-v" + v + "-linux-x64/lib/npm-cli.js", body: "cli"},
		entry{name: "node-v" + v + "-linux-x64/bin/npm", typ: tar.TypeSymlink, link: "../lib/npm-cli.js"},
	)
}

func TestRuntimeInstallReplaceRemove(t *testing.T) {
	p := newProviderServer(t)
	r := p.runtimes(t)
	p.node(t, "24.22.3", nodeArchive(t, "24.22.3"))
	fakeVersion(t, "v24.22.3\n")
	var log strings.Builder
	v, err := r.Install(context.Background(), &log, "nodejs", "nodejs", "24")
	if err != nil || v != "24.22.3" {
		t.Fatalf("install: %q %v\n%s", v, err, log.String())
	}
	dir := filepath.Join(r.Dir, "nodejs", "24.22.3")
	st, err := os.Stat(filepath.Join(dir, "bin/node"))
	if err != nil || st.Mode().Perm() != 0o755 || st.Mode()&os.ModeSetuid != 0 {
		t.Fatalf("node binary: %v %v (setuid must be dropped)", st, err)
	}
	if target, _ := os.Readlink(filepath.Join(dir, "bin/npm")); target != "../lib/npm-cli.js" {
		t.Fatalf("npm link: %q", target)
	}
	if got := r.Installed("nodejs", "24"); got != "24.22.3" {
		t.Fatalf("line link: %q", got)
	}
	// Installing again is a no-op.
	log.Reset()
	if _, err := r.Install(context.Background(), &log, "nodejs", "nodejs", "24"); err != nil || !strings.Contains(log.String(), "Already installed") {
		t.Fatalf("reinstall: %v %s", err, log.String())
	}
	// A newer build of the line replaces the old one.
	p.node(t, "24.23.0", nodeArchive(t, "24.23.0"))
	fakeVersion(t, "v24.23.0\n")
	if v, err := r.Install(context.Background(), io.Discard, "nodejs", "nodejs", "24"); err != nil || v != "24.23.0" {
		t.Fatalf("update: %q %v", v, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("old version left behind")
	}
	if v, err := r.Remove("nodejs", "24"); err != nil || v != "24.23.0" {
		t.Fatalf("remove: %q %v", v, err)
	}
	entries, _ := os.ReadDir(filepath.Join(r.Dir, "nodejs"))
	if len(entries) != 0 {
		t.Fatalf("left after remove: %v", entries)
	}
	if _, err := r.Remove("nodejs", "24"); code(err) != helperapi.CodePkgNotInstalled {
		t.Fatalf("second remove: %v", err)
	}
}

func TestRuntimeRefusesBadDownloads(t *testing.T) {
	p := newProviderServer(t)
	r := p.runtimes(t)
	fakeVersion(t, "v24.22.3")

	// Checksum mismatch: nothing is unpacked.
	p.node(t, "24.22.3", nodeArchive(t, "24.22.3"))
	p.files["/node/v24.22.3/node-v24.22.3-linux-x64.tar.gz"] = append(p.files["/node/v24.22.3/node-v24.22.3-linux-x64.tar.gz"], 0)
	if _, err := r.Install(context.Background(), io.Discard, "nodejs", "nodejs", "24"); code(err) != CodeRuntimeChecksum {
		t.Fatalf("checksum: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(r.Dir, "nodejs")); len(entries) != 0 {
		t.Fatalf("unpacked despite the mismatch: %v", entries)
	}

	for name, archive := range map[string][]byte{
		"parent path":         tarGz(t, entry{name: "../evil", body: "x"}),
		"absolute path":       tarGz(t, entry{name: "/etc/evil", body: "x"}),
		"escaping symlink":    tarGz(t, entry{name: "a/link", typ: tar.TypeSymlink, link: "../../etc"}),
		"absolute symlink":    tarGz(t, entry{name: "a/link", typ: tar.TypeSymlink, link: "/etc/shadow"}),
		"device file":         tarGz(t, entry{name: "a/dev", typ: tar.TypeChar}),
		"link to grandparent": tarGz(t, entry{name: "a/up", typ: tar.TypeSymlink, link: "../.."}),
	} {
		p.node(t, "24.22.3", archive)
		if _, err := r.Install(context.Background(), io.Discard, "nodejs", "nodejs", "24"); code(err) != CodeRuntimeArchive {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A runtime that does not report the expected version is not installed.
	p.node(t, "24.22.3", nodeArchive(t, "24.22.3"))
	fakeVersion(t, "Illegal instruction")
	if _, err := r.Install(context.Background(), io.Discard, "nodejs", "nodejs", "24"); code(err) != CodeRuntimeBroken {
		t.Fatalf("broken runtime: %v", err)
	}
	if r.Installed("nodejs", "24") != "" {
		t.Fatal("broken runtime registered")
	}
}

func TestRuntimeProviders(t *testing.T) {
	p := newProviderServer(t)
	r := p.runtimes(t)
	goArchive := tarGz(t, entry{name: "go/bin/go", body: "go", mode: 0o755})
	p.files["/go/?mode=json&include=all"] = []byte(`[{"version":"go1.28rc1","stable":false,"files":[]},
		{"version":"go1.27.4","stable":true,"files":[{"filename":"go1.27.4.linux-amd64.tar.gz","os":"linux","arch":"amd64","sha256":"` + sum256(goArchive) + `","kind":"archive"}]},
		{"version":"go1.27.3","stable":true,"files":[]}]`)
	p.files["/go/go1.27.4.linux-amd64.tar.gz"] = goArchive

	jdk := tarGz(t, entry{name: "jdk-21.0.13+9/bin/java", body: "java", mode: 0o755})
	p.files["/adoptium/assets/latest/21/hotspot?architecture=x64&image_type=jdk&os=linux&vendor=eclipse"] = []byte(`[{"binary":{"package":{"link":"` +
		p.URL + `/temurin/jdk.tar.gz","checksum":"` + sum256(jdk) + `"}},"version":{"semver":"21.0.13+9.0.LTS","openjdk_version":"21.0.13+9-LTS"}}]`)
	p.files["/temurin/jdk.tar.gz"] = jdk

	sdk := tarGz(t, entry{name: "./dotnet", body: "host", mode: 0o755}, entry{name: "./sdk/10.0.404/x", body: "x"})
	p.files["/dotnet/release-metadata/10.0/releases.json"] = []byte(`{"releases":[{"sdk":{"version":"10.0.404","files":[
		{"name":"dotnet-sdk-linux-arm64.tar.gz","rid":"linux-arm64","url":"x","hash":"x"},
		{"name":"dotnet-sdk-linux-x64.tar.gz","rid":"linux-x64","url":"` + p.URL + `/dl/sdk.tar.gz","hash":"` + sum512(sdk) + `"}]}}]}`)
	p.files["/dl/sdk.tar.gz"] = sdk

	py := tarGz(t, entry{name: "python/bin/python3", body: "py", mode: 0o755})
	p.files["/pbs/latest-release.json"] = []byte(`{"tag":"20261110","asset_url_prefix":"` + p.URL + `/pbsdl"}`)
	p.files["/pbsdl/SHA256SUMS"] = []byte(strings.Join([]string{
		sum256(py) + "  cpython-3.13.18+20261110-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz",
		"aaaa  cpython-3.13.2+20261110-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz",
		"bbbb  cpython-3.13.19+20261110-x86_64-unknown-linux-gnu-freethreaded-install_only_stripped.tar.gz",
		"cccc  cpython-3.13.20+20261110-x86_64_v3-unknown-linux-gnu-install_only_stripped.tar.gz",
	}, "\n"))
	p.files["/pbsdl/cpython-3.13.18+20261110-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz"] = py

	for _, tc := range []struct{ sw, provider, line, version, check string }{
		{"go", "go", "1.27", "1.27.4", "bin/go"},
		{"java", "temurin", "21", "21.0.13+9-LTS", "bin/java"},
		{"dotnet", "dotnet", "10", "10.0.404", "dotnet"},
		{"python", "python-build-standalone", "3.13", "3.13.18", "bin/python3"},
	} {
		fakeVersion(t, "version "+tc.version)
		v, err := r.Install(context.Background(), io.Discard, tc.sw, tc.provider, tc.line)
		if err != nil || v != tc.version {
			t.Errorf("%s: %q %v", tc.provider, v, err)
			continue
		}
		if _, err := os.Stat(filepath.Join(r.Dir, tc.sw, tc.version, tc.check)); err != nil {
			t.Errorf("%s: %v", tc.provider, err)
		}
	}
	// Metadata without the line is a metadata error, not a crash.
	if _, err := r.Install(context.Background(), io.Discard, "go", "go", "1.30"); code(err) != CodeRuntimeMetadata {
		t.Fatalf("missing line: %v", err)
	}
}

func TestStandaloneThroughSoftwareOps(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := newProviderServer(t)
	f := &fakeSystem{}
	f.use(t)
	s, o, r := softwareFor(t, cat, "debian-13", trixie, "apt")
	s.Runtimes = p.runtimes(t)
	p.node(t, "24.22.3", nodeArchive(t, "24.22.3"))
	fakeVersion(t, "v24.22.3")

	op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "nodejs", Line: "24"})
	if op.State != helperapi.OpSucceeded || !strings.Contains(log, "checksum matches") {
		t.Fatalf("install: %+v\n%s", op.Error, log)
	}
	// The system library the official build needs comes from the system
	// repositories, once.
	if !f.ran("apt-get install -y --no-install-recommends libatomic1") {
		t.Fatalf("libatomic not installed: %v", f.cmds)
	}
	st, _ := s.Status(context.Background())
	if st.Installed["nodejs"]["24"][0].Version != "24.22.3" {
		t.Fatalf("status: %+v", st.Installed)
	}
	op, _ = runSoftwareOp(t, o, r, helperapi.ActionPkgRemove, helperapi.SoftwareRequest{Software: "nodejs", Line: "24"})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("remove: %+v", op.Error)
	}
	// Python needs no system library and does not touch apt.
	f.cmds = nil
	p.files["/pbs/latest-release.json"] = []byte(`{"tag":"1","asset_url_prefix":"` + p.URL + `/pbsdl"}`)
	py := tarGz(t, entry{name: "python/bin/python3", body: "py", mode: 0o755})
	p.files["/pbsdl/SHA256SUMS"] = []byte(sum256(py) + "  cpython-3.13.1+1-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz\n")
	p.files["/pbsdl/cpython-3.13.1+1-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz"] = py
	fakeVersion(t, "Python 3.13.1")
	if op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "python", Line: "3.13"}); op.State != helperapi.OpSucceeded || f.ran("apt-get") {
		t.Fatalf("python: %+v %v\n%s", op.Error, f.cmds, log)
	}
}
