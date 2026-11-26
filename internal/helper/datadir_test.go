// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// redoHeader is the start of an InnoDB redo log file: a format number, and
// the server that created it at byte 16.
func redoHeader(creator string) string {
	return "Phys" + strings.Repeat("\x00", 12) + creator + strings.Repeat("\x00", 48-16-len(creator)) + strings.Repeat("\x01", 400)
}

func TestReadDataRecord(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  dataRecord
		found bool
	}{
		"mariadb 11 and later": {map[string]string{"mariadb_upgrade_info": "12.3.4-MariaDB", "mysql_upgrade_info": "10.11.6-MariaDB"},
			dataRecord{"MariaDB", "12.3.4"}, true},
		"mariadb 10": {map[string]string{"mysql_upgrade_info": "10.6.23-MariaDB\n", "ib_logfile0": redoHeader("MariaDB 10.6.23")},
			dataRecord{"MariaDB", "10.6.23"}, true},
		"mysql 5.7": {map[string]string{"mysql_upgrade_info": "5.7.44"}, dataRecord{"MySQL", "5.7.44"}, true},
		// MySQL 8 writes no upgrade file: the newest redo log names it.
		"mysql 8": {map[string]string{"mysql.ibd": "x", "#innodb_redo/#ib_redo5": redoHeader("MySQL 8.0.46"),
			"#innodb_redo/#ib_redo12": redoHeader("MySQL 8.4.13"), "#innodb_redo/#ib_redo13_tmp": strings.Repeat("\x00", 512)},
			dataRecord{"MySQL", "8.4.13"}, true},
		"only the redo log":       {map[string]string{"ib_logfile0": redoHeader("MariaDB 11.8.9")}, dataRecord{"MariaDB", "11.8.9"}, true},
		"mysql without a version": {map[string]string{"mysql.ibd": "x"}, dataRecord{"MySQL", ""}, true},
		"not a version":           {map[string]string{"mysql_upgrade_info": "root:x:0:0:root:/root:/bin/bash"}, dataRecord{}, false},
		"empty":                   {map[string]string{}, dataRecord{}, false},
	} {
		dir := t.TempDir()
		for p, content := range tc.files {
			writeSystemFile(t, dir, p, content)
		}
		if got, ok := readData(dir, catalog.DataMySQL); got != tc.want || ok != tc.found {
			t.Errorf("%s: %+v %v", name, got, ok)
		}
	}
	if _, ok := readData(filepath.Join(t.TempDir(), "missing"), catalog.DataMySQL); ok {
		t.Error("a missing directory records something")
	}

	// The directory is the service account's: what a link there leads to
	// is never read, a file or a directory.
	dir, elsewhere := t.TempDir(), t.TempDir()
	writeSystemFile(t, elsewhere, "secret", "12.3.4-MariaDB")
	writeSystemFile(t, elsewhere, "redo/#ib_redo5", redoHeader("MySQL 9.7.1"))
	for name, target := range map[string]string{"mariadb_upgrade_info": "secret", "#innodb_redo": "redo", "ib_logfile0": "secret"} {
		if err := os.Symlink(filepath.Join(elsewhere, target), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := readData(dir, catalog.DataMySQL); ok {
		t.Fatalf("followed a link: %+v", got)
	}
}

func TestLineBefore(t *testing.T) {
	for _, tc := range []struct {
		line, version string
		want          bool
	}{
		{"11.8", "12.3.4", true}, {"10.11", "11.4.2", true}, {"10.6", "10.11.6", true}, {"8.0", "8.4.13", true},
		{"12.3", "12.3.4", false}, {"12.3", "11.8.9", false}, {"9.7", "8.4.13", false}, {"10.11", "10.6.23", false},
	} {
		if got := lineBefore(tc.line, tc.version); got != tc.want {
			t.Errorf("%s before %s: %v", tc.line, tc.version, got)
		}
	}
}

// dataCatalog has two products sharing one data directory, and one line
// each from the system on Debian 13.
func dataCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	entry := func(id, product, other string, lines ...string) string {
		quoted := `"` + strings.Join(lines, `", "`) + `"`
		var decl []string
		for _, l := range lines {
			decl = append(decl, `{ version = "`+l+`" }`)
		}
		return `id = "` + id + `"
name = "` + product + `"
category = "database"
provides = ["mysql-protocol"]
single = true
conflicts = ["` + other + `"]
data = { path = "/var/lib/mysql", format = "mysql", product = "` + product + `" }
lines = [` + strings.Join(decl, ", ") + `]
[[sources]]
method = "system"
releases = ["debian-13"]
lines = [` + quoted + `]
packages = ["` + id + `-server-{line}"]
`
	}
	cat, err := catalog.LoadFS(fstest.MapFS{
		"software/mdb.toml": {Data: []byte(entry("mdb", "MariaDB", "my", "12.3", "11.8"))},
		"software/my.toml":  {Data: []byte(entry("my", "MySQL", "mdb", "8.4"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// A line is not installed on the data that a newer version or the other
// product left: the older server cannot read it and may damage it.
func TestInstallChecksTheData(t *testing.T) {
	f := &fakeSystem{madison: strings.Join([]string{
		" mdb-server-12.3 | 1:12.3.4-1 | http://deb.debian.org/debian trixie/main amd64 Packages",
		" mdb-server-11.8 | 1:11.8.9-1 | http://deb.debian.org/debian trixie/main amd64 Packages",
		" my-server-8.4 | 8.4.13-1 | http://deb.debian.org/debian trixie/main amd64 Packages",
	}, "\n")}
	f.use(t)
	s, o, r := softwareFor(t, dataCatalog(t), "debian-13", trixie, "apt")
	data := func(files map[string]string) {
		t.Helper()
		_ = os.RemoveAll(filepath.Join(s.Root, "var/lib/mysql"))
		for p, content := range files {
			writeSystemFile(t, s.Root, "/var/lib/mysql/"+p, content)
		}
	}
	refused := func(req helperapi.SoftwareRequest, code, product, version string) {
		t.Helper()
		f.cmds = nil
		e := opError(t, o, r, req)
		if e.Code != code || e.Params["path"] != "/var/lib/mysql" || e.Params["product"] != product || e.Params["version"] != version {
			t.Fatalf("%s %s: %+v", req.Software, req.Line, e)
		}
		if f.ran("apt-get") {
			t.Fatalf("%s %s: the package manager ran: %v", req.Software, req.Line, f.cmds)
		}
	}

	// MariaDB 12.3 was here and is removed: 11.8 would start on its data.
	data(map[string]string{"mariadb_upgrade_info": "12.3.4-MariaDB", "ib_logfile0": redoHeader("MariaDB 12.3.4")})
	refused(helperapi.SoftwareRequest{Software: "mdb", Line: "11.8"}, "pkg.data_newer", "MariaDB", "12.3.4")
	refused(helperapi.SoftwareRequest{Software: "my", Line: "8.4"}, "pkg.data_other", "MariaDB", "12.3.4")
	// MySQL's data, MariaDB asked for.
	data(map[string]string{"mysql.ibd": "x", "#innodb_redo/#ib_redo7": redoHeader("MySQL 8.4.13")})
	refused(helperapi.SoftwareRequest{Software: "mdb", Line: "12.3"}, "pkg.data_other", "MySQL", "8.4.13")

	// The same line again, a newer line (the server upgrades the data) and
	// an empty directory are fine.
	for _, tc := range []struct {
		files    map[string]string
		sw, line string
	}{
		{map[string]string{"mysql.ibd": "x", "#innodb_redo/#ib_redo7": redoHeader("MySQL 8.4.13")}, "my", "8.4"},
		{map[string]string{"mariadb_upgrade_info": "11.8.9-MariaDB"}, "mdb", "12.3"},
		{map[string]string{}, "mdb", "11.8"},
	} {
		data(tc.files)
		f.installed = map[string]string{}
		op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: tc.sw, Line: tc.line})
		if op.State != helperapi.OpSucceeded {
			t.Fatalf("%s %s on %v: %+v\n%s", tc.sw, tc.line, tc.files, op.Error, log)
		}
		// Installed, it runs on its data already: installing it again
		// does not look.
		if tc.sw == "mdb" && tc.line == "11.8" {
			data(map[string]string{"mariadb_upgrade_info": "12.3.4-MariaDB"})
			if op, log := runSoftwareOp(t, o, r, helperapi.ActionPkgInstall, helperapi.SoftwareRequest{Software: "mdb", Line: "11.8"}); op.State != helperapi.OpSucceeded {
				t.Fatalf("installed line again: %+v\n%s", op.Error, log)
			}
		}
		if _, err := s.Status(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
