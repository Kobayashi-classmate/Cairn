// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"errors"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// The data a line finds when it is installed. Removing a line keeps its
// data, and the lines of a software (and software that conflicts with it)
// share the data directory: a line installed after another was removed
// starts on what that one left. The servers upgrade data written by an
// older version, not the other way round, and do not read each other's
// data, so an install that would start an older version or another
// product on the directory is refused before anything changes.

// dataRecord is what a data directory says about the server that last
// used it.
type dataRecord struct {
	Product string // as the server names itself ("MariaDB", "MySQL")
	Version string // its full version ("11.8.9"), "" when not recorded
}

// maxDataMarker is how much of a file that records a version is read.
const maxDataMarker = 512

var (
	// "11.8.9-MariaDB" (mariadb_upgrade_info, mysql_upgrade_info), or
	// "5.7.44" for MySQL before 8.0.
	upgradeInfo = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+)(-MariaDB)?`)
	// The creator in the header of an InnoDB redo log: "MariaDB 12.3.4",
	// "MySQL 8.4.13".
	redoCreator = regexp.MustCompile(`(MariaDB|MySQL) ([0-9]+\.[0-9]+\.[0-9]+)`)
	redoFile    = regexp.MustCompile(`^#ib_redo([0-9]+)$`)
)

// readData reads what the data directory at path (in format) says about
// the server that last used it; false when it records nothing (no
// directory, an empty one, a format it does not know). The directory
// belongs to the service account: everything in it is opened through it,
// never through a link, and only a version is taken from what is read.
func readData(path, format string) (dataRecord, bool) {
	if format != catalog.DataMySQL {
		return dataRecord{}, false
	}
	dir, err := os.OpenRoot(path)
	if err != nil {
		return dataRecord{}, false
	}
	defer func() { _ = dir.Close() }()
	// MariaDB writes the version that last upgraded the directory (11.0
	// and later under its own name, the older one still read).
	for _, name := range []string{"mariadb_upgrade_info", "mysql_upgrade_info"} {
		if m := upgradeInfo.FindStringSubmatch(readMarker(dir, name)); m != nil {
			if m[2] != "" {
				return dataRecord{Product: "MariaDB", Version: m[1]}, true
			}
			return dataRecord{Product: "MySQL", Version: m[1]}, true
		}
	}
	// MySQL 8 writes no such file. Its redo log files name the version
	// that created them: the newest one, the version that ran last.
	if redo, err := subDir(dir, "#innodb_redo"); err == nil {
		defer func() { _ = redo.Close() }()
		names, _ := dirNames(redo)
		slices.SortFunc(names, func(a, b string) int { return redoNumber(b) - redoNumber(a) })
		for _, name := range names {
			if redoNumber(name) < 0 {
				continue
			}
			if m := redoCreator.FindStringSubmatch(readMarker(redo, name)); m != nil {
				return dataRecord{Product: m[1], Version: m[2]}, true
			}
		}
	}
	if m := redoCreator.FindStringSubmatch(readMarker(dir, "ib_logfile0")); m != nil {
		return dataRecord{Product: m[1], Version: m[2]}, true
	}
	// MySQL's data dictionary, whatever the version.
	if info, err := dir.Lstat("mysql.ibd"); err == nil && info.Mode().IsRegular() {
		return dataRecord{Product: "MySQL"}, true
	}
	return dataRecord{}, false
}

// redoNumber is the number of a redo log file ("#ib_redo12"), -1 for
// anything else (the spare "#ib_redo13_tmp" files have no header yet).
func redoNumber(name string) int {
	m := redoFile.FindStringSubmatch(name)
	if m == nil {
		return -1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return -1
	}
	return n
}

// readMarker returns the start of the regular file name in dir, "" when
// it is not one or cannot be read.
func readMarker(dir *os.Root, name string) string {
	f, err := openNoLink(dir, name)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	buf := make([]byte, maxDataMarker)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return ""
	}
	return string(buf[:n])
}

// checkData refuses to install line o of sw on a data directory that a
// newer version of it or another product used last: an older server does
// not read what a newer one wrote, and may damage it; another product does
// not read it at all. Data an older version wrote is upgraded by the
// server itself.
func (s *Software) checkData(sw *catalog.Software, o catalog.Offer) error {
	if sw.Data == nil {
		return nil
	}
	rec, ok := readData(s.path(sw.Data.Path), sw.Data.Format)
	if !ok {
		return nil
	}
	params := map[string]any{"software": sw.ID, "line": o.Line, "path": sw.Data.Path, "product": rec.Product, "version": rec.Version}
	if !strings.EqualFold(rec.Product, sw.Data.Product) {
		return pkgErr(helperapi.CodePkgDataOther, params)
	}
	if rec.Version != "" && lineBefore(o.Line, rec.Version) {
		return pkgErr(helperapi.CodePkgDataNewer, params)
	}
	return nil
}

// lineBefore reports whether line ("11.8") is older than the line of
// version ("12.3.4"): their major and minor numbers compared as numbers.
func lineBefore(line, version string) bool {
	a, b := strings.Split(line, "."), strings.Split(version, ".")
	for i := 0; i < 2; i++ {
		var x, y int
		if i < len(a) {
			x, _ = strconv.Atoi(a[i])
		}
		if i < len(b) {
			y, _ = strconv.Atoi(b[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}
