// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// A literal holds a quote or a backslash whatever the server's sql_mode:
// every script takes NO_BACKSLASH_ESCAPES out of the session first, a
// quote is doubled, and the client's own commands are off.
func TestMySQLScriptsReadTheSameInEveryMode(t *testing.T) {
	for in, want := range map[string]string{
		`plain`: `'plain'`, `it's`: `'it''s'`, `a\b`: `'a\\b'`, `x\`: `'x\\'`, `back\'quote`: `'back\\''quote'`, "nul\x00": `'nul\0'`,
	} {
		if got := myStr(in); got != want {
			t.Errorf("myStr(%q) = %s, want %s", in, got, want)
		}
	}
	e := newDBEnv(t, map[string]string{"mariadb-server": mariadbInstalled})
	e.reply = func(c dbCall) (string, error) {
		if strings.Contains(c.stdin, "SELECT 'h', Host FROM mysql.user WHERE User = 'app'") {
			return "h\tlocalhost\n", nil
		}
		return "", nil
	}
	if op, log := e.run(helperapi.ActionDBUserDrop, helperapi.DBRef{Engine: "mysql", Name: "app"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("drop: %+v\n%s", op, log)
	}
	if len(e.calls) == 0 {
		t.Fatal("no client ran")
	}
	for _, c := range e.calls {
		first, _, _ := strings.Cut(c.stdin, "\n")
		if !slices.Contains(c.args, "--binary-mode") || !strings.HasPrefix(first, "SET SESSION sql_mode = ") || !strings.Contains(first, "NO_BACKSLASH_ESCAPES") {
			t.Fatalf("client %s with %q", c.line(), c.stdin)
		}
	}
}

// What the helper reads back from the server goes into a statement only
// when it looks like what the panel makes: a host entry or a grantee with
// a quote is refused, and nothing is changed.
func TestMySQLRefusesOddNamesFromTheServer(t *testing.T) {
	e := newDBEnv(t, map[string]string{"mariadb-server": mariadbInstalled})
	hosts, grants := "", ""
	e.reply = func(c dbCall) (string, error) {
		switch {
		case strings.Contains(c.stdin, "SELECT 'h', Host FROM mysql.user"):
			return hosts, nil
		case strings.Contains(c.stdin, "FROM mysql.db WHERE Db IN"):
			return "d\n" + grants, nil
		}
		return "", nil
	}
	changed := func() string {
		for _, c := range e.calls {
			for _, stmt := range []string{"DROP USER", "ALTER USER", "REVOKE", "DROP DATABASE", "GRANT "} {
				if strings.Contains(c.stdin, stmt) {
					return c.stdin
				}
			}
		}
		return ""
	}
	hosts = "h\tlocalhost\nh\tx' OR '1\n"
	for _, tc := range []struct {
		action string
		req    any
	}{
		{helperapi.ActionDBUserDrop, helperapi.DBRef{Engine: "mysql", Name: "app"}},
		{helperapi.ActionDBUserSet, helperapi.DBUserRequest{Engine: "mysql", Name: "app", Password: "N3w-secret"}},
		{helperapi.ActionDBCreate, helperapi.DBCreateRequest{Engine: "mysql", Name: "shop", Owner: "app"}},
	} {
		e.reset()
		if op, _ := e.run(tc.action, tc.req); op.State != helperapi.OpFailed || op.Error.Code != "db.unsafe_entry" {
			t.Fatalf("%s with an odd host: %+v", tc.action, op)
		}
		if s := changed(); s != "" {
			t.Fatalf("%s changed something:\n%s", tc.action, s)
		}
	}
	// Dropping a database takes the grants on it; one of a user with an
	// odd name stops it.
	e.reset()
	grants = "g\tev'il\tlocalhost\tshop\n"
	if op, _ := e.run(helperapi.ActionDBDrop, helperapi.DBRef{Engine: "mysql", Name: "shop"}); op.State != helperapi.OpFailed || op.Error.Code != "db.unsafe_entry" {
		t.Fatalf("drop with an odd grantee: %+v", op)
	}
	if s := changed(); s != "" {
		t.Fatalf("the drop changed something:\n%s", s)
	}
	// The anonymous user and a host pattern are names the server makes.
	e.reset()
	grants = "g\t\t%\tshop\ng\tapp-1\t192.168.%\tshop\n"
	if op, log := e.run(helperapi.ActionDBDrop, helperapi.DBRef{Engine: "mysql", Name: "shop"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("drop: %+v\n%s", op, log)
	}
	if sql := e.script(); !strings.Contains(sql, "REVOKE ALL PRIVILEGES ON `shop`.* FROM ''@'%';") ||
		!strings.Contains(sql, "REVOKE ALL PRIVILEGES ON `shop`.* FROM 'app-1'@'192.168.%';") {
		t.Fatalf("drop script:\n%s", sql)
	}
}

// A user with rights beyond single databases is neither changed nor
// dropped: who asks the helper could otherwise take it over.
func TestMySQLLeavesPrivilegedUsersAlone(t *testing.T) {
	e := newDBEnv(t, map[string]string{"mariadb-server": mariadbInstalled})
	grants := ""
	e.reply = func(c dbCall) (string, error) {
		switch {
		case strings.Contains(c.stdin, "SELECT 'h', Host FROM mysql.user WHERE User = 'dba'"):
			return "h\tlocalhost\nh\t%\n", nil
		case strings.Contains(c.stdin, "SHOW GRANTS FOR 'dba'@"):
			return grants, nil
		}
		return "", nil
	}
	const usage = "GRANT USAGE ON *.* TO `dba`@`localhost` IDENTIFIED BY PASSWORD '*AB12CD'\n"
	changes := []struct {
		action string
		req    any
	}{
		{helperapi.ActionDBUserSet, helperapi.DBUserRequest{Engine: "mysql", Name: "dba", Password: "N3w-secret"}},
		{helperapi.ActionDBUserSet, helperapi.DBUserRequest{Engine: "mysql", Name: "dba", Hosts: []string{"%"}, Grants: []helperapi.DBGrant{}}},
		{helperapi.ActionDBUserDrop, helperapi.DBRef{Engine: "mysql", Name: "dba"}},
	}
	for _, line := range []string{
		"GRANT ALL PRIVILEGES ON *.* TO `dba`@`%` WITH GRANT OPTION",
		"GRANT SELECT, RELOAD ON *.* TO `dba`@`localhost`",
		"GRANT FILE ON *.* TO `dba`@`localhost`",
		"GRANT SYSTEM_VARIABLES_ADMIN,SET_USER_ID ON *.* TO `dba`@`localhost`",
		"GRANT USAGE ON *.* TO `dba`@`localhost` WITH GRANT OPTION",
		"GRANT PROXY ON ''@'%' TO 'dba'@'localhost'",
		"GRANT `admin_role` TO `dba`@`localhost`",
		"GRANT `r1`@`%`,`r2`@`%` TO `dba`@`localhost`",
		"GRANT INSERT ON `mysql`.* TO `dba`@`localhost`",
		"GRANT UPDATE ON `mysql`.`global_priv` TO `dba`@`localhost`",
		"GRANT ALL PRIVILEGES ON `%`.* TO `dba`@`localhost`",
		"GRANT ALL PRIVILEGES ON `my%`.* TO `dba`@`localhost`",
		"GRANT ALL PRIVILEGES ON `MySQL`.* TO `dba`@`localhost`",
	} {
		grants = usage + line + "\n"
		for _, ch := range changes {
			e.reset()
			op, log := e.run(ch.action, ch.req)
			if op.State != helperapi.OpFailed || op.Error.Code != "db.user_privileged" || op.Error.Params["name"] != "dba" {
				t.Fatalf("%q: %s went through: %+v\n%s", line, ch.action, op, log)
			}
			for _, c := range e.calls {
				for _, stmt := range []string{"ALTER USER", "DROP USER", "CREATE USER", "REVOKE"} {
					if strings.Contains(c.stdin, stmt) {
						t.Fatalf("%q: %s ran %s:\n%s", line, ch.action, stmt, c.stdin)
					}
				}
			}
		}
	}
	// Users of single databases are changed as before: a grant with
	// column names, an escaped pattern, a routine.
	for _, line := range []string{
		"GRANT ALL PRIVILEGES ON `shop`.* TO `dba`@`localhost`",
		"GRANT SELECT ON `my\\\\_app`.* TO `dba`@`localhost`",
		"GRANT SELECT (`a`, `b ON x`), INSERT ON `shop`.`t` TO `dba`@`localhost`",
		"GRANT EXECUTE ON PROCEDURE `shop`.`p` TO `dba`@`localhost`",
		"GRANT USAGE ON *.* TO `dba`@`%`",
	} {
		grants = usage + line + "\n"
		e.reset()
		if op, log := e.run(helperapi.ActionDBUserSet, changes[0].req); op.State != helperapi.OpSucceeded || !strings.Contains(e.script(), "ALTER USER 'dba'@'localhost' IDENTIFIED BY 'N3w-secret';") {
			t.Fatalf("%q: %+v\n%s", line, op, log)
		}
	}
}

func TestPostgreSQLLeavesPrivilegedRolesAlone(t *testing.T) {
	e := newPGEnv(t)
	privileged := ""
	e.reply = func(c dbCall) (string, error) {
		switch {
		case c.name == "/usr/bin/pg_lsclusters":
			return pgClusters, nil
		case strings.HasPrefix(c.stdin, "SELECT 'r' FROM pg_roles"):
			return "r\n", nil
		case strings.HasPrefix(c.stdin, "SELECT 'p', "):
			return privileged, nil
		case strings.HasPrefix(c.stdin, "SELECT 'd', datname"):
			return "d\tpostgres\tpostgres\nd\tshop\tadmin\n", nil
		}
		return "", nil
	}
	changes := []struct {
		action string
		req    any
	}{
		{helperapi.ActionDBUserSet, helperapi.DBUserRequest{Engine: "postgresql-17", Name: "dba", Password: "N3w-secret"}},
		{helperapi.ActionDBUserSet, helperapi.DBUserRequest{Engine: "postgresql-17", Name: "dba", Hosts: []string{"localhost", "%"}}},
		{helperapi.ActionDBUserDrop, helperapi.DBRef{Engine: "postgresql-17", Name: "dba"}},
	}
	for _, rows := range []string{
		"p\tdba\tt\tf\tf\tf\n", "p\tdba\tf\tt\tf\tf\n", "p\tdba\tf\tf\tt\tf\n", "p\tdba\tf\tf\tf\tt\n",
		"p\tpg_execute_server_program\tf\tf\tf\tf\n", "p\tpg_write_server_files\tf\tf\tf\tf\n", "p\tadmins\tt\tf\tf\tf\n",
	} {
		privileged = rows
		for _, ch := range changes {
			e.reset()
			op, log := e.run(ch.action, ch.req)
			if op.State != helperapi.OpFailed || op.Error.Code != "db.user_privileged" {
				t.Fatalf("%q: %s went through: %+v\n%s", rows, ch.action, op, log)
			}
			for _, c := range e.calls {
				for _, stmt := range []string{"ALTER ROLE", "DROP ROLE", "REASSIGN", "pg_reload_conf"} {
					if strings.Contains(c.stdin, stmt) {
						t.Fatalf("%q: %s ran %s:\n%s", rows, ch.action, stmt, c.stdin)
					}
				}
			}
			if hba, _ := os.ReadFile(filepath.Join(e.d.Root, "/etc/postgresql/17/main/pg_hba.conf")); strings.Contains(string(hba), "dba") {
				t.Fatalf("pg_hba.conf names dba:\n%s", hba)
			}
		}
	}
	// The question covers memberships and the predefined roles that reach
	// the server's account.
	var query string
	for _, c := range e.calls {
		if strings.HasPrefix(c.stdin, "SELECT 'p', ") {
			query = c.stdin
		}
	}
	for _, want := range []string{"pg_has_role", "rolsuper", "rolcreaterole", "rolreplication", "rolbypassrls", "pg_execute_server_program"} {
		if !strings.Contains(query, want) {
			t.Fatalf("query lacks %s:\n%s", want, query)
		}
	}
	// A plain role gets its new password, and a role that may not log in
	// stays so.
	privileged = ""
	e.reset()
	if op, log := e.run(helperapi.ActionDBUserSet, changes[0].req); op.State != helperapi.OpSucceeded {
		t.Fatalf("plain role: %+v\n%s", op, log)
	}
	if sql := e.script(); !strings.Contains(sql, `ALTER ROLE "dba" WITH PASSWORD 'N3w-secret';`) || strings.Contains(sql, "LOGIN") {
		t.Fatalf("password script:\n%s", sql)
	}
}

// The panel's MySQL snippet is read last, and where the server listens is
// read back after the restart: a setting read after it, or a refused
// change, gets the previous files back.
func TestMySQLRemoteIsReadBack(t *testing.T) {
	e := newDBEnv(t, map[string]string{"mariadb-server": mariadbInstalled})
	e.f.systemd = true
	dir := filepath.Join(e.d.Root, "/etc/mysql/mariadb.conf.d")
	legacy := "# " + dbManagedNote + "\n[mysqld]\nbind-address = 127.0.0.1\n"
	writeSystemFile(t, e.d.Root, "/etc/mysql/mariadb.conf.d/99-cairn.cnf", legacy)
	bind, skip := "0.0.0.0", "OFF"
	e.reply = func(c dbCall) (string, error) {
		if strings.Contains(c.stdin, "bind_address") {
			return "bind_address\t" + bind + "\nskip_networking\t" + skip + "\n", nil
		}
		return "", nil
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "<none>"
		}
		return string(data)
	}
	if op, log := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "mysql", Enabled: true}); op.State != helperapi.OpSucceeded {
		t.Fatalf("remote on: %+v\n%s", op, log)
	}
	if got := read("zz-cairn.cnf"); !strings.Contains(got, "[mysqld]\nbind-address = 0.0.0.0\n") || read("99-cairn.cnf") != "<none>" {
		t.Fatalf("zz-cairn.cnf %q, 99-cairn.cnf %q", got, read("99-cairn.cnf"))
	}
	enabled := read("zz-cairn.cnf")

	// Another file keeps the server on every address: the change does not
	// hold, and the previous file is back.
	op, log := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "mysql", Enabled: false})
	if op.State != helperapi.OpFailed || op.Error.Code != "db.remote_not_applied" || op.Error.Params["listen"] != "0.0.0.0" {
		t.Fatalf("remote off, overridden: %+v\n%s", op, log)
	}
	if read("zz-cairn.cnf") != enabled {
		t.Fatalf("zz-cairn.cnf not restored: %q", read("zz-cairn.cnf"))
	}
	if n := strings.Count(strings.Join(e.f.cmds, "\n"), "systemctl restart mariadb"); n != 3 {
		t.Fatalf("restarts: %d\n%s", n, strings.Join(e.f.cmds, "\n"))
	}

	// With the earlier release's file: it comes back too, and the new one
	// goes.
	if err := os.Remove(filepath.Join(dir, "zz-cairn.cnf")); err != nil {
		t.Fatal(err)
	}
	writeSystemFile(t, e.d.Root, "/etc/mysql/mariadb.conf.d/99-cairn.cnf", legacy)
	bind, skip = "127.0.0.1", "ON"
	if op, _ := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "mysql", Enabled: true}); op.State != helperapi.OpFailed || op.Error.Code != "db.remote_not_applied" {
		t.Fatalf("remote on, skip_networking: %+v", op)
	}
	if read("99-cairn.cnf") != legacy || read("zz-cairn.cnf") != "<none>" {
		t.Fatalf("after the refused change: 99 %q, zz %q", read("99-cairn.cnf"), read("zz-cairn.cnf"))
	}
	// Turning it off where the server agrees.
	bind, skip = "127.0.0.1", "OFF"
	if op, log := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "mysql", Enabled: false}); op.State != helperapi.OpSucceeded {
		t.Fatalf("remote off: %+v\n%s", op, log)
	}
	if !strings.Contains(read("zz-cairn.cnf"), "bind-address = 127.0.0.1") || read("99-cairn.cnf") != "<none>" {
		t.Fatalf("zz %q, 99 %q", read("zz-cairn.cnf"), read("99-cairn.cnf"))
	}
}

// PostgreSQL's listen_addresses is read back as well: ALTER SYSTEM wins
// over conf.d.
func TestPostgreSQLRemoteIsReadBack(t *testing.T) {
	e := newPGEnv(t)
	listen := "localhost"
	e.reply = func(c dbCall) (string, error) {
		switch {
		case c.name == "/usr/bin/pg_lsclusters":
			return pgClusters, nil
		case strings.Contains(c.stdin, "current_setting('listen_addresses')"):
			return "l\t" + listen + "\n", nil
		}
		return "", nil
	}
	conf := filepath.Join(e.d.Root, "/etc/postgresql/17/main/conf.d/99-cairn.conf")
	if op, _ := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "postgresql-17", Enabled: true}); op.State != helperapi.OpFailed || op.Error.Code != "db.remote_not_applied" {
		t.Fatalf("overridden: %+v", op)
	}
	if _, err := os.Stat(conf); !os.IsNotExist(err) {
		t.Fatalf("the new file stays: %v", err)
	}
	listen = "*"
	if op, log := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "postgresql-17", Enabled: true}); op.State != helperapi.OpSucceeded {
		t.Fatalf("remote on: %+v\n%s", op, log)
	}
	if data, _ := os.ReadFile(conf); !strings.Contains(string(data), "listen_addresses = '*'") {
		t.Fatalf("config %q", data)
	}
}
