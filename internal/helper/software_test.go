// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/pgpkey"
	"github.com/Kobayashi-classmate/Cairn/internal/platform"
)

// fakeSystem stands in for apt, dnf, dpkg and rpm.
type fakeSystem struct {
	mu        sync.Mutex
	cmds      []string
	installed map[string]string
	madison   string
	repoquery string
	modules   string
	systemd   bool
	// failInstall makes "apt-get install" fail when an argument starts with
	// it; printURIs answers "apt-get install --print-uris".
	failInstall string
	printURIs   string
	// failPrefix makes any command starting with it fail.
	failPrefix string
}

func (f *fakeSystem) use(t *testing.T) {
	t.Helper()
	oldRun, oldQuery, oldSystemd := runCommand, queryCommand, systemdRunning
	t.Cleanup(func() { runCommand, queryCommand, systemdRunning = oldRun, oldQuery, oldSystemd })
	if f.installed == nil {
		f.installed = map[string]string{}
	}
	systemdRunning = func() bool { return f.systemd }
	runCommand = func(_ context.Context, log io.Writer, name string, args ...string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		cmd := name + " " + strings.Join(args, " ")
		f.cmds = append(f.cmds, cmd)
		_, _ = fmt.Fprintf(log, "$ %s\n", cmd)
		if f.failPrefix != "" && strings.HasPrefix(cmd, f.failPrefix) {
			_, _ = fmt.Fprintf(log, "[emerg] test failure of %s\n", name)
			return &helperapi.Error{Code: "exec.failed", Params: map[string]any{"command": name}}
		}
		switch {
		case name == "apt-get" && args[0] == "install":
			for _, a := range args {
				if f.failInstall != "" && strings.HasPrefix(a, f.failInstall) {
					return &helperapi.Error{Code: "pkg.command_failed", Params: map[string]any{"command": "apt-get"}}
				}
			}
			for _, a := range args {
				if p, v, ok := strings.Cut(a, "="); ok && !strings.HasPrefix(a, "Dpkg") {
					f.installed[p] = v
				}
			}
		case name == "dnf" && args[0] == "install":
			for _, a := range args[2:] {
				// name-evr where name is known from repoquery
				for _, fld := range strings.Split(f.repoquery, "\n") {
					nv := strings.Fields(fld)
					if len(nv) == 2 && a == nv[0]+"-"+nv[1] {
						f.installed[nv[0]] = nv[1]
					}
				}
			}
		case (name == "apt-get" || name == "dnf") && args[0] == "remove":
			for _, p := range args[2:] {
				delete(f.installed, p)
			}
		}
		return nil
	}
	queryCommand = func(_ context.Context, name string, args ...string) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.cmds = append(f.cmds, "query "+name+" "+strings.Join(args, " "))
		switch name {
		case "apt-cache":
			return f.madison, nil
		case "apt-get":
			return f.printURIs, nil
		case "dnf":
			if args[0] == "module" {
				return f.modules, nil
			}
			return f.repoquery, nil
		case "dpkg-query", "rpm":
			var b strings.Builder
			for _, a := range args[3:] {
				if v, ok := f.installed[a]; ok {
					fmt.Fprintf(&b, "%s\t%s\tii \n", a, v)
				}
			}
			return b.String(), nil
		}
		return "", fmt.Errorf("unexpected command %s", name)
	}
}

func (f *fakeSystem) ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cmds {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func softwareFor(t *testing.T, cat *catalog.Catalog, release string, osr platform.OSRelease, manager string) (*Software, *Ops, *Registry) {
	t.Helper()
	chain, err := platform.Chain(release)
	if err != nil {
		t.Fatal(err)
	}
	s := &Software{Catalog: cat, Manager: manager, OS: osr, Chain: chain, Arch: "amd64", Root: t.TempDir(), Log: slog.New(slog.DiscardHandler)}
	o, r := newTestOps(t)
	RegisterSoftware(r, o, s)
	return s, o, r
}

func runSoftwareOp(t *testing.T, o *Ops, r *Registry, action string, req helperapi.SoftwareRequest) (helperapi.Op, string) {
	t.Helper()
	ref, err := call[helperapi.OpRef](t, r, action, req)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	op := waitOp(t, o, ref.ID, helperapi.OpSucceeded, helperapi.OpFailed)
	res, _ := o.Get(ref.ID, 0)
	return op, res.Log
}

var trixie = platform.OSRelease{ID: "debian", VersionID: "13", Codename: "trixie"}

func TestInstallSystemLineAndStatus(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{systemd: true, madison: strings.Join([]string{
		" php8.4-fpm | 8.4.99-1+0~deb.sury.org | https://packages.sury.org/php trixie/main amd64 Packages",
		" php8.4-fpm | 8.4.27-1~deb13u1 | http://deb.debian.org/debian trixie/main amd64 Packages",
		" php8.4-cli | 8.4.99-1+0~deb.sury.org | https://packages.sury.org/php trixie/main amd64 Packages",
		" php8.4-cli | 8.4.27-1~deb13u1 | http://deb.debian.org/debian trixie/main amd64 Packages",
	}, "\n")}
	f.use(t)
	s, o, r := softwareFor(t, cat, "debian-13", trixie, "apt")

	op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "php", Line: "8.4"})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("install: %+v\n%s", op.Error, log)
	}
	var res helperapi.InstallResult
	_ = json.Unmarshal(op.Result, &res)
	// The system line comes from the distribution even though the vendor
	// repository has a newer build of the same package.
	if res.Method != "system" || len(res.Packages) != 2 || res.Packages[0].Version != "8.4.27-1~deb13u1" || !res.ServicesStarted {
		t.Fatalf("result: %+v", res)
	}
	if !f.ran("apt-get update") || !f.ran("apt-get install -y --no-install-recommends -o Dpkg::Options::=--force-confold php8.4-fpm=8.4.27-1~deb13u1 php8.4-cli=8.4.27-1~deb13u1") ||
		!f.ran("systemctl enable --now php8.4-fpm") {
		t.Fatalf("commands: %v", f.cmds)
	}
	if entries, _ := os.ReadDir(filepath.Join(s.Root, "etc")); len(entries) != 0 {
		t.Fatal("a system line must not add repository files")
	}

	st, err := s.Status(context.Background())
	if err != nil || len(st.Installed["php"]["8.4"]) != 2 || len(st.Installed["php"]) != 1 {
		t.Fatalf("status: %+v %v", st, err)
	}

	// Removing keeps configuration and data (remove, not purge).
	op, _ = runSoftwareOp(t, o, r, helperapi.ActionPkgRemove, helperapi.SoftwareRequest{Software: "php", Line: "8.4"})
	if op.State != helperapi.OpSucceeded || !f.ran("apt-get remove -y php8.4-fpm php8.4-cli") {
		t.Fatalf("remove: %+v %v", op.Error, f.cmds)
	}
	op, _ = runSoftwareOp(t, o, r, helperapi.ActionPkgRemove, helperapi.SoftwareRequest{Software: "php", Line: "8.4"})
	if op.Error == nil || op.Error.Code != helperapi.CodePkgNotInstalled {
		t.Fatalf("second remove: %+v", op.Error)
	}

	// Lines that are not offered here, and malformed requests, are refused.
	op, _ = runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "php", Line: "7.4"})
	if op.Error == nil || op.Error.Code != helperapi.CodePkgNotAvailable {
		t.Fatalf("end-of-life line: %+v", op.Error)
	}
	if _, err := call[helperapi.OpRef](t, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "php; rm -rf /", Line: "8.4"}); code(err) != "validation.invalid" {
		t.Fatalf("bad request: %v", err)
	}
}

// testKey returns a synthetic key file and its fingerprint.
func testKey(t *testing.T, seed byte) ([]byte, string) {
	t.Helper()
	body := []byte{4, 0x65, 0, 0, seed, 1, 0x00, 0x10, seed, 1, 0x00, 0x11, 1, 0, 1}
	file := append([]byte{0xC6, byte(len(body))}, body...)
	fprs, err := pgpkey.Fingerprints(file)
	if err != nil {
		t.Fatal(err)
	}
	return pgpkey.Armor(file), fprs[0]
}

type keyServer map[string][]byte

func (k keyServer) RoundTrip(r *http.Request) (*http.Response, error) {
	data, ok := k[r.URL.String()]
	if !ok {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(bytes.NewReader(nil)), Request: r}, nil
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Request: r}, nil
}

func TestInstallVendorLineVerifiesKey(t *testing.T) {
	key, fpr := testKey(t, 1)
	other, _ := testKey(t, 2)
	files := fstest.MapFS{
		"repos/vend.toml": {Data: []byte(fmt.Sprintf(`id = "vend"
name = "Vendor"
official = true
releases = ["debian-13"]
[apt]
uri = "https://repo.vendor.example/{distro}"
suite = "{codename}"
components = ["db-{line}"]
key = { url = "https://repo.vendor.example/key.asc", fingerprints = ["%s"] }
prefer = ["db-common"]
`, fpr))},
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
services = ["db"]
`)},
	}
	cat, err := catalog.LoadFS(files)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{madison: " db-server | 2.1.9-1 | https://repo.vendor.example/debian trixie/db-2.1 amd64 Packages\n" +
		" db-server | 2.0.1-1 | http://deb.debian.org/debian trixie/main amd64 Packages\n"}
	f.use(t)
	s, o, r := softwareFor(t, cat, "debian-13", trixie, "apt")
	s.HTTP = &http.Client{Transport: keyServer{"https://repo.vendor.example/key.asc": other}}

	// A key that is not the pinned one is refused before anything is written.
	op, _ := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "db", Line: "2.1"})
	if op.Error == nil || op.Error.Code != helperapi.CodePkgKeyMismatch {
		t.Fatalf("wrong key accepted: %+v", op.Error)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "etc/apt/sources.list.d/cairn-vend-21.sources")); !os.IsNotExist(err) {
		t.Fatal("repository written despite the key mismatch")
	}

	s.HTTP = &http.Client{Transport: keyServer{"https://repo.vendor.example/key.asc": key}}
	op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "db", Line: "2.1"})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("install: %+v\n%s", op.Error, log)
	}
	sources, _ := os.ReadFile(filepath.Join(s.Root, "etc/apt/sources.list.d/cairn-vend-21.sources"))
	prefs, _ := os.ReadFile(filepath.Join(s.Root, "etc/apt/preferences.d/cairn-vend-21"))
	keyring, _ := os.ReadFile(filepath.Join(s.Root, "etc/apt/keyrings/cairn-vend-21.gpg"))
	if !strings.Contains(string(sources), "URIs: https://repo.vendor.example/debian\nSuites: trixie\nComponents: db-2.1\nSigned-By: /etc/apt/keyrings/cairn-vend-21.gpg") {
		t.Fatalf("sources:\n%s", sources)
	}
	if !strings.Contains(string(prefs), "Pin: origin repo.vendor.example\nPin-Priority: 100") ||
		!strings.Contains(string(prefs), "Package: db-common\nPin: origin repo.vendor.example\nPin-Priority: 600") {
		t.Fatalf("preferences:\n%s", prefs)
	}
	if pgpkey.IsArmored(keyring) || len(keyring) == 0 {
		t.Fatal("apt keyring must be stored in binary form")
	}
	// The vendor's version is chosen, not the system's older one, and
	// without systemd the services are left alone.
	if !f.ran("apt-get install -y --no-install-recommends -o Dpkg::Options::=--force-confold db-server=2.1.9-1") || f.ran("systemctl") {
		t.Fatalf("commands: %v", f.cmds)
	}
	if !strings.Contains(log, "systemd is not running; services not started: db") {
		t.Fatalf("log: %s", log)
	}
}

func TestDNFStreamsAndStatus(t *testing.T) {
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	rocky := platform.OSRelease{ID: "rocky", VersionID: "9.3"}
	f := &fakeSystem{modules: "Rocky Linux 9 - AppStream\nName Stream Profiles Summary\nphp  8.1 [e] common\n",
		installed: map[string]string{"nginx": "2:1.26.3-1.module+el9.6.0", "nginx-mod-stream": "2:1.26.3-1.module+el9.6.0"}}
	f.use(t)
	s, o, r := softwareFor(t, cat, "el-9", rocky, "dnf")

	// Several nginx lines share package names; only the installed one counts.
	st, _ := s.Status(context.Background())
	if len(st.Installed["nginx"]) != 1 || st.Installed["nginx"]["1.26"] == nil {
		t.Fatalf("status: %+v", st.Installed)
	}
	// Another stream of the module is already enabled.
	op, _ := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "php", Line: "8.3"})
	if op.Error == nil || op.Error.Code != helperapi.CodePkgStreamConflict || op.Error.Params["enabled"] != "8.1" {
		t.Fatalf("stream conflict: %+v", op.Error)
	}
	// Without a conflict the stream is enabled, then the exact versions are installed.
	f.modules = ""
	f.repoquery = "php-fpm 8.3.19-1.module+el9.6.0\nphp-cli 8.3.19-1.module+el9.6.0\n"
	op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "php", Line: "8.3"})
	if op.State != helperapi.OpSucceeded || !f.ran("dnf module enable -y php:8.3") ||
		!f.ran("dnf install -y php-fpm-8.3.19-1.module+el9.6.0 php-cli-8.3.19-1.module+el9.6.0") {
		t.Fatalf("install: %+v %v\n%s", op.Error, f.cmds, log)
	}
}

func TestLineMatches(t *testing.T) {
	for v, want := range map[string]bool{
		"8.4.16-1~deb13u1": true, "2:8.4.1-1": true,
		"8.40.1": false, "18.1-1": false, "8.4": false, "x:8.4.1": false,
	} {
		if got := lineMatches(v, "8.4"); got != want {
			t.Errorf("lineMatches(%q, 8.4) = %v", v, got)
		}
	}
	if !lineMatches("1:10.11.6-0+deb12u1", "10.11") || lineMatches("16.4-1", "1") {
		t.Error("epoch or prefix handling")
	}
}
