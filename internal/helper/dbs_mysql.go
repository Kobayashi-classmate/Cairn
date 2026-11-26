// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

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

// myStr quotes a MySQL string literal. Every script the helper runs as
// root starts with mysqlSession, so a backslash in a literal is an escape
// character whatever the server's sql_mode: it is doubled. A quote is
// written twice, as standard SQL has it, which reads the same with
// NO_BACKSLASH_ESCAPES or without.
func myStr(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`, "\x00", `\0`).Replace(s) + "'"
}

// mysqlSession is the first line of every script the helper runs as root.
// An administrator may have put NO_BACKSLASH_ESCAPES into the server's
// sql_mode, which changes where a literal with a backslash ends, for the
// server and for the client alike (the client splits the script into
// statements by the server's mode): the session takes it out, on a line of
// its own, before any statement of the helper's.
const mysqlSession = "SET SESSION sql_mode = TRIM(BOTH ',' FROM REPLACE(CONCAT(',', @@SESSION.sql_mode, ','), ',NO_BACKSLASH_ESCAPES,', ','));\n"

// mysqlEntry is what the helper takes back from the server into a
// statement of its own (user names and hosts of accounts): letters,
// digits and the punctuation of host patterns, the anonymous user's empty
// name included. The panel never makes anything else; another name was
// made by hand, and is refused rather than quoted.
var mysqlEntry = regexp.MustCompile(`^[A-Za-z0-9_.%:/@+-]{0,255}$`)

// checkEntry refuses a value read from the server that mysqlEntry does
// not take.
func checkEntry(v string) error {
	if mysqlEntry.MatchString(v) {
		return nil
	}
	return unsafeEntry(v)
}

func unsafeEntry(v string) error {
	shown := strconv.Quote(v)
	if len(shown) > 80 {
		shown = shown[:77] + "..."
	}
	return dbErr(helperapi.CodeDBUnsafeEntry, map[string]any{"value": shown})
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
	// --binary-mode turns the client's own commands (system, source, tee)
	// off: a statement split where the helper did not mean it can never
	// become one. The script starts with mysqlSession (not logged).
	res, err := dbRun(ctx, execx.Cmd{
		Name: d.tool("mariadb", "mysql"), Args: []string{"--protocol=socket", "--batch", "--skip-column-names", "--default-character-set=utf8mb4", "--binary-mode"},
		Stdin: strings.NewReader(mysqlSession + s.run.String()), Timeout: dbTimeout, MaxOutput: 8 << 20,
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
	info.Remote = mysqlListensRemotely(bind, skip)
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
			if err := checkEntry(r[1]); err != nil {
				return nil, err
			}
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
			if r[3] != name && r[3] != escaped {
				return unsafeEntry(r[3])
			}
			for _, v := range r[1:3] {
				if err := checkEntry(v); err != nil {
					return err
				}
			}
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
	// Nil hosts keep the user's host entries (an existing user only).
	if req.Hosts == nil && len(existing) == 0 {
		return dbErr(helperapi.CodeDBUserNotFound, map[string]any{"name": req.Name})
	}
	if err := d.mysqlRefusePrivileged(ctx, req.Name, existing); err != nil {
		return err
	}
	wanted := slices.Clone(existing)
	if req.Hosts != nil {
		wanted = nil
		for _, h := range req.Hosts {
			if h = myHost(h); !slices.Contains(wanted, h) {
				wanted = append(wanted, h)
			}
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
	// Nil grants leave the privileges as they are: a password or host
	// change never revokes anything (new host entries start without any).
	for _, h := range wanted {
		if req.Grants == nil {
			break
		}
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
	if err := d.mysqlRefusePrivileged(ctx, name, hosts); err != nil {
		return err
	}
	var s sqlScript
	for _, h := range hosts {
		s.add("DROP USER %s;", myAccount(name, h))
	}
	_, err = d.mysqlExec(ctx, log, &s)
	return err
}

// mysqlRefusePrivileged refuses a user with rights beyond single
// databases (see mysqlBeyondDB), from the grants of every host entry it
// has. The helper changes and drops only users of single databases:
// whoever asks it to, the panel or someone in its place, could otherwise
// set the password or the hosts of an account with rights over the whole
// server, its files or the other accounts, and take it over.
func (d *DBs) mysqlRefusePrivileged(ctx context.Context, user string, hosts []string) error {
	if len(hosts) == 0 {
		return nil
	}
	var s sqlScript
	for _, h := range hosts {
		s.add("SHOW GRANTS FOR %s;", myAccount(user, h))
	}
	rows, err := d.mysqlExec(ctx, nil, &s)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if why := mysqlBeyondDB(r[0]); why != "" {
			return dbErr(helperapi.CodeDBUserPrivileged, map[string]any{"name": user, "privilege": why})
		}
	}
	return nil
}

// mysqlBeyondDB says what a line of SHOW GRANTS gives beyond the use of
// single databases ("" for nothing): any privilege on every database but
// USAGE (MySQL lists its dynamic privileges there too) or the grant option
// there, PROXY, a role (what it may do is not on the line), or a grant on
// the mysql database, or on a pattern that covers it, whose tables hold
// the accounts. A line the helper cannot read counts as more.
func mysqlBeyondDB(line string) string {
	rest, ok := strings.CutPrefix(line, "GRANT ")
	if !ok {
		return "" // SET DEFAULT ROLE; REVOKE (partial revokes) takes away
	}
	on, to := grantIndex(rest, " ON "), grantIndex(rest, " TO ")
	if to < 0 {
		return strings.TrimSpace(rest)
	}
	if on < 0 || on > to {
		return "role " + strings.TrimSpace(rest[:to])
	}
	privs, level := strings.TrimSpace(rest[:on]), strings.TrimSpace(rest[on+len(" ON "):to])
	if strings.HasPrefix(privs, "PROXY") {
		return "PROXY"
	}
	if level == "*.*" {
		if privs != "USAGE" || strings.Contains(rest[to:], " GRANT OPTION") {
			return privs + " ON *.*"
		}
		return ""
	}
	for _, kind := range []string{"PROCEDURE ", "FUNCTION ", "TABLE "} {
		level = strings.TrimPrefix(level, kind)
	}
	db, object, ok := grantLevel(level)
	switch {
	case !ok:
		return privs + " ON " + level
	case object == "*" && grantCovers(db, "mysql"), object != "*" && strings.EqualFold(db, "mysql"):
		return privs + " ON " + level
	}
	return ""
}

// grantIndex is where sep starts in a grant, outside quotes and
// parentheses (column lists); -1 when it does not.
func grantIndex(s, sep string) int {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			// A doubled quote closes and opens again.
			if c == quote {
				quote = 0
			}
		case c == '`' || c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && strings.HasPrefix(s[i:], sep):
			return i
		}
	}
	return -1
}

// grantLevel splits `db`.object (or db.object) into its database and
// object.
func grantLevel(level string) (string, string, bool) {
	if !strings.HasPrefix(level, "`") {
		return strings.Cut(level, ".")
	}
	var b strings.Builder
	for i := 1; i < len(level); i++ {
		if level[i] != '`' {
			b.WriteByte(level[i])
			continue
		}
		if i+1 < len(level) && level[i+1] == '`' {
			b.WriteByte('`')
			i++
			continue
		}
		object, ok := strings.CutPrefix(level[i+1:], ".")
		return b.String(), object, ok
	}
	return "", "", false
}

// grantCovers reports whether a database pattern of a grant (% and _ are
// wildcards, \ escapes) takes in name, letter case aside.
func grantCovers(pattern, name string) bool {
	var re strings.Builder
	re.WriteString("(?is)^")
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		switch c := runes[i]; {
		case c == '%':
			re.WriteString(".*")
		case c == '_':
			re.WriteString(".")
		case c == '\\' && i+1 < len(runes):
			i++
			re.WriteString(regexp.QuoteMeta(string(runes[i])))
		default:
			re.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	re.WriteString("$")
	m, err := regexp.MatchString(re.String(), name)
	return err != nil || m
}

// mysqlConfDir is where the server reads its configuration snippets.
func (d *DBs) mysqlConfDir(e dbEngine) string {
	// EL (MariaDB and MySQL alike).
	if _, err := os.Stat(d.path("/etc/my.cnf.d")); err == nil {
		return "/etc/my.cnf.d"
	}
	if e.software == "mariadb" {
		return "/etc/mysql/mariadb.conf.d"
	}
	if _, err := os.Stat(d.path("/etc/mysql/mysql.conf.d")); err == nil {
		return "/etc/mysql/mysql.conf.d"
	}
	return "/etc/mysql/conf.d"
}

// The panel's configuration snippet of MySQL and MariaDB. The server reads
// a directory's snippets in name order and the last setting wins: Ubuntu's
// mysql.conf.d/mysqld.cnf, which sets bind-address too, came after the
// name earlier releases used.
const (
	mysqlConfName   = "zz-cairn.cnf"
	mysqlConfLegacy = "99-cairn.cnf"
)

// mysqlConfDirs are the directories mysqlConfDir chooses from.
var mysqlConfDirs = []string{"/etc/my.cnf.d", "/etc/mysql/mariadb.conf.d", "/etc/mysql/mysql.conf.d", "/etc/mysql/conf.d"}

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
	dir := d.mysqlConfDir(e)
	return d.reconfigure(ctx, log, e, []confEdit{{path.Join(dir, mysqlConfName), []byte(conf)}, {path.Join(dir, mysqlConfLegacy), nil}},
		func() error { return d.systemctl(ctx, log, e, helperapi.ServiceRestart) },
		func() error { return d.mysqlCheckRemote(ctx, log, e, enabled) })
}

// mysqlListensRemotely tells from bind_address and skip_networking
// whether the server takes connections from other hosts: MySQL listens
// everywhere when nothing is set.
func mysqlListensRemotely(bind string, skip bool) bool {
	return !skip && (bind == "" || slices.ContainsFunc(strings.Split(bind, ","), func(a string) bool { return !isLoopback(a) }))
}

// mysqlCheckRemote reads back, after a restart, where the server listens:
// a file read after the panel's, or an option on the command line, can
// set it too.
func (d *DBs) mysqlCheckRemote(ctx context.Context, log io.Writer, e dbEngine, enabled bool) error {
	var rows [][]string
	err := retryRunning(ctx, func() error {
		var err error
		rows, err = d.mysqlExec(ctx, nil, mysqlQuery("SHOW GLOBAL VARIABLES WHERE Variable_name IN ('bind_address', 'skip_networking');"))
		return err
	})
	if err != nil {
		return err
	}
	bind, skip := "", false
	for _, r := range rows {
		switch {
		case r[0] == "bind_address" && len(r) > 1:
			bind = r[1]
		case r[0] == "skip_networking" && len(r) > 1:
			skip = r[1] == "ON"
		}
	}
	if mysqlListensRemotely(bind, skip) == enabled {
		return nil
	}
	listen := bind
	if skip {
		listen = "skip_networking"
	}
	_, _ = fmt.Fprintf(log, "the server listens on %q after the restart: another setting overrides the panel's\n", listen)
	return dbErr(helperapi.CodeDBRemoteNotApplied, map[string]any{"engine": e.id, "listen": listen})
}

// dbRetryWait is how long the helper waits before asking a server that has
// just restarted again; tests shorten it.
var dbRetryWait = time.Second

// retryRunning runs fn until it does not fail for a server that is not
// running yet, for up to half a minute.
func retryRunning(ctx context.Context, fn func() error) error {
	for i := 0; ; i++ {
		err := fn()
		if err == nil || i >= 30 || asError(err).Code != helperapi.CodeDBNotRunning {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(dbRetryWait):
		}
	}
}

// MigrateConfig renames the MySQL snippet of earlier releases to the one
// read last, where the server finds it (see mysqlConfName); it takes
// effect when the server restarts next. Failures are logged; the helper
// carries on.
func (d *DBs) MigrateConfig(logger *slog.Logger) {
	for _, dir := range mysqlConfDirs {
		old, p := path.Join(dir, mysqlConfLegacy), path.Join(dir, mysqlConfName)
		data, err := d.readConfig(old)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil && !strings.Contains(string(data), dbManagedNote) {
			continue
		}
		if err == nil {
			if _, err = d.readConfig(p); err == nil {
				err = confDBErr(removeConfFile(d.Root, old))
			} else if errors.Is(err, fs.ErrNotExist) {
				err = errors.Join(d.writeConfig(p, data), confDBErr(removeConfFile(d.Root, old)))
			}
		}
		if err != nil {
			logger.Error("renaming the panel's MySQL configuration failed", "file", old, "err", err)
			continue
		}
		logger.Info("the panel's MySQL configuration was renamed; it takes effect when the server restarts", "from", old, "to", p)
	}
}

// confEdit is a configuration file reconfigure sets: Data is its new
// content; nil removes it.
type confEdit struct {
	path string
	data []byte
}

// reconfigure changes configuration files and restarts the server; verify
// (nil: none) then checks that the server took the change. When the server
// does not come back, or verify fails, the previous files return and the
// server restarts with them.
func (d *DBs) reconfigure(ctx context.Context, log io.Writer, e dbEngine, edits []confEdit, restart, verify func() error) error {
	old := make([][]byte, len(edits)) // nil: the file did not exist
	for i, ed := range edits {
		data, err := d.readConfig(ed.path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err == nil && data == nil {
			data = []byte{}
		}
		old[i] = data
	}
	for i, ed := range edits {
		var err error
		switch {
		case ed.data != nil:
			if err = d.writeConfig(ed.path, ed.data); err == nil {
				_, _ = fmt.Fprintf(log, "wrote %s\n", ed.path)
			}
		case old[i] != nil:
			if err = confDBErr(removeConfFile(d.Root, ed.path)); err == nil {
				_, _ = fmt.Fprintf(log, "removed %s\n", ed.path)
			}
		}
		if err != nil {
			if back := d.putBack(edits[:i], old[:i]); back != nil {
				_, _ = fmt.Fprintf(log, "restoring the configuration failed too: %v\n", back)
				return errors.Join(err, back)
			}
			return err
		}
	}
	err := restart()
	if err == nil && verify != nil {
		err = verify()
	}
	if err == nil {
		return nil
	}
	// A server that came back elsewhere than asked keeps its own code.
	code, detail, params := helperapi.CodeDBRestartFailed, errDetail(err), map[string]any{"engine": e.id}
	if he := asError(err); he.Code == helperapi.CodeDBRemoteNotApplied {
		code, detail, params["listen"] = he.Code, "", he.Params["listen"]
	}
	if back := d.putBack(edits, old); back != nil {
		_, _ = fmt.Fprintf(log, "restoring the previous configuration failed: %v\n", back)
		params["detail"] = strings.TrimPrefix(detail+"; restoring the configuration: "+back.Error(), "; ")
		return dbErr(code, params)
	}
	_, _ = fmt.Fprintln(log, "the previous configuration is back")
	if rerr := restart(); rerr != nil {
		_, _ = fmt.Fprintf(log, "the server did not start with the previous configuration either: %v\n", rerr)
		detail = strings.TrimPrefix(detail+"; restarting with the previous configuration: "+errDetail(rerr), "; ")
	}
	if detail != "" {
		params["detail"] = detail
	}
	return dbErr(code, params)
}

// putBack writes the files edits changed as they were (nil: removes them).
func (d *DBs) putBack(edits []confEdit, old [][]byte) error {
	var errs []error
	for i, ed := range edits {
		switch {
		case old[i] != nil:
			errs = append(errs, d.writeConfig(ed.path, old[i]))
		case ed.data != nil:
			if err := removeConfFile(d.Root, ed.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, confDBErr(err))
			}
		}
	}
	return errors.Join(errs...)
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

// mysqlRestore loads a dump as the given user, after taking a copy of the
// database as it is (unless req.SkipCopy). A MySQL dump is not loaded in a
// transaction: statements before a failing one stay done. When the dump
// fails, the database is put back from the copy.
func (d *DBs) mysqlRestore(ctx context.Context, log io.Writer, e dbEngine, req helperapi.DBRestoreRequest) error {
	if !strings.HasSuffix(req.File, ".sql") && !strings.HasSuffix(req.File, ".sql.gz") {
		return dbErr(helperapi.CodeDBQuery, map[string]any{"detail": "unsupported dump format"})
	}
	in, err := d.openDump(e.id, req.File)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	hosts, err := d.mysqlHosts(ctx, req.User)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return dbErr(helperapi.CodeDBUserNotFound, map[string]any{"name": req.User})
	}
	if err := d.mysqlRefusePrivileged(ctx, req.User, hosts); err != nil {
		return err
	}
	var copied, charset, collation string
	if !req.SkipCopy {
		// The database's own settings, as the dump may change them.
		rows, err := d.mysqlExec(ctx, nil, mysqlQuery("SELECT 'c', DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = %s;", myStr(req.Name)))
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r[0] == "c" && len(r) > 2 {
				charset, collation = r[1], r[2]
			}
		}
		if !mysqlCharset.MatchString(charset) || !mysqlCharset.MatchString(collation) {
			return dbErr(helperapi.CodeDBNotFound, map[string]any{"name": req.Name})
		}
		info, err := d.writeDump(e.id, req.Name, dbCopySuffix+".sql.gz", true, func(w io.Writer) error { return d.mysqlDumpTo(ctx, log, e, req.Name, w) })
		if err != nil {
			return copyFailed(req.Name, err)
		}
		copied = info.File
		_, _ = fmt.Fprintf(log, "A copy of %s as it was before the restore: %s\n", req.Name, copied)
	}
	err = d.mysqlLoad(ctx, log, e, req, in, req.File)
	if err == nil {
		return nil
	}
	he := asError(err)
	// A client that could not sign in or found no database ran nothing.
	if he.Code != helperapi.CodeDBQuery {
		return err
	}
	params := map[string]any{"database": req.Name, "detail": he.Params["detail"]}
	if copied == "" {
		_, _ = fmt.Fprintln(log, "The restore stopped partway: what the dump did before the failing statement stays.")
		return dbErr(helperapi.CodeDBRestorePartial, params)
	}
	params["copy"] = copied
	_, _ = fmt.Fprintf(log, "The restore stopped partway; putting %s back from %s.\n", req.Name, copied)
	if perr := d.mysqlPutBack(ctx, log, e, req, copied, charset, collation); perr != nil {
		_, _ = fmt.Fprintf(log, "Putting the database back failed: %v\nRestore %s to bring it back as it was.\n", perr, copied)
		params["detail"] = fmt.Sprint(he.Params["detail"]) + "; putting it back: " + errDetail(perr)
		return dbErr(helperapi.CodeDBRestorePartial, params)
	}
	_, _ = fmt.Fprintf(log, "%s is back as it was before the restore.\n", req.Name)
	return dbErr(helperapi.CodeDBRestoreRolledBack, params)
}

// mysqlCharset matches a character set or collation name the server
// reports.
var mysqlCharset = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// dbCopySuffix ends the name of the copy of a database a restore takes
// first (before the extension).
const dbCopySuffix = "-before-restore"

// copyFailed is the error of a restore whose copy of the database could
// not be made: the restore did not start. A server that is down or
// refuses the helper keeps its own error.
func copyFailed(database string, err error) error {
	switch asError(err).Code {
	case helperapi.CodeDBNotRunning, helperapi.CodeDBAccessDenied:
		return err
	}
	return dbErr(helperapi.CodeDBCopyFailed, map[string]any{"database": database, "detail": errDetail(err)})
}

// mysqlPutBack makes the database what the copy holds: it drops the
// database and makes it again with its settings (the grants on it, kept by
// name, stay), then loads the copy as the restoring user.
func (d *DBs) mysqlPutBack(ctx context.Context, log io.Writer, e dbEngine, req helperapi.DBRestoreRequest, copied, charset, collation string) error {
	in, err := d.openDump(e.id, copied)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	var s sqlScript
	s.add("DROP DATABASE %s;", myID(req.Name))
	s.add("CREATE DATABASE %s CHARACTER SET %s COLLATE %s;", myID(req.Name), charset, collation)
	if _, err := d.mysqlExec(ctx, log, &s); err != nil {
		return err
	}
	return d.mysqlLoad(ctx, log, e, req, in, copied)
}

// mysqlLoad runs a dump as the given user: the client reads the
// credentials from a root-only option file, never from its arguments.
// Definers the user may not set become the user (see mysqlDefiners).
func (d *DBs) mysqlLoad(ctx context.Context, log io.Writer, e dbEngine, req helperapi.DBRestoreRequest, in io.Reader, name string) error {
	// Option file values are quoted: # would start a comment.
	cnf, cleanup, err := d.tempFile("client-*.cnf", fmt.Appendf(nil, "[client]\nuser=\"%s\"\npassword=\"%s\"\n", req.User, req.Password))
	if err != nil {
		return err
	}
	defer cleanup()
	// The client runs as root (it reads the root-only option file), and the
	// dump is untrusted: --binary-mode turns off every client command but
	// charset and delimiter in piped input (no system, tee or source), and
	// --local-infile=0 keeps LOAD DATA LOCAL from reading files as root.
	args := []string{"--defaults-extra-file=" + cnf, "--protocol=socket", "--default-character-set=utf8mb4", "--binary-mode",
		"--local-infile=0", "--max-allowed-packet=1073741824", "--database=" + req.Name}
	_, _ = fmt.Fprintf(log, "$ mysql --user=%s --database=%s < %s\n", req.User, req.Name, name)
	script := mysqlDefiners(in)
	_, err = dbRun(ctx, execx.Cmd{Name: d.tool("mariadb", "mysql"), Args: args, Stdin: script, Output: log, Timeout: dumpTimeout, MaxOutput: 64 << 10})
	_ = script.Close()
	if n := script.rewritten(); n > 0 {
		_, _ = fmt.Fprintf(log, "%d DEFINER clauses of the dump named another account; the objects belong to %s instead.\n", n, req.User)
	}
	if err != nil {
		return mysqlError(err)
	}
	return nil
}

// mysqlDefiner matches a DEFINER clause where a dump's statement starts:
// after CREATE [OR REPLACE] [ALGORITHM=...] and the version comments
// mysqldump and mariadb-dump wrap them in (/*!50013 DEFINER=... */,
// /*!50003 CREATE*/ /*!50017 DEFINER=...*/).
var mysqlDefiner = func() *regexp.Regexp {
	part := "`(?:[^`]|``)*`|'(?:[^'\\\\]|\\\\.|'')*'|\"(?:[^\"\\\\]|\\\\.)*\"|[^\\s@*/;'\"`]+"
	return regexp.MustCompile(`(?i)^(\s*(?:(?:/\*M?!\d*|\*/|CREATE\b|OR\b|REPLACE\b|ALGORITHM\s*=\s*\w+)\s*)*)DEFINER\s*=\s*(?:` +
		part + `)(?:\s*@\s*(?:` + part + `))?`)
}()

// definerCopy is a dump passed on with its definers rewritten; Close stops
// the copy and waits for it.
type definerCopy struct {
	pipeCopy
	n *int
}

// rewritten is how many DEFINER clauses were rewritten; read it after
// Close.
func (c definerCopy) rewritten() int { return *c.n }

// mysqlDefiners passes a dump on with every DEFINER clause naming the
// restoring user (CURRENT_USER). The user cannot create objects for other
// accounts (that needs SUPER or SET_USER_ID), so a dump made elsewhere, or
// by root here (mysqldump writes the definer each object has, often
// root@localhost or the user at 127.0.0.1), would otherwise fail at its
// first view, trigger, routine or event, with the tables before it already
// replaced. Only the first clause of a line that starts a statement is
// rewritten, never the rest of the line (a trigger's body, data).
func mysqlDefiners(in io.Reader) definerCopy {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	n := new(int)
	go func() {
		defer close(done)
		br := bufio.NewReaderSize(in, 64<<10)
		start := true
		for {
			chunk, err := br.ReadSlice('\n')
			whole := !errors.Is(err, bufio.ErrBufferFull)
			if start && len(chunk) > 0 {
				if loc := mysqlDefiner.FindSubmatchIndex(chunk); loc != nil {
					line := make([]byte, 0, len(chunk))
					line = append(line, chunk[:loc[3]]...)
					line = append(line, "DEFINER=CURRENT_USER"...)
					chunk = append(line, chunk[loc[1]:]...)
					*n++
				}
			}
			if len(chunk) > 0 {
				if _, werr := pw.Write(chunk); werr != nil {
					return
				}
			}
			start = whole
			if err != nil && whole {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				_ = pw.CloseWithError(err)
				return
			}
		}
	}()
	return definerCopy{pipeCopy: pipeCopy{PipeReader: pr, done: done}, n: n}
}
