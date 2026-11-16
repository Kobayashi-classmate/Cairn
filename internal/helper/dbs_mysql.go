// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// MariaDB and MySQL, administered as root over the socket (unix_socket or
// auth_socket authentication on Debian and Ubuntu).

// sqlScript collects statements twice: as run, and as logged with the
// passwords masked.
type sqlScript struct{ run, show strings.Builder }

// secret is an argument of sqlScript.add that the log must not show.
type secret string

func (s *sqlScript) add(format string, args ...any) {
	shown := make([]any, len(args))
	for i, a := range args {
		if sec, ok := a.(secret); ok {
			args[i], shown[i] = string(sec), "'********'"
		} else {
			shown[i] = a
		}
	}
	_, _ = fmt.Fprintf(&s.run, format+"\n", args...)
	_, _ = fmt.Fprintf(&s.show, format+"\n", shown...)
}

// myStr quotes a MySQL string literal.
func myStr(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\x00", `\0`).Replace(s) + "'"
}

// myID quotes a MySQL identifier.
func myID(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// myGrantDB quotes a database name of a GRANT, where _ and % are
// wildcards: "my_app" must not also grant "myxapp".
func myGrantDB(s string) string {
	return myID(strings.NewReplacer(`\`, `\\`, `_`, `\_`, `%`, `\%`).Replace(s))
}

func myAccount(user, host string) string { return myStr(user) + "@" + myStr(host) }

// myHost converts a request's host to MySQL's form: a CIDR becomes
// address/netmask, which MariaDB and MySQL both understand.
func myHost(h string) string {
	if _, n, err := net.ParseCIDR(h); err == nil && n.IP.To4() != nil {
		return n.IP.String() + "/" + net.IP(n.Mask).String()
	}
	return h
}

// fromMyHost is myHost's inverse, for status.
func fromMyHost(h string) string {
	addr, mask, ok := strings.Cut(h, "/")
	if !ok {
		return h
	}
	ip, m := net.ParseIP(addr).To4(), net.ParseIP(mask).To4()
	if ip == nil || m == nil {
		return h
	}
	ones, bits := net.IPMask(m).Size()
	if bits == 0 {
		return h
	}
	return ip.String() + "/" + strconv.Itoa(ones)
}

// unescapeBatch undoes the escaping of the client's batch output.
func unescapeBatch(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '0':
			b.WriteByte(0)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// parseRows splits tab-separated output into rows.
func parseRows(out []byte, unescape bool) [][]string {
	var rows [][]string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if unescape {
			for i := range cols {
				cols[i] = unescapeBatch(cols[i])
			}
		}
		rows = append(rows, cols)
	}
	return rows
}

func (d *DBs) tool(names ...string) string {
	for _, n := range names {
		if _, err := os.Stat(d.path("/usr/bin/" + n)); err == nil {
			return "/usr/bin/" + n
		}
	}
	return "/usr/bin/" + names[len(names)-1]
}

// mysqlError maps a client failure to a protocol error.
func mysqlError(err error) error {
	detail := errDetail(err)
	switch {
	case strings.Contains(detail, "ERROR 2002"), strings.Contains(detail, "ERROR 2003"), strings.Contains(detail, "error: 2002"):
		return dbErr(helperapi.CodeDBNotRunning, map[string]any{"engine": helperapi.DBMySQL})
	case strings.Contains(detail, "ERROR 1045"), strings.Contains(detail, "ERROR 1698"):
		return dbErr(helperapi.CodeDBAccessDenied, map[string]any{"engine": helperapi.DBMySQL, "detail": detail})
	case strings.Contains(detail, "ERROR 1049"), strings.Contains(detail, "error: 1049:"):
		return dbErr(helperapi.CodeDBNotFound, map[string]any{"detail": detail})
	}
	return dbErr(helperapi.CodeDBQuery, map[string]any{"detail": detail})
}

// mysqlExec runs a script as root and returns the rows it printed.
func (d *DBs) mysqlExec(ctx context.Context, log io.Writer, s *sqlScript) ([][]string, error) {
	if log != nil {
		_, _ = fmt.Fprintf(log, "$ mysql <<SQL\n%sSQL\n", s.show.String())
	}
	res, err := dbRun(ctx, execx.Cmd{
		Name: d.tool("mariadb", "mysql"), Args: []string{"--protocol=socket", "--batch", "--skip-column-names", "--default-character-set=utf8mb4"},
		Stdin: strings.NewReader(s.run.String()), Timeout: dbTimeout, MaxOutput: 8 << 20,
	})
	if err != nil {
		return nil, mysqlError(err)
	}
	return parseRows(res.Stdout, true), nil
}

func mysqlQuery(format string, args ...any) *sqlScript {
	var s sqlScript
	s.add(format, args...)
	return &s
}

func isLoopback(addr string) bool {
	addr = strings.TrimPrefix(strings.TrimSpace(addr), "-")
	if addr == "localhost" {
		return true
	}
	ip := net.ParseIP(addr)
	return ip != nil && ip.IsLoopback()
}

func (d *DBs) mysqlStatus(ctx context.Context, _ dbEngine, info *helperapi.DBEngineInfo) error {
	rows, err := d.mysqlExec(ctx, nil, mysqlQuery(`SELECT 'v', VERSION();
SHOW GLOBAL VARIABLES WHERE Variable_name IN ('bind_address', 'port', 'skip_networking');
SELECT 'd', s.SCHEMA_NAME, COALESCE(SUM(t.DATA_LENGTH + t.INDEX_LENGTH), 0) FROM information_schema.SCHEMATA s LEFT JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = s.SCHEMA_NAME GROUP BY s.SCHEMA_NAME ORDER BY s.SCHEMA_NAME;
SELECT 'u', User, Host FROM mysql.user ORDER BY User, Host;`))
	if err != nil {
		if asError(err).Code == helperapi.CodeDBNotRunning {
			return nil
		}
		return err
	}
	info.Running = true
	hosts := map[string][]string{}
	var users []string
	bind, skip := "", false
	for _, r := range rows {
		switch {
		case r[0] == "v" && len(r) > 1:
			info.Version = r[1]
		case r[0] == "bind_address" && len(r) > 1:
			bind = r[1]
		case r[0] == "port" && len(r) > 1:
			info.Port, _ = strconv.Atoi(r[1])
		case r[0] == "skip_networking" && len(r) > 1:
			skip = r[1] == "ON"
		case r[0] == "d" && len(r) > 2:
			if helperapi.ValidDBName(helperapi.DBMySQL, r[1]) {
				size, _ := strconv.ParseInt(r[2], 10, 64)
				info.Databases = append(info.Databases, helperapi.DBInfo{Name: r[1], SizeBytes: size})
			}
		case r[0] == "u" && len(r) > 2:
			if helperapi.ValidDBUser(helperapi.DBMySQL, r[1]) {
				if _, ok := hosts[r[1]]; !ok {
					users = append(users, r[1])
				}
				hosts[r[1]] = append(hosts[r[1]], fromMyHost(r[2]))
			}
		}
	}
	// MySQL listens everywhere when nothing is set.
	info.Remote = !skip && (bind == "" || slices.ContainsFunc(strings.Split(bind, ","), func(a string) bool { return !isLoopback(a) }))
	for _, u := range users {
		info.Users = append(info.Users, helperapi.DBUserInfo{Name: u, Hosts: hosts[u]})
	}
	return nil
}

// mysqlHosts returns the host entries of a user, as stored.
func (d *DBs) mysqlHosts(ctx context.Context, user string) ([]string, error) {
	rows, err := d.mysqlExec(ctx, nil, mysqlQuery("SELECT 'h', Host FROM mysql.user WHERE User = %s;", myStr(user)))
	if err != nil {
		return nil, err
	}
	var hosts []string
	for _, r := range rows {
		if r[0] == "h" && len(r) > 1 {
			hosts = append(hosts, r[1])
		}
	}
	return hosts, nil
}

func (d *DBs) mysqlCreate(ctx context.Context, log io.Writer, _ dbEngine, req helperapi.DBCreateRequest) error {
	rows, err := d.mysqlExec(ctx, nil, mysqlQuery("SELECT 'd' FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = %s;", myStr(req.Name)))
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		return dbErr(helperapi.CodeDBExists, map[string]any{"name": req.Name})
	}
	var hosts []string
	if req.Owner != "" {
		if hosts, err = d.mysqlHosts(ctx, req.Owner); err != nil {
			return err
		}
		if len(hosts) == 0 {
			return dbErr(helperapi.CodeDBUserNotFound, map[string]any{"name": req.Owner})
		}
	}
	var s sqlScript
	s.add("CREATE DATABASE %s CHARACTER SET utf8mb4;", myID(req.Name))
	for _, h := range hosts {
		s.add("GRANT ALL PRIVILEGES ON %s.* TO %s;", myGrantDB(req.Name), myAccount(req.Owner, h))
	}
	_, err = d.mysqlExec(ctx, log, &s)
	return err
}

func (d *DBs) mysqlDrop(ctx context.Context, log io.Writer, _ dbEngine, name string) error {
	escaped := strings.NewReplacer(`_`, `\_`, `%`, `\%`).Replace(name)
	rows, err := d.mysqlExec(ctx, nil, mysqlQuery("SELECT 'd' FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = %s;\nSELECT 'g', User, Host, Db FROM mysql.db WHERE Db IN (%s, %s);",
		myStr(name), myStr(name), myStr(escaped)))
	if err != nil {
		return err
	}
	var s sqlScript
	found := false
	for _, r := range rows {
		switch {
		case r[0] == "d":
			found = true
		case r[0] == "g" && len(r) > 3:
			// Grants outlive the database; take them with it.
			s.add("REVOKE ALL PRIVILEGES ON %s.* FROM %s;", myID(r[3]), myAccount(r[1], r[2]))
		}
	}
	if !found {
		return dbErr(helperapi.CodeDBNotFound, map[string]any{"name": name})
	}
	s.add("DROP DATABASE %s;", myID(name))
	_, err = d.mysqlExec(ctx, log, &s)
	return err
}

func (d *DBs) mysqlUserSet(ctx context.Context, log io.Writer, _ dbEngine, req helperapi.DBUserRequest) error {
	existing, err := d.mysqlHosts(ctx, req.Name)
	if err != nil {
		return err
	}
	if req.New && len(existing) > 0 {
		return dbErr(helperapi.CodeDBUserExists, map[string]any{"name": req.Name})
	}
	var wanted []string
	for _, h := range req.Hosts {
		if h = myHost(h); !slices.Contains(wanted, h) {
			wanted = append(wanted, h)
		}
	}
	var s sqlScript
	for _, h := range wanted {
		switch {
		case !slices.Contains(existing, h) && req.Password == "":
			return dbErr(helperapi.CodeDBPasswordNeed, map[string]any{"name": req.Name})
		case !slices.Contains(existing, h):
			s.add("CREATE USER %s IDENTIFIED BY %s;", myAccount(req.Name, h), secret(myStr(req.Password)))
		case req.Password != "":
			s.add("ALTER USER %s IDENTIFIED BY %s;", myAccount(req.Name, h), secret(myStr(req.Password)))
		}
	}
	for _, h := range existing {
		if !slices.Contains(wanted, h) {
			s.add("DROP USER %s;", myAccount(req.Name, h))
		}
	}
	for _, h := range wanted {
		s.add("REVOKE ALL PRIVILEGES, GRANT OPTION FROM %s;", myAccount(req.Name, h))
		for _, g := range req.Grants {
			privs := "ALL PRIVILEGES"
			if g.Level == helperapi.GrantReadOnly {
				privs = "SELECT, SHOW VIEW"
			}
			s.add("GRANT %s ON %s.* TO %s;", privs, myGrantDB(g.Database), myAccount(req.Name, h))
		}
	}
	_, err = d.mysqlExec(ctx, log, &s)
	return err
}

func (d *DBs) mysqlUserDrop(ctx context.Context, log io.Writer, _ dbEngine, name string) error {
	hosts, err := d.mysqlHosts(ctx, name)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return dbErr(helperapi.CodeDBUserNotFound, map[string]any{"name": name})
	}
	var s sqlScript
	for _, h := range hosts {
		s.add("DROP USER %s;", myAccount(name, h))
	}
	_, err = d.mysqlExec(ctx, log, &s)
	return err
}

// mysqlConfDir is where the server reads its configuration snippets.
func (d *DBs) mysqlConfDir(e dbEngine) string {
	if e.software == "mariadb" {
		return "/etc/mysql/mariadb.conf.d"
	}
	if _, err := os.Stat(d.path("/etc/mysql/mysql.conf.d")); err == nil {
		return "/etc/mysql/mysql.conf.d"
	}
	return "/etc/mysql/conf.d"
}

// mysqlRemote sets the address the server listens on and restarts it.
func (d *DBs) mysqlRemote(ctx context.Context, log io.Writer, e dbEngine, enabled bool) error {
	if !systemdRunning() {
		return dbErr(helperapi.CodeDBSystemd, map[string]any{"engine": e.id})
	}
	bind := "127.0.0.1"
	if enabled {
		bind = "0.0.0.0"
	}
	conf := fmt.Sprintf("# %s\n[mysqld]\nbind-address = %s\n", dbManagedNote, bind)
	return d.reconfigure(ctx, log, e, path.Join(d.mysqlConfDir(e), "99-cairn.cnf"), []byte(conf),
		func() error { return d.systemctl(ctx, log, e, helperapi.ServiceRestart) })
}

// reconfigure writes a configuration file and restarts the server; when
// the server does not come back, the previous file returns.
func (d *DBs) reconfigure(ctx context.Context, log io.Writer, e dbEngine, p string, data []byte, restart func() error) error {
	old, readErr := os.ReadFile(d.path(p))                          //nolint:gosec // G304: fixed configuration paths.
	if err := os.MkdirAll(path.Dir(d.path(p)), 0o755); err != nil { //nolint:gosec // G301: the server's configuration directories are world-readable.
		return err
	}
	if err := d.writeConfig(p, data); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(log, "wrote %s\n", p)
	err := restart()
	if err == nil {
		return nil
	}
	if readErr == nil {
		_ = d.writeConfig(p, old)
	} else {
		_ = os.Remove(d.path(p))
	}
	_, _ = fmt.Fprintf(log, "restart failed; %s restored\n", p)
	_ = restart()
	return dbErr(helperapi.CodeDBRestartFailed, map[string]any{"engine": e.id, "detail": errDetail(err)})
}

func (d *DBs) mysqlDump(ctx context.Context, log io.Writer, e dbEngine, name string) (helperapi.DumpInfo, error) {
	return d.writeDump(e.id, name, ".sql.gz", true, func(w io.Writer) error { return d.mysqlDumpTo(ctx, log, e, name, w) })
}

// mysqlDumpTo writes a database's SQL dump to w.
func (d *DBs) mysqlDumpTo(ctx context.Context, log io.Writer, e dbEngine, name string, w io.Writer) error {
	args := []string{"--protocol=socket", "--single-transaction", "--quick", "--routines", "--triggers", "--events",
		"--hex-blob", "--default-character-set=utf8mb4"}
	if e.software == "mysql" {
		args = append(args, "--set-gtid-purged=OFF")
	}
	args = append(args, name)
	tool := d.tool("mariadb-dump", "mysqldump")
	_, _ = fmt.Fprintf(log, "$ %s %s\n", path.Base(tool), strings.Join(args, " "))
	if _, err := dbRun(ctx, execx.Cmd{Name: tool, Args: args, Stdout: w, Output: log, Timeout: dumpTimeout, MaxOutput: 64 << 10}); err != nil {
		return mysqlError(err)
	}
	return nil
}

// mysqlRestore loads a dump as the given user: the client reads the
// credentials from a root-only option file, never from its arguments.
func (d *DBs) mysqlRestore(ctx context.Context, log io.Writer, e dbEngine, req helperapi.DBRestoreRequest) error {
	if !strings.HasSuffix(req.File, ".sql") && !strings.HasSuffix(req.File, ".sql.gz") {
		return dbErr(helperapi.CodeDBQuery, map[string]any{"detail": "unsupported dump format"})
	}
	in, err := d.openDump(e.id, req.File)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	// Option file values are quoted: # would start a comment.
	cnf, cleanup, err := d.tempFile("client-*.cnf", fmt.Appendf(nil, "[client]\nuser=\"%s\"\npassword=\"%s\"\n", req.User, req.Password))
	if err != nil {
		return err
	}
	defer cleanup()
	args := []string{"--defaults-extra-file=" + cnf, "--protocol=socket", "--default-character-set=utf8mb4", "--binary-mode",
		"--max-allowed-packet=1073741824", "--database=" + req.Name}
	_, _ = fmt.Fprintf(log, "$ mysql --user=%s --database=%s < %s\n", req.User, req.Name, req.File)
	_, err = dbRun(ctx, execx.Cmd{Name: d.tool("mariadb", "mysql"), Args: args, Stdin: in, Output: log, Timeout: dumpTimeout, MaxOutput: 64 << 10})
	if err != nil {
		return mysqlError(err)
	}
	return nil
}
