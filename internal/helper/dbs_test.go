// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// dbCall is one run of a database tool.
type dbCall struct {
	name  string
	args  []string
	stdin string
	env   []string
}

func (c dbCall) line() string { return c.name + " " + strings.Join(c.args, " ") }

type dbEnv struct {
	t     *testing.T
	f     *fakeSystem
	d     *DBs
	o     *Ops
	r     *Registry
	mu    sync.Mutex
	calls []dbCall
	// reply answers a tool run: its standard output, or an error.
	reply func(c dbCall) (string, error)
}

func newDBEnv(t *testing.T, installed map[string]string) *dbEnv {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{installed: installed}
	f.use(t)
	s, o, r := softwareFor(t, cat, "debian-13", trixie, "apt")
	d := &DBs{Software: s, Root: s.Root, DataDir: "/var/lib/cairn", PanelUser: "cairn"}
	RegisterDBs(r, o, d)
	if err := os.MkdirAll(filepath.Join(s.Root, "/var/lib/cairn"), 0o700); err != nil {
		t.Fatal(err)
	}
	e := &dbEnv{t: t, f: f, d: d, o: o, r: r, reply: func(dbCall) (string, error) { return "", nil }}
	uid, gid := os.Getuid(), os.Getgid()
	oldRun, oldUser := dbRun, lookupUser
	t.Cleanup(func() { dbRun, lookupUser = oldRun, oldUser })
	lookupUser = func(name string) (int, int, error) {
		if name == "cairn" {
			return uid, gid, nil
		}
		return 0, 0, os.ErrNotExist
	}
	dbRun = func(_ context.Context, c execx.Cmd) (execx.Result, error) {
		call := dbCall{name: c.Name, args: c.Args, env: c.Env}
		if c.Stdin != nil {
			data, _ := io.ReadAll(c.Stdin)
			call.stdin = string(data)
		}
		e.mu.Lock()
		e.calls = append(e.calls, call)
		reply := e.reply
		e.mu.Unlock()
		out, err := reply(call)
		if c.Stdout != nil {
			_, _ = io.WriteString(c.Stdout, out)
			return execx.Result{}, err
		}
		return execx.Result{Stdout: []byte(out)}, err
	}
	return e
}

func (e *dbEnv) run(action string, req any) (helperapi.Op, string) {
	e.t.Helper()
	ref, err := call[helperapi.OpRef](e.t, e.r, action, req)
	if err != nil {
		e.t.Fatalf("%s: %v", action, err)
	}
	op := waitOp(e.t, e.o, ref.ID, helperapi.OpSucceeded, helperapi.OpFailed)
	got, _ := e.o.Get(ref.ID, 0)
	return op, got.Log
}

func (e *dbEnv) script() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var b strings.Builder
	for _, c := range e.calls {
		b.WriteString(c.stdin)
	}
	return b.String()
}

func (e *dbEnv) reset() {
	e.mu.Lock()
	e.calls = nil
	e.mu.Unlock()
}

func (e *dbEnv) status() helperapi.DBStatus {
	e.t.Helper()
	s, err := call[helperapi.DBStatus](e.t, e.r, helperapi.ActionDBStatus, helperapi.Empty{})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func exitErr(stderr string) error {
	return &execx.ExitError{Name: "client", ExitCode: 1, Stderr: stderr}
}

const mariadbInstalled = "1:11.8.3-0+deb13u1"

func TestMySQLStatus(t *testing.T) {
	e := newDBEnv(t, map[string]string{"mariadb-server": mariadbInstalled})
	writeSystemFile(t, e.d.Root, "/usr/bin/mariadb", "#!binary")
	e.reply = func(c dbCall) (string, error) {
		if c.name != "/usr/bin/mariadb" {
			t.Errorf("client %s", c.name)
		}
		return strings.Join([]string{
			"v\t11.8.3-MariaDB-0+deb13u1",
			"bind_address\t127.0.0.1", "port\t3306", "skip_networking\tOFF",
			"d\tinformation_schema\t0", "d\tmysql\t1000", "d\tshop\t4096", "d\tsys\t0",
			"u\tmariadb.sys\tlocalhost", "u\troot\tlocalhost",
			"u\tshop\t10.0.0.0/255.0.0.0", "u\tshop\tlocalhost",
		}, "\n"), nil
	}
	s := e.status()
	if !s.Supported || len(s.Engines) != 1 {
		t.Fatalf("status %+v", s)
	}
	m := s.Engines[0]
	if m.ID != "mysql" || m.Software != "mariadb" || m.Line != "11.8" || m.Service != "mariadb" || !m.Running || m.Remote || m.Port != 3306 {
		t.Fatalf("engine %+v", m)
	}
	if len(m.Databases) != 1 || m.Databases[0].Name != "shop" || m.Databases[0].SizeBytes != 4096 {
		t.Fatalf("databases %+v", m.Databases)
	}
	if len(m.Users) != 1 || m.Users[0].Name != "shop" || strings.Join(m.Users[0].Hosts, ",") != "10.0.0.0/8,localhost" {
		t.Fatalf("users %+v", m.Users)
	}

	// A server that is down is not an error.
	e.reply = func(dbCall) (string, error) {
		return "", exitErr("ERROR 2002 (HY000): Can't connect to local server through socket '/run/mysqld/mysqld.sock' (2)")
	}
	if m := e.status().Engines[0]; m.Running || m.Error != nil {
		t.Fatalf("stopped engine %+v", m)
	}
	e.reply = func(dbCall) (string, error) {
		return "", exitErr("ERROR 1698 (28000): Access denied for user 'root'@'localhost'")
	}
	if m := e.status().Engines[0]; m.Error == nil || m.Error.Code != helperapi.CodeDBAccessDenied {
		t.Fatalf("denied engine %+v", m)
	}
}

func TestMySQLCreateUserAndDrop(t *testing.T) {
	e := newDBEnv(t, map[string]string{"mariadb-server": mariadbInstalled})
	hosts := "h\tlocalhost\nh\t10.0.0.0/255.0.0.0\n"
	e.reply = func(c dbCall) (string, error) {
		switch {
		case strings.Contains(c.stdin, "FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = 'taken'"):
			return "d\n", nil
		case strings.HasPrefix(c.stdin, "SELECT 'h', Host FROM mysql.user WHERE User = 'my_shop'"):
			return hosts, nil
		case strings.Contains(c.stdin, "FROM mysql.db WHERE Db IN ('my_shop', 'my\\\\_shop')"):
			return "d\ng\tmy_shop\tlocalhost\tmy\\\\_shop\n", nil
		}
		return "", nil
	}
	if op, _ := e.run(helperapi.ActionDBCreate, helperapi.DBCreateRequest{Engine: "mysql", Name: "taken"}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBExists {
		t.Fatalf("existing database: %+v", op)
	}
	e.reset()
	if op, log := e.run(helperapi.ActionDBCreate, helperapi.DBCreateRequest{Engine: "mysql", Name: "my_shop", Owner: "my_shop"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("create: %+v\n%s", op, log)
	}
	sql := e.script()
	for _, want := range []string{
		"CREATE DATABASE `my_shop` CHARACTER SET utf8mb4;",
		// _ is a wildcard in GRANT: escaped, it grants this database only.
		"GRANT ALL PRIVILEGES ON `my\\_shop`.* TO 'my_shop'@'localhost';",
		"GRANT ALL PRIVILEGES ON `my\\_shop`.* TO 'my_shop'@'10.0.0.0/255.0.0.0';",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("create script lacks %q:\n%s", want, sql)
		}
	}

	e.reset()
	req := helperapi.DBUserRequest{Engine: "mysql", Name: "my_shop", Password: "S3cret-pass", Hosts: []string{"localhost", "192.168.1.0/24"},
		Grants: []helperapi.DBGrant{{Database: "my_shop", Level: helperapi.GrantAll}, {Database: "stats", Level: helperapi.GrantReadOnly}}}
	op, log := e.run(helperapi.ActionDBUserSet, req)
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("user set: %+v\n%s", op, log)
	}
	sql = e.script()
	for _, want := range []string{
		"CREATE USER 'my_shop'@'192.168.1.0/255.255.255.0' IDENTIFIED BY 'S3cret-pass';",
		"ALTER USER 'my_shop'@'localhost' IDENTIFIED BY 'S3cret-pass';",
		"DROP USER 'my_shop'@'10.0.0.0/255.0.0.0';",
		"REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'my_shop'@'localhost';",
		"GRANT ALL PRIVILEGES ON `my\\_shop`.* TO 'my_shop'@'192.168.1.0/255.255.255.0';",
		"GRANT SELECT, SHOW VIEW ON `stats`.* TO 'my_shop'@'localhost';",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("user script lacks %q:\n%s", want, sql)
		}
	}
	if strings.Contains(log, "S3cret-pass") || !strings.Contains(log, "IDENTIFIED BY '********'") {
		t.Fatalf("the log must mask the password:\n%s", log)
	}

	// A new user needs a password.
	hosts = ""
	req.Password = ""
	if op, _ := e.run(helperapi.ActionDBUserSet, req); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBPasswordNeed {
		t.Fatalf("user without password: %+v", op)
	}
	// A user the panel did not make is never taken over by a new one.
	hosts = "h\tlocalhost\n"
	req.New, req.Password = true, "S3cret-pass"
	if op, _ := e.run(helperapi.ActionDBUserSet, req); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBUserExists {
		t.Fatalf("new user over an existing one: %+v", op)
	}
	hosts = ""
	if op, _ := e.run(helperapi.ActionDBUserDrop, helperapi.DBRef{Engine: "mysql", Name: "my_shop"}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBUserNotFound {
		t.Fatalf("drop of a missing user: %+v", op)
	}

	// Dropping a database takes its grants (as stored) with it.
	e.reset()
	if op, log := e.run(helperapi.ActionDBDrop, helperapi.DBRef{Engine: "mysql", Name: "my_shop"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("drop: %+v\n%s", op, log)
	}
	sql = e.script()
	if !strings.Contains(sql, "REVOKE ALL PRIVILEGES ON `my\\_shop`.* FROM 'my_shop'@'localhost';") || !strings.Contains(sql, "DROP DATABASE `my_shop`;") {
		t.Fatalf("drop script:\n%s", sql)
	}
	// Reserved names never reach the server.
	if _, err := call[helperapi.OpRef](t, e.r, helperapi.ActionDBDrop, helperapi.DBRef{Engine: "mysql", Name: "mysql"}); err == nil {
		t.Fatal("dropping the mysql database must be refused")
	}
}

func TestMySQLRemoteNeedsSystemd(t *testing.T) {
	e := newDBEnv(t, map[string]string{"mariadb-server": mariadbInstalled})
	if op, _ := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "mysql", Enabled: true}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBSystemd {
		t.Fatalf("remote without systemd: %+v", op)
	}
	e.f.systemd = true
	if op, log := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "mysql", Enabled: true}); op.State != helperapi.OpSucceeded {
		t.Fatalf("remote: %+v\n%s", op, log)
	}
	conf, err := os.ReadFile(filepath.Join(e.d.Root, "/etc/mysql/mariadb.conf.d/99-cairn.cnf"))
	if err != nil || !strings.Contains(string(conf), "[mysqld]\nbind-address = 0.0.0.0\n") {
		t.Fatalf("config %q, %v", conf, err)
	}
	if !e.f.ran("systemctl restart mariadb") {
		t.Fatal("the server was not restarted")
	}
	// A server that does not come back gets its previous configuration.
	e.f.failPrefix = "systemctl restart"
	if op, _ := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "mysql", Enabled: false}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBRestartFailed {
		t.Fatalf("failed restart: %+v", op)
	}
	if conf, _ := os.ReadFile(filepath.Join(e.d.Root, "/etc/mysql/mariadb.conf.d/99-cairn.cnf")); !strings.Contains(string(conf), "0.0.0.0") {
		t.Fatalf("config not restored: %q", conf)
	}
}

func TestMySQLDumpAndRestore(t *testing.T) {
	e := newDBEnv(t, map[string]string{"mariadb-server": mariadbInstalled})
	writeSystemFile(t, e.d.Root, "/usr/bin/mariadb-dump", "#!binary")
	const dump = "CREATE TABLE t (id int);\nINSERT INTO t VALUES (1);\n"
	var cnf string
	e.reply = func(c dbCall) (string, error) {
		for _, a := range c.args {
			if p, ok := strings.CutPrefix(a, "--defaults-extra-file="); ok {
				data, _ := os.ReadFile(p)
				cnf = string(data)
			}
		}
		if strings.HasSuffix(c.name, "mariadb-dump") {
			return dump, nil
		}
		return "", nil
	}
	op, log := e.run(helperapi.ActionDBDump, helperapi.DBDumpRequest{Engine: "mysql", Name: "shop"})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("dump: %+v\n%s", op, log)
	}
	if args := e.calls[0].line(); !strings.Contains(args, "--single-transaction") || strings.Contains(args, "gtid") || !strings.HasSuffix(args, " shop") {
		t.Fatalf("dump command %s", args)
	}
	res, _ := e.o.Get(op.ID, 0)
	var info helperapi.DumpInfo
	decode(t, res.Op.Result, &info)
	if !strings.HasPrefix(info.File, "shop_") || !strings.HasSuffix(info.File, ".sql.gz") {
		t.Fatalf("dump %+v", info)
	}
	p := filepath.Join(e.d.Root, "/var/lib/cairn/backups/db/mysql", info.File)
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	_ = f.Close()
	if string(got) != dump {
		t.Fatalf("dump content %q", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("dump mode %v", fi.Mode())
	}

	e.reset()
	op, log = e.run(helperapi.ActionDBRestore, helperapi.DBRestoreRequest{Engine: "mysql", Name: "shop", File: info.File, User: "shop", Password: "pass#word1"})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("restore: %+v\n%s", op, log)
	}
	c := e.calls[0]
	if c.stdin != dump || !strings.Contains(c.line(), "--database=shop") || strings.Contains(c.line(), "pass#word1") {
		t.Fatalf("restore call %s with %q", c.line(), c.stdin)
	}
	// The credentials come from a quoted option file, removed afterwards.
	if cnf != "[client]\nuser=\"shop\"\npassword=\"pass#word1\"\n" {
		t.Fatalf("option file %q", cnf)
	}
	if left, _ := os.ReadDir(filepath.Join(e.d.Root, dbHelperTmp)); len(left) != 0 {
		t.Fatalf("option file left behind: %v", left)
	}
	if strings.Contains(log, "pass#word1") {
		t.Fatalf("the log shows the password:\n%s", log)
	}

	// Links never lead the helper out of the panel's directory.
	dir := filepath.Join(e.d.Root, "/var/lib/cairn/backups/db/mysql")
	if err := os.Symlink("/etc/shadow", filepath.Join(dir, "evil.sql")); err != nil {
		t.Fatal(err)
	}
	if op, _ := e.run(helperapi.ActionDBRestore, helperapi.DBRestoreRequest{Engine: "mysql", Name: "shop", File: "evil.sql", User: "shop", Password: "pass#word1"}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBDumpNotFound {
		t.Fatalf("restore through a link: %+v", op)
	}
}

const pgClusters = "17  main    5432 online postgres /var/lib/postgresql/17/main /var/log/postgresql/postgresql-17-main.log\n"

func newPGEnv(t *testing.T) *dbEnv {
	e := newDBEnv(t, map[string]string{"postgresql-17": "17.6-0+deb13u1", "postgresql-18": "18.1-1.pgdg13+1"})
	writeSystemFile(t, e.d.Root, "/etc/postgresql/17/main/pg_hba.conf", "local all postgres peer\nhost all all 127.0.0.1/32 scram-sha-256\n")
	return e
}

func TestPostgreSQLStatus(t *testing.T) {
	e := newPGEnv(t)
	e.reply = func(c dbCall) (string, error) {
		if c.name == "/usr/bin/pg_lsclusters" {
			return pgClusters, nil
		}
		return strings.Join([]string{
			"v\t17.6 (Debian 17.6-0+deb13u1)", "l\tlocalhost",
			"d\tpostgres\t7500000\tpostgres", "d\tshop\t8000000\tshop",
			"u\tpostgres", "u\tshop",
		}, "\n"), nil
	}
	s := e.status()
	if len(s.Engines) != 2 || s.Engines[0].ID != "postgresql-18" || !s.Engines[0].NoCluster {
		t.Fatalf("engines %+v", s.Engines)
	}
	p := s.Engines[1]
	if p.ID != "postgresql-17" || !p.Running || p.Remote || p.Port != 5432 || p.Version != "17.6 (Debian 17.6-0+deb13u1)" {
		t.Fatalf("engine %+v", p)
	}
	if len(p.Databases) != 1 || p.Databases[0].Owner != "shop" || len(p.Users) != 1 || p.Users[0].Hosts[0] != "localhost" {
		t.Fatalf("databases %+v users %+v", p.Databases, p.Users)
	}
	// psql runs as postgres with the cluster's own client.
	if c := e.calls[len(e.calls)-1]; !strings.HasPrefix(c.line(), "runuser -u postgres -- /usr/lib/postgresql/17/bin/psql") || !strings.Contains(c.line(), "-p 5432 -d postgres") {
		t.Fatalf("psql call %s", c.line())
	}
}

func TestPostgreSQLUsersAndHosts(t *testing.T) {
	e := newPGEnv(t)
	roleExists := false
	e.reply = func(c dbCall) (string, error) {
		switch {
		case c.name == "/usr/bin/pg_lsclusters":
			return pgClusters, nil
		case strings.HasPrefix(c.stdin, "SELECT 'r' FROM pg_roles"):
			if roleExists {
				return "r\n", nil
			}
			return "", nil
		case strings.HasPrefix(c.stdin, "SELECT 'd', datname"):
			return "d\tpostgres\tpostgres\nd\tshop\tshop\nd\tstats\tpostgres\n", nil
		case strings.Contains(c.stdin, "aclexplode"):
			return "g\told\n", nil
		}
		return "", nil
	}
	req := helperapi.DBUserRequest{Engine: "postgresql-17", Name: "report", Password: "S3cret-pass", Hosts: []string{"localhost", "%", "10.1.0.0/16"},
		Grants: []helperapi.DBGrant{{Database: "stats", Level: helperapi.GrantReadOnly}, {Database: "shop", Level: helperapi.GrantAll}}}
	missing := req
	missing.Grants = []helperapi.DBGrant{{Database: "missing", Level: helperapi.GrantAll}}
	if op, _ := e.run(helperapi.ActionDBUserSet, missing); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBNotFound {
		t.Fatalf("grant on an unknown database: %+v", op)
	}
	e.reply = func(c dbCall) (string, error) {
		switch {
		case c.name == "/usr/bin/pg_lsclusters":
			return pgClusters, nil
		case strings.HasPrefix(c.stdin, "SELECT 'd', datname"):
			return "d\tpostgres\tpostgres\nd\tshop\tadmin\nd\tstats\tpostgres\nd\told\tpostgres\n", nil
		case strings.Contains(c.stdin, "aclexplode"):
			return "g\told\n", nil
		}
		return "", nil
	}
	e.reset()
	op, log := e.run(helperapi.ActionDBUserSet, req)
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("user set: %+v\n%s", op, log)
	}
	byDB := map[string]string{}
	for _, c := range e.calls {
		for i, a := range c.args {
			if a == "-d" {
				byDB[c.args[i+1]] += c.stdin
			}
		}
	}
	if !strings.Contains(byDB["postgres"], `CREATE ROLE "report" LOGIN PASSWORD 'S3cret-pass';`) || !strings.Contains(byDB["postgres"], "SELECT pg_reload_conf();") {
		t.Fatalf("postgres script:\n%s", byDB["postgres"])
	}
	if !strings.Contains(byDB["stats"], `GRANT SELECT ON ALL TABLES IN SCHEMA public TO "report";`) || strings.Contains(byDB["stats"], "GRANT ALL") {
		t.Fatalf("read-only script:\n%s", byDB["stats"])
	}
	if !strings.Contains(byDB["shop"], `ALTER DEFAULT PRIVILEGES FOR ROLE "admin" IN SCHEMA public GRANT ALL ON TABLES TO "report";`) {
		t.Fatalf("full access script:\n%s", byDB["shop"])
	}
	// Access no longer listed is taken away.
	if !strings.Contains(byDB["old"], `REVOKE ALL ON DATABASE "old" FROM "report";`) || strings.Contains(byDB["old"], "GRANT") {
		t.Fatalf("revoke script:\n%s", byDB["old"])
	}
	if strings.Contains(log, "S3cret-pass") {
		t.Fatalf("the log shows the password:\n%s", log)
	}
	hba, _ := os.ReadFile(filepath.Join(e.d.Root, "/etc/postgresql/17/main/pg_hba.conf"))
	for _, want := range []string{"host all all 127.0.0.1/32 scram-sha-256\n" + pgHbaBegin, "host\tall\treport\t0.0.0.0/0\tscram-sha-256",
		"host\tall\treport\t::/0\tscram-sha-256", "host\tall\treport\t10.1.0.0/16\tscram-sha-256"} {
		if !strings.Contains(string(hba), want) {
			t.Fatalf("pg_hba.conf lacks %q:\n%s", want, hba)
		}
	}
	_, _, hosts := parseHba(string(hba))
	if strings.Join(hosts["report"], ",") != "%,10.1.0.0/16" {
		t.Fatalf("hosts read back %v", hosts)
	}

	// Dropping: a role owning a database stays; otherwise its objects go
	// to each database's owner first.
	roleExists = true
	e.reply = func(c dbCall) (string, error) {
		switch {
		case c.name == "/usr/bin/pg_lsclusters":
			return pgClusters, nil
		case strings.HasPrefix(c.stdin, "SELECT 'r' FROM pg_roles"):
			return "r\n", nil
		case strings.HasPrefix(c.stdin, "SELECT 'd', datname"):
			return "d\tpostgres\tpostgres\nd\tshop\treport\n", nil
		}
		return "", nil
	}
	if op, _ := e.run(helperapi.ActionDBUserDrop, helperapi.DBRef{Engine: "postgresql-17", Name: "report"}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBUserOwns {
		t.Fatalf("drop of an owner: %+v", op)
	}
	e.reply = func(c dbCall) (string, error) {
		switch {
		case c.name == "/usr/bin/pg_lsclusters":
			return pgClusters, nil
		case strings.HasPrefix(c.stdin, "SELECT 'r' FROM pg_roles"):
			return "r\n", nil
		case strings.HasPrefix(c.stdin, "SELECT 'd', datname"):
			return "d\tpostgres\tpostgres\nd\tshop\tadmin\n", nil
		}
		return "", nil
	}
	e.reset()
	if op, log := e.run(helperapi.ActionDBUserDrop, helperapi.DBRef{Engine: "postgresql-17", Name: "report"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("drop: %+v\n%s", op, log)
	}
	if sql := e.script(); !strings.Contains(sql, `REASSIGN OWNED BY "report" TO "admin";`) || !strings.Contains(sql, `DROP ROLE "report";`) {
		t.Fatalf("drop script:\n%s", sql)
	}
	if hba, _ := os.ReadFile(filepath.Join(e.d.Root, "/etc/postgresql/17/main/pg_hba.conf")); strings.Contains(string(hba), "report") || strings.Contains(string(hba), pgHbaBegin) {
		t.Fatalf("pg_hba.conf keeps the user:\n%s", hba)
	}
}

func TestPostgreSQLCreateClusterRemoteAndDump(t *testing.T) {
	e := newPGEnv(t)
	e.reply = func(c dbCall) (string, error) {
		switch {
		case c.name == "/usr/bin/pg_lsclusters":
			return pgClusters, nil
		case strings.Contains(c.stdin, "FROM pg_database WHERE datname = 'shop'"):
			return "d\n", nil
		case strings.Contains(c.line(), "pg_dump"):
			return "PGDMP-data", nil
		}
		return "", nil
	}
	if op, log := e.run(helperapi.ActionDBCreate, helperapi.DBCreateRequest{Engine: "postgresql-17", Name: "blog", Owner: "blog"}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBUserNotFound {
		t.Fatalf("create with a missing owner: %+v\n%s", op, log)
	}
	if op, log := e.run(helperapi.ActionDBCreate, helperapi.DBCreateRequest{Engine: "postgresql-17", Name: "blog"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("create: %+v\n%s", op, log)
	}
	if sql := e.script(); !strings.Contains(sql, `CREATE DATABASE "blog" TEMPLATE template0 ENCODING 'UTF8';`) || !strings.Contains(sql, `REVOKE CONNECT, TEMPORARY ON DATABASE "blog" FROM PUBLIC;`) {
		t.Fatalf("create script:\n%s", sql)
	}
	// The line without a cluster gets one.
	if op, log := e.run(helperapi.ActionDBPGCluster, helperapi.DBEngineRef{Engine: "postgresql-18"}); op.State != helperapi.OpSucceeded || !e.f.ran("pg_createcluster 18 main --start") {
		t.Fatalf("cluster: %+v\n%s", op, log)
	}
	if op, _ := e.run(helperapi.ActionDBPGCluster, helperapi.DBEngineRef{Engine: "postgresql-17"}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBExists {
		t.Fatalf("second cluster: %+v", op)
	}
	// Remote access works without systemd: pg_ctlcluster restarts.
	if op, log := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "postgresql-17", Enabled: true}); op.State != helperapi.OpSucceeded || !e.f.ran("pg_ctlcluster 17 main restart") {
		t.Fatalf("remote: %+v\n%s", op, log)
	}
	if conf, _ := os.ReadFile(filepath.Join(e.d.Root, "/etc/postgresql/17/main/conf.d/99-cairn.conf")); !strings.Contains(string(conf), "listen_addresses = '*'") {
		t.Fatalf("config %q", conf)
	}

	e.reset()
	op, log := e.run(helperapi.ActionDBDump, helperapi.DBDumpRequest{Engine: "postgresql-17", Name: "shop"})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("dump: %+v\n%s", op, log)
	}
	res, _ := e.o.Get(op.ID, 0)
	var info helperapi.DumpInfo
	decode(t, res.Op.Result, &info)
	data, err := os.ReadFile(filepath.Join(e.d.Root, "/var/lib/cairn/backups/db/postgresql-17", info.File))
	if err != nil || string(data) != "PGDMP-data" || !strings.HasSuffix(info.File, ".dump") {
		t.Fatalf("dump %+v: %q %v", info, data, err)
	}
	e.reset()
	if op, log := e.run(helperapi.ActionDBRestore, helperapi.DBRestoreRequest{Engine: "postgresql-17", Name: "shop", File: info.File, User: "shop", Password: "S3cret-pass"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("restore: %+v\n%s", op, log)
	}
	c := e.calls[len(e.calls)-1]
	if c.name != "/usr/lib/postgresql/17/bin/pg_restore" || !strings.Contains(c.line(), "-h 127.0.0.1 -p 5432 -U shop -d shop") || c.stdin != "PGDMP-data" ||
		!bytes.Contains([]byte(strings.Join(c.env, "\n")), []byte("PGPASSWORD=S3cret-pass")) {
		t.Fatalf("restore call %s env %v stdin %q", c.line(), c.env, c.stdin)
	}
}

func TestDBValidation(t *testing.T) {
	for _, tc := range []struct {
		req  interface{ Validate() error }
		fine bool
	}{
		{helperapi.DBCreateRequest{Engine: "mysql", Name: "shop"}, true},
		{helperapi.DBCreateRequest{Engine: "mysql", Name: "information_schema"}, false},
		{helperapi.DBCreateRequest{Engine: "mysql", Name: "a`b"}, false},
		{helperapi.DBCreateRequest{Engine: "postgresql-17", Name: "Shop"}, false},
		{helperapi.DBCreateRequest{Engine: "postgresql-17", Name: "pg_x"}, false},
		{helperapi.DBCreateRequest{Engine: "redis", Name: "x"}, false},
		{helperapi.DBCreateRequest{Engine: "oracle", Name: "x"}, false},
		{helperapi.DBUserRequest{Engine: "mysql", Name: "root", Hosts: []string{"localhost"}}, false},
		{helperapi.DBUserRequest{Engine: "mysql", Name: "app", Password: "short", Hosts: []string{"localhost"}}, false},
		{helperapi.DBUserRequest{Engine: "mysql", Name: "app", Password: "it's-a-pass", Hosts: []string{"localhost"}}, false},
		{helperapi.DBUserRequest{Engine: "mysql", Name: "app", Password: "good-pass#1", Hosts: []string{"10.0.0.0/8", "%", "1.2.3.4"}}, true},
		{helperapi.DBUserRequest{Engine: "mysql", Name: "app", Hosts: []string{"example.com"}}, false},
		{helperapi.DBUserRequest{Engine: "mysql", Name: "app", Hosts: []string{"localhost"}, Grants: []helperapi.DBGrant{{Database: "mysql", Level: "all"}}}, false},
		{helperapi.DBRestoreRequest{Engine: "mysql", Name: "shop", File: "../x.sql", User: "shop", Password: "good-pass#1"}, false},
		{helperapi.DBRestoreRequest{Engine: "mysql", Name: "shop", File: "shop.sql.gz", User: "shop", Password: "good-pass#1"}, true},
		{helperapi.RedisScanRequest{DB: 16}, false},
		{helperapi.RedisDeleteRequest{Keys: []string{}}, false},
	} {
		if err := tc.req.Validate(); (err == nil) != tc.fine {
			t.Errorf("%+v: %v", tc.req, err)
		}
	}
}

func decode(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}
