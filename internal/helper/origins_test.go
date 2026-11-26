// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/platform"
)

// originsCatalog has a database whose line 2.1 comes from the system and
// from a vendor repository under the same package names, a newer line from
// the vendor only, and a store that cannot run next to it.
func originsCatalog(t *testing.T, fpr string) *catalog.Catalog {
	t.Helper()
	files := fstest.MapFS{
		"repos/vend.toml": {Data: []byte(fmt.Sprintf(`id = "vend"
name = "Vendor"
official = true
releases = ["debian-13", "el-9"]
pin_line = true
[apt]
uri = "https://repo.vendor.example/{distro}"
suite = "{codename}"
components = ["main"]
key = { url = "https://repo.vendor.example/key.asc", fingerprints = ["%[1]s"] }
[dnf]
baseurl = "https://repo.vendor.example/el/{major}/$basearch"
key = { url = "https://repo.vendor.example/key.asc", fingerprints = ["%[1]s"] }
module_hotfixes = true
`, fpr))},
		"software/db.toml": {Data: []byte(`id = "db"
name = "DB"
category = "database"
provides = ["x"]
single = true
conflicts = ["kv"]
lines = [{ version = "3.0" }, { version = "2.1" }]
[[sources]]
method = "vendor"
repo = "vend"
releases = ["debian-13", "el-9"]
lines = ["3.0", "2.1"]
packages = ["db-server", "db-tools"]
services = ["db"]
[[sources]]
method = "system"
releases = ["debian-13"]
lines = ["2.1"]
packages = ["db-server", "db-tools"]
services = ["db"]
`)},
		"software/kv.toml": {Data: []byte(`id = "kv"
name = "KV"
category = "cache"
provides = ["y"]
conflicts = ["db"]
lines = [{ version = "1" }]
[[sources]]
method = "system"
releases = ["debian-13"]
lines = ["1"]
packages = ["kv"]
`)},
	}
	cat, err := catalog.LoadFS(files)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func opError(t *testing.T, o *Ops, r *Registry, req helperapi.SoftwareRequest) *helperapi.Error {
	t.Helper()
	op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, req)
	if op.State != helperapi.OpFailed || op.Error == nil {
		t.Fatalf("%s %s from %q installed:\n%s", req.Software, req.Line, req.Origin, log)
	}
	return op.Error
}

func TestOriginsAreRecordedAndChecked(t *testing.T) {
	key, fpr := testKey(t, 1)
	cat := originsCatalog(t, fpr)
	f := &fakeSystem{madison: strings.Join([]string{
		" db-server | 3.0.2-1 | https://repo.vendor.example/debian trixie/main amd64 Packages",
		" db-server | 2.1.9-1 | https://repo.vendor.example/debian trixie/main amd64 Packages",
		" db-server | 2.1.4-3 | http://deb.debian.org/debian trixie/main amd64 Packages",
		" db-tools | 3.0.2-1 | https://repo.vendor.example/debian trixie/main amd64 Packages",
		" db-tools | 2.1.9-1 | https://repo.vendor.example/debian trixie/main amd64 Packages",
		" db-tools | 2.1.4-3 | http://deb.debian.org/debian trixie/main amd64 Packages",
		" kv | 1.0.1-1 | http://deb.debian.org/debian trixie/main amd64 Packages",
	}, "\n")}
	f.use(t)
	s, o, r := softwareFor(t, cat, "debian-13", trixie, "apt")
	s.HTTP = &http.Client{Transport: keyServer{"https://repo.vendor.example/key.asc": key}}
	ctx := context.Background()
	repoFiles := []string{"etc/apt/sources.list.d/cairn-vend-21.sources", "etc/apt/preferences.d/cairn-vend-21", "etc/apt/keyrings/cairn-vend-21.gpg"}

	// The default source of 2.1 is the system's.
	op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "db", Line: "2.1"})
	var res helperapi.InstallResult
	_ = json.Unmarshal(op.Result, &res)
	if op.State != helperapi.OpSucceeded || res.Origin != catalog.OriginSystem || res.Packages[0].Version != "2.1.4-3" {
		t.Fatalf("install: %+v %+v\n%s", op.Error, res, log)
	}
	if got := s.recordedOrigin("db", "2.1"); got != catalog.OriginSystem {
		t.Fatalf("recorded %q", got)
	}

	// The same line from the other source, another line, conflicting
	// software: each has to wait for the installed line to be removed.
	if e := opError(t, o, r, helperapi.SoftwareRequest{Software: "db", Line: "2.1", Origin: "vend"}); e.Code != helperapi.CodePkgOtherOrigin || e.Params["installed"] != catalog.OriginSystem {
		t.Fatalf("other origin: %+v", e)
	}
	if e := opError(t, o, r, helperapi.SoftwareRequest{Software: "db", Line: "3.0"}); e.Code != helperapi.CodePkgOtherLine || e.Params["installed"] != "2.1" {
		t.Fatalf("other line: %+v", e)
	}
	if e := opError(t, o, r, helperapi.SoftwareRequest{Software: "kv", Line: "1"}); e.Code != helperapi.CodePkgConflictsWith || e.Params["other"] != "db" {
		t.Fatalf("conflict: %+v", e)
	}
	if e := opError(t, o, r, helperapi.SoftwareRequest{Software: "kv", Line: "1", Origin: "vend"}); e.Code != helperapi.CodePkgOriginUnknown {
		t.Fatalf("unknown origin: %+v", e)
	}

	op, log = runSoftwareOp(t, o, r, helperapi.ActionPkgRemove, helperapi.SoftwareRequest{Software: "db", Line: "2.1"})
	if op.State != helperapi.OpSucceeded || s.recordedOrigin("db", "2.1") != "" {
		t.Fatalf("remove: %+v\n%s", op.Error, log)
	}

	// From the vendor now: same package names, its own builds, pinned to
	// the line since the repository serves every line.
	op, log = runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "db", Line: "2.1", Origin: "vend"})
	if op.State != helperapi.OpSucceeded || f.installed["db-server"] != "2.1.9-1" || f.installed["db-tools"] != "2.1.9-1" {
		t.Fatalf("install from vend: %+v %v\n%s", op.Error, f.installed, log)
	}
	prefs := readText(t, filepath.Join(s.Root, "etc/apt/preferences.d/cairn-vend-21"))
	if !strings.Contains(prefs, "Package: db-server db-tools\nPin: version /^([0-9]+:)?2\\.1\\./\nPin-Priority: 600\n") {
		t.Fatalf("preferences:\n%s", prefs)
	}
	st, _ := s.Status(ctx)
	if st.Origins["db"]["2.1"] != "vend" || len(st.Installed["db"]["2.1"]) != 2 {
		t.Fatalf("status: %+v %+v", st.Installed, st.Origins)
	}
	// What acts on the installed line goes by its origin.
	if _, got, err := s.installedOffer(ctx, "db", "2.1"); err != nil || got.Origin != "vend" {
		t.Fatalf("installed offer: %+v %v", got, err)
	}
	// Without the record, the line's sources are tried in order: the
	// system's comes first under the same names.
	_ = os.Remove(s.originsPath())
	if st, _ := s.Status(ctx); st.Origins["db"]["2.1"] != catalog.OriginSystem {
		t.Fatalf("status without a record: %+v", st.Origins)
	}
	if err := s.setOrigin("db", "2.1", "vend"); err != nil {
		t.Fatal(err)
	}

	// Removing the last line from the repository removes the repository.
	op, log = runSoftwareOp(t, o, r, helperapi.ActionPkgRemove, helperapi.SoftwareRequest{Software: "db", Line: "2.1"})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("remove: %+v\n%s", op.Error, log)
	}
	for _, p := range repoFiles {
		if _, err := os.Stat(filepath.Join(s.Root, p)); !os.IsNotExist(err) {
			t.Errorf("%s left behind:\n%s", p, log)
		}
	}

	// An install set may not hold conflicting software either.
	ref, err := call[helperapi.OpRef](t, r, helperapi.ActionPkgInstallSet, helperapi.InstallSetRequest{Items: []helperapi.SoftwareRequest{
		{Software: "db", Line: "3.0"}, {Software: "kv", Line: "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if op := waitOp(t, o, ref.ID, helperapi.OpSucceeded, helperapi.OpFailed); op.Error == nil || op.Error.Code != helperapi.CodePkgConflictsWith {
		t.Fatalf("install set: %+v", op.Error)
	}
}

func TestVendorLinesOnDNF(t *testing.T) {
	key, fpr := testKey(t, 1)
	cat := originsCatalog(t, fpr)
	rocky := platform.OSRelease{ID: "rocky", VersionID: "9.6"}
	// One repository, every line: the newest build of the line is chosen.
	f := &fakeSystem{repoquery: "db-server 3.1.0-1\ndb-server 3.0.5-1\ndb-server 3.0.12-1\ndb-tools 3.0.12-1\ndb-tools 3.1.0-1\n"}
	f.use(t)
	s, o, r := softwareFor(t, cat, "el-9", rocky, "dnf")
	s.HTTP = &http.Client{Transport: keyServer{"https://repo.vendor.example/key.asc": key}}

	op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "db", Line: "3.0"})
	if op.State != helperapi.OpSucceeded || !f.ran("dnf install -y db-server-3.0.12-1 db-tools-3.0.12-1") ||
		!f.ran("query dnf repoquery --qf %{name} %{evr}\n --repo cairn-vend-30 db-server db-tools") {
		t.Fatalf("install: %+v %v\n%s", op.Error, f.cmds, log)
	}
	repo := readText(t, filepath.Join(s.Root, "etc/yum.repos.d/cairn-vend-30.repo"))
	if !strings.Contains(repo, "baseurl=https://repo.vendor.example/el/9/$basearch\n") || !strings.Contains(repo, "module_hotfixes=1\n") ||
		!strings.Contains(repo, "includepkgs=db-server-3.0.* db-tools-3.0.*\n") {
		t.Fatalf("repository:\n%s", repo)
	}
}

func TestAptLinePattern(t *testing.T) {
	if got := aptLinePattern("8.10"); got != `/^([0-9]+:)?8\.10\./` {
		t.Fatal(got)
	}
}

// A package that came with a line and depends on it goes with it.
func TestRemoveTakesCompanions(t *testing.T) {
	files := fstest.MapFS{"software/db.toml": {Data: []byte(`id = "db"
name = "DB"
category = "database"
provides = ["x"]
lines = [{ version = "2.1" }]
[[sources]]
method = "system"
releases = ["debian-13"]
lines = ["2.1"]
packages = ["db-server"]
companions = ["db-compat", "db-absent"]
`)}}
	cat, err := catalog.LoadFS(files)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{installed: map[string]string{"db-server": "2.1.4-1", "db-compat": "2.1.4-1"},
		removeSim: "Remv db-server [2.1.4-1]\nRemv db-compat [2.1.4-1]\n"}
	f.use(t)
	_, o, r := softwareFor(t, cat, "debian-13", trixie, "apt")
	op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgRemove, helperapi.SoftwareRequest{Software: "db", Line: "2.1"})
	if op.State != helperapi.OpSucceeded || !f.ran("apt-get remove -y db-server db-compat") || f.installed["db-compat"] != "" {
		t.Fatalf("remove: %+v %v\n%s", op.Error, f.cmds, log)
	}
}

// metaCatalog has a database whose line's package is a metapackage: the
// server and its service are in db-server-2.1, which the line names as its
// companion when withServer is set; and a database whose service is shared
// by all its lines (Debian's postgresql.service).
func metaCatalog(t *testing.T, fpr string, withServer bool) *catalog.Catalog {
	t.Helper()
	companions := ""
	if withServer {
		companions = "companions = [\"db-server-{line}\"]\n"
	}
	files := fstest.MapFS{
		"repos/vend.toml": {Data: []byte(fmt.Sprintf(`id = "vend"
name = "Vendor"
official = true
releases = ["debian-13"]
[apt]
uri = "https://repo.vendor.example/{distro}"
suite = "{codename}"
components = ["main"]
key = { url = "https://repo.vendor.example/key.asc", fingerprints = ["%s"] }
`, fpr))},
		"software/db.toml": {Data: []byte(`id = "db"
name = "DB"
category = "database"
provides = ["x"]
lines = [{ version = "2.1" }]
[[sources]]
method = "vendor"
repo = "vend"
releases = ["debian-13"]
lines = ["2.1"]
packages = ["db"]
` + companions + `services = ["db"]
`)},
		"software/pg.toml": {Data: []byte(`id = "pg"
name = "PG"
category = "database"
provides = ["y"]
lines = [{ version = "16" }]
[[sources]]
method = "system"
releases = ["debian-13"]
lines = ["16"]
packages = ["pg-{line}"]
services = ["pg"]
`)},
	}
	cat, err := catalog.LoadFS(files)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// Removing a line checks that its service went with it: a server left in
// a package the line does not name keeps running, and is reported, with
// its repository kept for its updates.
func TestRemoveChecksTheServiceIsGone(t *testing.T) {
	key, fpr := testKey(t, 1)
	for _, withServer := range []bool{false, true} {
		f := &fakeSystem{madison: " db | 2.1.9-1 | https://repo.vendor.example/debian trixie/main amd64 Packages\n",
			owners: map[string]string{"/usr/lib/systemd/system/db.service": "db-server-2.1", "/lib/systemd/system/pg.service": "pg-common"}}
		f.use(t)
		s, o, r := softwareFor(t, metaCatalog(t, fpr, withServer), "debian-13", trixie, "apt")
		s.HTTP = &http.Client{Transport: keyServer{"https://repo.vendor.example/key.asc": key}}
		op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "db", Line: "2.1"})
		if op.State != helperapi.OpSucceeded {
			t.Fatalf("install: %+v\n%s", op.Error, log)
		}
		// What the metapackage brought: the server and its unit.
		f.installed["db-server-2.1"] = "2.1.9-1"
		writeSystemFile(t, s.Root, "/usr/lib/systemd/system/db.service", "[Service]\n")
		op, log = runSoftwareOp(t, o, r, helperapi.ActionPkgRemove, helperapi.SoftwareRequest{Software: "db", Line: "2.1"})
		repoLeft := fileExists(filepath.Join(s.Root, "etc/apt/sources.list.d/cairn-vend.sources"))
		if !withServer {
			if op.State != helperapi.OpFailed || op.Error.Code != "pkg.remove_incomplete" || op.Error.Params["units"] != "db.service" ||
				op.Error.Params["packages"] != "db-server-2.1" || !repoLeft || !f.ran("apt-get remove -y db") {
				t.Fatalf("server left: %+v repository kept %v\n%s", op.Error, repoLeft, log)
			}
			continue
		}
		if op.State != helperapi.OpSucceeded || !f.ran("apt-get remove -y db db-server-2.1") || repoLeft {
			t.Fatalf("with the server: %+v repository kept %v\n%s", op.Error, repoLeft, log)
		}

		// A unit all lines share comes from a package of its own version.
		f.installed["pg-16"], f.installed["pg-common"] = "16.4-1", "283"
		writeSystemFile(t, s.Root, "/lib/systemd/system/pg.service", "[Service]\n")
		op, log = runSoftwareOp(t, o, r, helperapi.ActionPkgRemove, helperapi.SoftwareRequest{Software: "pg", Line: "16"})
		if op.State != helperapi.OpSucceeded || f.installed["pg-common"] == "" {
			t.Fatalf("shared unit: %+v\n%s", op.Error, log)
		}
	}
}
