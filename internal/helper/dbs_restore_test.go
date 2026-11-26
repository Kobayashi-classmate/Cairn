// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

const mysqlDumps = "/var/lib/cairn/backups/db/mysql"

// mysqlRestoreEnv answers like a MariaDB server whose database shop holds
// current (what mariadb-dump writes); load answers each run of the
// restoring client (by number, from 0) and records its input.
type mysqlRestoreEnv struct {
	*dbEnv
	current string
	grants  string
	loads   []string
	load    func(n int) error
	dumpErr error
}

func newMySQLRestoreEnv(t *testing.T) *mysqlRestoreEnv {
	e := &mysqlRestoreEnv{dbEnv: newDBEnv(t, map[string]string{"mariadb-server": mariadbInstalled}),
		current: "CREATE TABLE t (id int);\nINSERT INTO t VALUES (1);\n", load: func(int) error { return nil }}
	writeSystemFile(t, e.d.Root, "/usr/bin/mariadb-dump", "#!binary")
	e.reply = func(c dbCall) (string, error) {
		switch {
		case strings.HasSuffix(c.name, "mariadb-dump"):
			return e.current, e.dumpErr
		case strings.Contains(c.stdin, "SELECT 'h', Host FROM mysql.user WHERE User = 'shop'"):
			return "h\tlocalhost\n", nil
		case strings.Contains(c.stdin, "SHOW GRANTS FOR"):
			return e.grants, nil
		case strings.Contains(c.stdin, "DEFAULT_CHARACTER_SET_NAME"):
			return "c\tutf8mb4\tutf8mb4_uca1400_ai_ci\n", nil
		case slices.Contains(c.args, "--database=shop"):
			e.loads = append(e.loads, c.stdin)
			return "", e.load(len(e.loads) - 1)
		}
		return "", nil
	}
	return e
}

func (e *mysqlRestoreEnv) restore(file string) (helperapi.Op, string) {
	e.t.Helper()
	e.reset()
	e.loads = nil
	return e.run(helperapi.ActionDBRestore, helperapi.DBRestoreRequest{Engine: "mysql", Name: "shop", File: file, User: "shop", Password: "pass#word1"})
}

// copies lists the copies restores took, oldest first.
func (e *mysqlRestoreEnv) copies() []string {
	entries, _ := os.ReadDir(filepath.Join(e.d.Root, mysqlDumps))
	var out []string
	for _, en := range entries {
		if strings.Contains(en.Name(), "-before-restore") {
			out = append(out, en.Name())
		}
	}
	return out
}

func gunzipFile(t *testing.T, p string) string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A restore first copies the database as it is, and the definers of the
// dump's views, triggers, routines and events become the restoring user,
// who cannot create objects for other accounts: anywhere else on a line,
// in a trigger's body or in data, DEFINER stays as it is.
func TestMySQLRestoreCopiesFirstAndTakesOverDefiners(t *testing.T) {
	e := newMySQLRestoreEnv(t)
	dump := strings.Join([]string{
		"CREATE TABLE t (id int);",
		"INSERT INTO t VALUES ('/*!50013 DEFINER=`root`@`localhost` */');",
		"/*!50001 CREATE ALGORITHM=UNDEFINED */",
		"/*!50013 DEFINER=`root`@`localhost` SQL SECURITY DEFINER */",
		"/*!50001 VIEW `v` AS select 1 AS `1` */;",
		"/*!50003 CREATE*/ /*!50017 DEFINER=`shop`@`127.0.0.1`*/ /*!50003 TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET @x = 'DEFINER=`a`@`b`' */;;",
		"CREATE DEFINER=`root`@`%` PROCEDURE `p`()",
		"/*!50106 CREATE*/ /*!50117 DEFINER=`root`@`localhost`*/ /*!50106 EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 1 */ ;;",
		"CREATE OR REPLACE ALGORITHM=MERGE DEFINER = 'old'@'10.0.0.%' SQL SECURITY INVOKER VIEW w AS SELECT 2;",
		"CREATE definer=admin@localhost FUNCTION f() RETURNS int RETURN 1;",
		"-- CREATE DEFINER=`root`@`localhost` VIEW x",
		"",
	}, "\n")
	want := strings.Join([]string{
		"CREATE TABLE t (id int);",
		"INSERT INTO t VALUES ('/*!50013 DEFINER=`root`@`localhost` */');",
		"/*!50001 CREATE ALGORITHM=UNDEFINED */",
		"/*!50013 DEFINER=CURRENT_USER SQL SECURITY DEFINER */",
		"/*!50001 VIEW `v` AS select 1 AS `1` */;",
		"/*!50003 CREATE*/ /*!50017 DEFINER=CURRENT_USER*/ /*!50003 TRIGGER trg BEFORE INSERT ON t FOR EACH ROW SET @x = 'DEFINER=`a`@`b`' */;;",
		"CREATE DEFINER=CURRENT_USER PROCEDURE `p`()",
		"/*!50106 CREATE*/ /*!50117 DEFINER=CURRENT_USER*/ /*!50106 EVENT `e` ON SCHEDULE EVERY 1 DAY DO SELECT 1 */ ;;",
		"CREATE OR REPLACE ALGORITHM=MERGE DEFINER=CURRENT_USER SQL SECURITY INVOKER VIEW w AS SELECT 2;",
		"CREATE DEFINER=CURRENT_USER FUNCTION f() RETURNS int RETURN 1;",
		"-- CREATE DEFINER=`root`@`localhost` VIEW x",
		"",
	}, "\n")
	writeSystemFile(t, e.d.Root, mysqlDumps+"/shop_up.sql", dump)
	op, log := e.restore("shop_up.sql")
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("restore: %+v\n%s", op, log)
	}
	if len(e.loads) != 1 || e.loads[0] != want {
		t.Fatalf("the client read:\n%s\nwant:\n%s", strings.Join(e.loads, "\n---\n"), want)
	}
	copies := e.copies()
	if len(copies) != 1 || !strings.HasPrefix(copies[0], "shop_") || !strings.HasSuffix(copies[0], "-before-restore.sql.gz") {
		t.Fatalf("copies %v", copies)
	}
	if got := gunzipFile(t, filepath.Join(e.d.Root, mysqlDumps, copies[0])); got != e.current {
		t.Fatalf("copy %q", got)
	}
	if !strings.Contains(log, copies[0]) || !strings.Contains(log, "6 DEFINER clauses") {
		t.Fatalf("log:\n%s", log)
	}
	// The copy comes first.
	dumped := slices.IndexFunc(e.calls, func(c dbCall) bool { return strings.HasSuffix(c.name, "mariadb-dump") })
	loaded := slices.IndexFunc(e.calls, func(c dbCall) bool { return slices.Contains(c.args, "--database=shop") })
	if dumped < 0 || dumped > loaded {
		t.Fatalf("copy at %d, restore at %d", dumped, loaded)
	}
}

// A dump that fails partway leaves the database as the copy has it: the
// database is made again (the grants on it stay) and the copy loaded as
// the same user.
func TestMySQLRestorePutsTheDatabaseBack(t *testing.T) {
	e := newMySQLRestoreEnv(t)
	writeSystemFile(t, e.d.Root, mysqlDumps+"/shop_bad.sql", "CREATE TABLE t2 (id int);\nGRANT ALL ON *.* TO shop@localhost;\n")
	failed := exitErr("ERROR 1044 (42000) at line 2: Access denied for user 'shop'@'localhost' to database 'mysql'")
	e.load = func(n int) error {
		if n == 0 {
			return failed
		}
		return nil
	}
	op, log := e.restore("shop_bad.sql")
	copies := e.copies()
	if op.State != helperapi.OpFailed || op.Error.Code != "db.restore_rolled_back" || len(copies) != 1 ||
		op.Error.Params["copy"] != copies[0] || op.Error.Params["database"] != "shop" {
		t.Fatalf("failed restore: %+v %v\n%s", op, copies, log)
	}
	if len(e.loads) != 2 || e.loads[1] != e.current {
		t.Fatalf("loads %q", e.loads)
	}
	sql := e.script()
	remade := strings.Index(sql, "DROP DATABASE `shop`;\nCREATE DATABASE `shop` CHARACTER SET utf8mb4 COLLATE utf8mb4_uca1400_ai_ci;")
	if remade < 0 || remade > strings.LastIndex(sql, e.current) {
		t.Fatalf("the database was not made again before the copy:\n%s", sql)
	}

	// Putting it back fails too: the copy is named for the user.
	e.load = func(int) error { return failed }
	op, log = e.restore("shop_bad.sql")
	if op.State != helperapi.OpFailed || op.Error.Code != "db.restore_partial" || op.Error.Params["copy"] == nil || !strings.Contains(log, "Putting the database back failed") {
		t.Fatalf("failed put back: %+v\n%s", op, log)
	}

	// A client that could not sign in ran nothing: nothing is put back.
	e.load = func(int) error {
		return exitErr("ERROR 1045 (28000): Access denied for user 'shop'@'localhost' (using password: YES)")
	}
	op, _ = e.restore("shop_bad.sql")
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBAccessDenied || strings.Contains(e.script(), "DROP DATABASE") {
		t.Fatalf("denied: %+v\n%s", op, e.script())
	}

	// No copy, no restore: the database stays untouched.
	e.load = func(int) error { return nil }
	e.dumpErr = exitErr("mariadb-dump: Got error: 1932: Table 'shop.x' doesn't exist in engine when using LOCK TABLES")
	op, _ = e.restore("shop_bad.sql")
	if op.State != helperapi.OpFailed || op.Error.Code != "db.copy_failed" || len(e.loads) != 0 {
		t.Fatalf("copy failing: %+v, loads %q", op, e.loads)
	}
}

// A user with rights beyond its databases restores nothing: the dump would
// run with them.
func TestRestoreRefusesPrivilegedUsers(t *testing.T) {
	e := newMySQLRestoreEnv(t)
	e.grants = "GRANT FILE ON *.* TO `shop`@`localhost`\n"
	writeSystemFile(t, e.d.Root, mysqlDumps+"/shop_up.sql", "SELECT 1;\n")
	if op, _ := e.restore("shop_up.sql"); op.State != helperapi.OpFailed || op.Error.Code != "db.user_privileged" || len(e.loads) != 0 || len(e.copies()) != 0 {
		t.Fatalf("mysql: %+v, loads %q", op, e.loads)
	}

	p := newPGEnv(t)
	p.reply = func(c dbCall) (string, error) {
		switch {
		case c.name == "/usr/bin/pg_lsclusters":
			return pgClusters, nil
		case strings.HasPrefix(c.stdin, "SELECT 'p', "):
			return "p\tshop\tt\tf\tf\tf\n", nil
		}
		return "", nil
	}
	writeSystemFile(t, p.d.Root, "/var/lib/cairn/backups/db/postgresql-17/shop.sql", "SELECT 1;\n")
	op, _ := p.run(helperapi.ActionDBRestore, helperapi.DBRestoreRequest{Engine: "postgresql-17", Name: "shop", File: "shop.sql", User: "shop", Password: "S3cret-pass"})
	if op.State != helperapi.OpFailed || op.Error.Code != "db.user_privileged" {
		t.Fatalf("postgresql: %+v", op)
	}
	for _, c := range p.calls {
		if strings.HasSuffix(c.name, "/psql") || strings.Contains(c.line(), "pg_dump") {
			t.Fatalf("ran %s", c.line())
		}
	}
}

// PostgreSQL loads a dump in a single transaction: one that fails changes
// nothing. The copy comes first all the same.
func TestPostgreSQLRestoreIsOneTransaction(t *testing.T) {
	e := newPGEnv(t)
	var fail error
	e.reply = func(c dbCall) (string, error) {
		switch {
		case c.name == "/usr/bin/pg_lsclusters":
			return pgClusters, nil
		case strings.Contains(c.line(), "pg_dump"):
			return "PGDMP-now", nil
		case strings.HasSuffix(c.name, "/pg_restore"), strings.HasSuffix(c.name, "/psql") && !slices.Contains(c.args, "--version"):
			return "", fail
		}
		return "", nil
	}
	dir := "/var/lib/cairn/backups/db/postgresql-17"
	writeSystemFile(t, e.d.Root, dir+"/shop_up.dump", "PGDMP-old")
	writeSystemFile(t, e.d.Root, dir+"/shop_up.sql", "CREATE TABLE junk (id int);\nALTER ROLE shop SUPERUSER;\n")
	for _, file := range []string{"shop_up.dump", "shop_up.sql"} {
		e.reset()
		req := helperapi.DBRestoreRequest{Engine: "postgresql-17", Name: "shop", File: file, User: "shop", Password: "S3cret-pass"}
		if op, log := e.run(helperapi.ActionDBRestore, req); op.State != helperapi.OpSucceeded {
			t.Fatalf("%s: %+v\n%s", file, op, log)
		}
		c := e.calls[len(e.calls)-1]
		if !slices.Contains(c.args, "--single-transaction") {
			t.Fatalf("%s: %s", file, c.line())
		}
	}
	entries, _ := os.ReadDir(filepath.Join(e.d.Root, dir))
	var copies []string
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), "-before-restore.dump") {
			copies = append(copies, en.Name())
			if data, _ := os.ReadFile(filepath.Join(e.d.Root, dir, en.Name())); string(data) != "PGDMP-now" {
				t.Fatalf("copy %s: %q", en.Name(), data)
			}
		}
	}
	if len(copies) != 2 {
		t.Fatalf("copies %v", copies)
	}

	fail = exitErr(`psql:<stdin>:2: ERROR:  permission denied to alter role`)
	e.reset()
	op, log := e.run(helperapi.ActionDBRestore, helperapi.DBRestoreRequest{Engine: "postgresql-17", Name: "shop", File: "shop_up.sql", User: "shop", Password: "S3cret-pass"})
	if op.State != helperapi.OpFailed || op.Error.Code != "db.restore_unchanged" || op.Error.Params["copy"] == nil || !strings.Contains(log, "nothing in shop was changed") {
		t.Fatalf("failed restore: %+v\n%s", op, log)
	}
}
