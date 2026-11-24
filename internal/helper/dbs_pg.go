// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// PostgreSQL (Debian's postgresql-common layout): one cluster per line,
// administered as the postgres role over peer authentication. Users are
// login roles; where they may connect from lives in a block of pg_hba.conf
// the panel manages, local connections being allowed by the system rules.

const (
	pgHbaBegin = "# BEGIN Cairn: remote hosts of database users. " + dbManagedNote
	pgHbaEnd   = "# END Cairn"
	pgAuth     = "scram-sha-256"
)

type pgClusterInfo struct {
	version, name string
	port          int
	online        bool
}

func (c pgClusterInfo) etc(file string) string {
	return path.Join("/etc/postgresql", c.version, c.name, file)
}

func pgBin(e dbEngine, tool string) string {
	return path.Join("/usr/lib/postgresql", e.line, "bin", tool)
}

// pgID quotes a PostgreSQL identifier.
func pgID(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// pgStr quotes a PostgreSQL string literal (standard_conforming_strings).
func pgStr(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// pgClusters lists the clusters of every line.
func (d *DBs) pgClusters(ctx context.Context) ([]pgClusterInfo, error) {
	res, err := dbRun(ctx, execx.Cmd{Name: "/usr/bin/pg_lsclusters", Args: []string{"-h"}, Timeout: 30 * time.Second})
	if err != nil {
		return nil, dbErr(helperapi.CodeDBQuery, map[string]any{"detail": errDetail(err)})
	}
	var out []pgClusterInfo
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		port, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		out = append(out, pgClusterInfo{version: f[0], name: f[1], port: port, online: strings.HasPrefix(f[3], "online")})
	}
	return out, nil
}

// pgCluster0 is the cluster of a line: "main", or the first one.
func (d *DBs) pgCluster0(ctx context.Context, e dbEngine) (pgClusterInfo, error) {
	all, err := d.pgClusters(ctx)
	if err != nil {
		return pgClusterInfo{}, err
	}
	var found []pgClusterInfo
	for _, c := range all {
		if c.version == e.line {
			found = append(found, c)
		}
	}
	if len(found) == 0 {
		return pgClusterInfo{}, dbErr(helperapi.CodeDBNoCluster, map[string]any{"engine": e.id})
	}
	for _, c := range found {
		if c.name == "main" {
			return c, nil
		}
	}
	return found[0], nil
}

// pgOnline is the line's cluster, which must be running.
func (d *DBs) pgOnline(ctx context.Context, e dbEngine) (pgClusterInfo, error) {
	c, err := d.pgCluster0(ctx, e)
	if err == nil && !c.online {
		err = dbErr(helperapi.CodeDBNotRunning, map[string]any{"engine": e.id})
	}
	return c, err
}

// pgAdminEnv pins the search path of every session the helper opens as the
// postgres role. A database user may create objects in a public schema (in
// any database it owns, and in every database of a cluster created by 14 or
// earlier), and an unqualified name, or an operator without an exact match
// in pg_catalog, would otherwise resolve to them and run with superuser
// rights. pg_catalog comes first and pg_temp, only this session's own,
// last; options sent at connection time override any search_path set on the
// database or the role.
var pgAdminEnv = []string{"PGOPTIONS=-c search_path=pg_catalog,pg_temp"}

// psql runs a script as the postgres role in a database.
func (d *DBs) psql(ctx context.Context, log io.Writer, e dbEngine, c pgClusterInfo, db string, s *sqlScript) ([][]string, error) {
	if log != nil {
		_, _ = fmt.Fprintf(log, "$ psql -d %s <<SQL\n%sSQL\n", db, s.show.String())
	}
	args := []string{"-u", "postgres", "--", pgBin(e, "psql"), "-X", "-q", "-A", "-t", "-F", "\t", "-v", "ON_ERROR_STOP=1",
		"-p", strconv.Itoa(c.port), "-d", db, "-f", "-"}
	res, err := dbRun(ctx, execx.Cmd{Name: "runuser", Args: args, Stdin: strings.NewReader(s.run.String()), Env: pgAdminEnv, Timeout: dbTimeout, MaxOutput: 8 << 20})
	if err != nil {
		return nil, pgError(err, e)
	}
	return parseRows(res.Stdout, false), nil
}

func pgError(err error, e dbEngine) error {
	detail := errDetail(err)
	switch {
	case strings.Contains(detail, "No such file or directory") && strings.Contains(detail, "connection to server"),
		strings.Contains(detail, "Connection refused"):
		return dbErr(helperapi.CodeDBNotRunning, map[string]any{"engine": e.id})
	case strings.Contains(detail, "password authentication failed"), strings.Contains(detail, "Peer authentication failed"):
		return dbErr(helperapi.CodeDBAccessDenied, map[string]any{"engine": e.id, "detail": detail})
	}
	return dbErr(helperapi.CodeDBQuery, map[string]any{"detail": detail})
}

func pgQuery(format string, args ...any) *sqlScript {
	var s sqlScript
	s.add(format, args...)
	return &s
}

func (d *DBs) pgStatus(ctx context.Context, e dbEngine, info *helperapi.DBEngineInfo) error {
	c, err := d.pgCluster0(ctx, e)
	if err != nil {
		if asError(err).Code == helperapi.CodeDBNoCluster {
			info.NoCluster = true
			return nil
		}
		return err
	}
	info.Port = c.port
	if !c.online {
		return nil
	}
	rows, err := d.psql(ctx, nil, e, c, "postgres", pgQuery(`SELECT 'v', current_setting('server_version');
SELECT 'l', current_setting('listen_addresses');
SELECT 'd', d.datname, pg_database_size(d.oid), pg_get_userbyid(d.datdba) FROM pg_database d WHERE NOT d.datistemplate ORDER BY 2;
SELECT 'u', rolname FROM pg_roles WHERE rolcanlogin ORDER BY 2;`))
	if err != nil {
		if asError(err).Code == helperapi.CodeDBNotRunning {
			return nil
		}
		return err
	}
	info.Running = true
	// Status shows what can be read; a refused file shows no hosts.
	_, _, hba, _ := d.pgHba(c)
	for _, r := range rows {
		switch {
		case r[0] == "v" && len(r) > 1:
			info.Version = r[1]
		case r[0] == "l" && len(r) > 1:
			info.Remote = slices.ContainsFunc(strings.Split(r[1], ","), func(a string) bool { return a == "*" || !isLoopback(a) })
		case r[0] == "d" && len(r) > 3:
			if helperapi.ValidDBName(e.id, r[1]) {
				size, _ := strconv.ParseInt(r[2], 10, 64)
				info.Databases = append(info.Databases, helperapi.DBInfo{Name: r[1], SizeBytes: size, Owner: r[3]})
			}
		case r[0] == "u" && len(r) > 1:
			if helperapi.ValidDBUser(e.id, r[1]) {
				info.Users = append(info.Users, helperapi.DBUserInfo{Name: r[1], Hosts: append([]string{"localhost"}, hba[r[1]]...)})
			}
		}
	}
	return nil
}

// pgHba reads pg_hba.conf around the managed block, and the hosts of
// every user in it; a missing file is empty. The cluster's directory
// belongs to postgres: see readConfFile.
func (d *DBs) pgHba(c pgClusterInfo) (string, string, map[string][]string, error) {
	data, err := d.readConfig(c.etc("pg_hba.conf"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		before, after, hosts := parseHba("")
		return before, after, hosts, err
	}
	before, after, hosts := parseHba(string(data))
	return before, after, hosts, nil
}

func parseHba(content string) (string, string, map[string][]string) {
	hosts := map[string][]string{}
	start := strings.Index(content, pgHbaBegin)
	if start < 0 {
		return content, "", hosts
	}
	rest := content[start+len(pgHbaBegin):]
	block, after, ok := strings.Cut(rest, pgHbaEnd)
	if !ok {
		return content, "", hosts
	}
	after = strings.TrimPrefix(after, "\n")
	for _, line := range strings.Split(block, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "host" {
			continue
		}
		h := f[3]
		switch h {
		case "::/0":
			continue
		case "0.0.0.0/0":
			h = "%"
		default:
			h = strings.TrimSuffix(h, "/32")
		}
		if !slices.Contains(hosts[f[2]], h) {
			hosts[f[2]] = append(hosts[f[2]], h)
		}
	}
	return content[:start], after, hosts
}

func renderHba(before, after string, hosts map[string][]string) string {
	var users []string
	for u, hs := range hosts {
		if len(hs) > 0 {
			users = append(users, u)
		}
	}
	sort.Strings(users)
	var b strings.Builder
	b.WriteString(before)
	if len(users) > 0 {
		if before != "" && !strings.HasSuffix(before, "\n") {
			b.WriteString("\n")
		}
		b.WriteString(pgHbaBegin + "\n")
		for _, u := range users {
			for _, h := range hosts[u] {
				var nets []string
				switch {
				case h == "%":
					nets = []string{"0.0.0.0/0", "::/0"}
				case strings.Contains(h, "/"):
					nets = []string{h}
				default:
					nets = []string{h + "/32"}
				}
				for _, n := range nets {
					_, _ = fmt.Fprintf(&b, "host\tall\t%s\t%s\t%s\n", u, n, pgAuth)
				}
			}
		}
		b.WriteString(pgHbaEnd + "\n")
	}
	b.WriteString(after)
	return b.String()
}

// pgSetHosts makes the managed block list hosts for user (nil: none)
// and reloads the server when it changed.
func (d *DBs) pgSetHosts(ctx context.Context, log io.Writer, e dbEngine, c pgClusterInfo, user string, hosts []string) error {
	before, after, all, err := d.pgHba(c)
	if err != nil {
		return err
	}
	var remote []string
	for _, h := range hosts {
		if h != "localhost" && !slices.Contains(remote, h) {
			if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
				continue
			}
			remote = append(remote, h)
		}
	}
	if slices.Equal(all[user], remote) {
		return nil
	}
	all[user] = remote
	if err := d.writeConfig(c.etc("pg_hba.conf"), []byte(renderHba(before, after, all))); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(log, "updated %s\n", c.etc("pg_hba.conf"))
	_, err = d.psql(ctx, log, e, c, "postgres", pgQuery("SELECT pg_reload_conf();"))
	return err
}

// pgDatabases returns the databases (not templates) with their owners.
func (d *DBs) pgDatabases(ctx context.Context, e dbEngine, c pgClusterInfo) (map[string]string, error) {
	rows, err := d.psql(ctx, nil, e, c, "postgres", pgQuery("SELECT 'd', datname, pg_get_userbyid(datdba) FROM pg_database WHERE NOT datistemplate AND datallowconn;"))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rows {
		if r[0] == "d" && len(r) > 2 {
			out[r[1]] = r[2]
		}
	}
	return out, nil
}

func (d *DBs) pgRoleExists(ctx context.Context, e dbEngine, c pgClusterInfo, name string) (bool, error) {
	rows, err := d.psql(ctx, nil, e, c, "postgres", pgQuery("SELECT 'r' FROM pg_roles WHERE rolname = %s;", pgStr(name)))
	return len(rows) > 0, err
}

func (d *DBs) pgCreate(ctx context.Context, log io.Writer, e dbEngine, req helperapi.DBCreateRequest) error {
	c, err := d.pgOnline(ctx, e)
	if err != nil {
		return err
	}
	dbs, err := d.pgDatabases(ctx, e, c)
	if err != nil {
		return err
	}
	if _, ok := dbs[req.Name]; ok {
		return dbErr(helperapi.CodeDBExists, map[string]any{"name": req.Name})
	}
	if req.Owner != "" {
		if ok, err := d.pgRoleExists(ctx, e, c, req.Owner); err != nil {
			return err
		} else if !ok {
			return dbErr(helperapi.CodeDBUserNotFound, map[string]any{"name": req.Owner})
		}
	}
	var s sqlScript
	owner := ""
	if req.Owner != "" {
		owner = " OWNER " + pgID(req.Owner)
	}
	s.add("CREATE DATABASE %s%s TEMPLATE template0 ENCODING 'UTF8';", pgID(req.Name), owner)
	// Only the users granted access may connect.
	s.add("REVOKE CONNECT, TEMPORARY ON DATABASE %s FROM PUBLIC;", pgID(req.Name))
	if _, err := d.psql(ctx, log, e, c, "postgres", &s); err != nil {
		return err
	}
	var in sqlScript
	in.add("REVOKE CREATE ON SCHEMA public FROM PUBLIC;")
	if req.Owner != "" {
		in.add("ALTER SCHEMA public OWNER TO %s;", pgID(req.Owner))
	}
	_, err = d.psql(ctx, log, e, c, req.Name, &in)
	return err
}

func (d *DBs) pgDrop(ctx context.Context, log io.Writer, e dbEngine, name string) error {
	c, err := d.pgOnline(ctx, e)
	if err != nil {
		return err
	}
	rows, err := d.psql(ctx, nil, e, c, "postgres", pgQuery("SELECT 'd' FROM pg_database WHERE datname = %s;", pgStr(name)))
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return dbErr(helperapi.CodeDBNotFound, map[string]any{"name": name})
	}
	_, err = d.psql(ctx, log, e, c, "postgres", pgQuery("DROP DATABASE %s WITH (FORCE);", pgID(name)))
	return err
}

// pgGrantScript sets a user's access to the public schema of a database:
// level "" takes it away.
func pgGrantScript(s *sqlScript, user, db, owner, level string) {
	u, o, q := pgID(user), pgID(owner), pgID(db)
	s.add("REVOKE ALL ON DATABASE %s FROM %s;", q, u)
	for _, kind := range []string{"TABLES", "SEQUENCES", "FUNCTIONS"} {
		s.add("REVOKE ALL ON ALL %s IN SCHEMA public FROM %s;", kind, u)
		s.add("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public REVOKE ALL ON %s FROM %s;", o, kind, u)
	}
	s.add("REVOKE ALL ON SCHEMA public FROM %s;", u)
	switch level {
	case helperapi.GrantAll:
		s.add("GRANT CONNECT, TEMPORARY, CREATE ON DATABASE %s TO %s;", q, u)
		s.add("GRANT USAGE, CREATE ON SCHEMA public TO %s;", u)
		for _, kind := range []string{"TABLES", "SEQUENCES", "FUNCTIONS"} {
			s.add("GRANT ALL ON ALL %s IN SCHEMA public TO %s;", kind, u)
			s.add("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT ALL ON %s TO %s;", o, kind, u)
		}
	case helperapi.GrantReadOnly:
		s.add("GRANT CONNECT ON DATABASE %s TO %s;", q, u)
		s.add("GRANT USAGE ON SCHEMA public TO %s;", u)
		for _, kind := range []string{"TABLES", "SEQUENCES"} {
			s.add("GRANT SELECT ON ALL %s IN SCHEMA public TO %s;", kind, u)
			s.add("ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA public GRANT SELECT ON %s TO %s;", o, kind, u)
		}
	}
}

func (d *DBs) pgUserSet(ctx context.Context, log io.Writer, e dbEngine, req helperapi.DBUserRequest) error {
	c, err := d.pgOnline(ctx, e)
	if err != nil {
		return err
	}
	exists, err := d.pgRoleExists(ctx, e, c, req.Name)
	if err != nil {
		return err
	}
	if req.New && exists {
		return dbErr(helperapi.CodeDBUserExists, map[string]any{"name": req.Name})
	}
	// Nil hosts keep the user's pg_hba.conf entries (an existing user only).
	if req.Hosts == nil && !exists {
		return dbErr(helperapi.CodeDBUserNotFound, map[string]any{"name": req.Name})
	}
	dbs, err := d.pgDatabases(ctx, e, c)
	if err != nil {
		return err
	}
	wanted := map[string]string{}
	for _, g := range req.Grants {
		if _, ok := dbs[g.Database]; !ok {
			return dbErr(helperapi.CodeDBNotFound, map[string]any{"name": g.Database})
		}
		wanted[g.Database] = g.Level
	}
	var s sqlScript
	s.add("SET password_encryption = 'scram-sha-256';")
	switch {
	case !exists && req.Password == "":
		return dbErr(helperapi.CodeDBPasswordNeed, map[string]any{"name": req.Name})
	case !exists:
		s.add("CREATE ROLE %s LOGIN PASSWORD %s;", pgID(req.Name), secret(pgStr(req.Password)))
	case req.Password != "":
		s.add("ALTER ROLE %s WITH LOGIN PASSWORD %s;", pgID(req.Name), secret(pgStr(req.Password)))
	}
	// Nil grants leave the privileges as they are: a password or host
	// change never revokes anything.
	if req.Grants != nil {
		s.add("SELECT 'g', d.datname FROM pg_database d WHERE NOT d.datistemplate AND EXISTS (SELECT 1 FROM aclexplode(d.datacl) a WHERE a.grantee OPERATOR(pg_catalog.=) %s::pg_catalog.regrole::pg_catalog.oid);", pgStr(req.Name))
	}
	rows, err := d.psql(ctx, log, e, c, "postgres", &s)
	if err != nil {
		return err
	}
	if req.Grants != nil {
		if err := d.pgSetGrants(ctx, log, e, c, req.Name, dbs, wanted, rows); err != nil {
			return err
		}
	}
	if req.Hosts == nil {
		return nil
	}
	return d.pgSetHosts(ctx, log, e, c, req.Name, req.Hosts)
}

// pgSetGrants gives user exactly the wanted access (by database), revoking
// it from the databases where it has some (rows of 'g' and a name).
func (d *DBs) pgSetGrants(ctx context.Context, log io.Writer, e dbEngine, c pgClusterInfo, user string, dbs, wanted map[string]string, rows [][]string) error {
	touch := map[string]bool{}
	for db := range wanted {
		touch[db] = true
	}
	for _, r := range rows {
		if r[0] == "g" && len(r) > 1 {
			touch[r[1]] = true
		}
	}
	names := make([]string, 0, len(touch))
	for db := range touch {
		// An owner has every privilege anyway.
		if owner, ok := dbs[db]; ok && owner != user {
			names = append(names, db)
		}
	}
	sort.Strings(names)
	for _, db := range names {
		var g sqlScript
		pgGrantScript(&g, user, db, dbs[db], wanted[db])
		if _, err := d.psql(ctx, log, e, c, db, &g); err != nil {
			return err
		}
	}
	return nil
}

func (d *DBs) pgUserDrop(ctx context.Context, log io.Writer, e dbEngine, name string) error {
	c, err := d.pgOnline(ctx, e)
	if err != nil {
		return err
	}
	if ok, err := d.pgRoleExists(ctx, e, c, name); err != nil {
		return err
	} else if !ok {
		return dbErr(helperapi.CodeDBUserNotFound, map[string]any{"name": name})
	}
	dbs, err := d.pgDatabases(ctx, e, c)
	if err != nil {
		return err
	}
	var owned []string
	for db, owner := range dbs {
		if owner == name {
			owned = append(owned, db)
		}
	}
	if len(owned) > 0 {
		sort.Strings(owned)
		return dbErr(helperapi.CodeDBUserOwns, map[string]any{"name": name, "databases": owned})
	}
	names := make([]string, 0, len(dbs))
	for db := range dbs {
		names = append(names, db)
	}
	sort.Strings(names)
	for _, db := range names {
		// What the user created goes to the database's owner; then its
		// privileges go.
		if _, err := d.psql(ctx, log, e, c, db, pgQuery("REASSIGN OWNED BY %s TO %s;\nDROP OWNED BY %s;", pgID(name), pgID(dbs[db]), pgID(name))); err != nil {
			return err
		}
	}
	if _, err := d.psql(ctx, log, e, c, "postgres", pgQuery("DROP ROLE %s;", pgID(name))); err != nil {
		return err
	}
	return d.pgSetHosts(ctx, log, e, c, name, nil)
}

func (d *DBs) pgRemote(ctx context.Context, log io.Writer, e dbEngine, enabled bool) error {
	c, err := d.pgCluster0(ctx, e)
	if err != nil {
		return err
	}
	listen := "localhost"
	if enabled {
		listen = "*"
	}
	conf := fmt.Sprintf("# %s\nlisten_addresses = '%s'\n", dbManagedNote, listen)
	dir := c.etc("conf.d")
	if err := d.pgConfDir(dir); err != nil {
		return err
	}
	return d.reconfigure(ctx, log, e, path.Join(dir, "99-cairn.conf"), []byte(conf),
		func() error { return d.pgCtl(ctx, log, c, helperapi.ServiceRestart) })
}

// pgConfDir makes sure the cluster's conf.d is a directory of its own
// (never a link to one elsewhere), making it for postgres when missing.
func (d *DBs) pgConfDir(dir string) error {
	f, err := openConfDir(d.Root, dir, false)
	if err == nil {
		return f.Close()
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return confDBErr(err)
	}
	if f, err = openConfDir(d.Root, dir, true); err != nil {
		return confDBErr(err)
	}
	defer func() { _ = f.Close() }()
	if uid, gid, err := lookupUser("postgres"); err == nil {
		return f.Chown(uid, gid)
	}
	return nil
}

// pgCtl starts, stops or restarts a cluster (through systemd when it
// runs; pg_ctlcluster decides).
func (d *DBs) pgCtl(ctx context.Context, log io.Writer, c pgClusterInfo, action string) error {
	if err := runCommand(ctx, log, "pg_ctlcluster", c.version, c.name, action); err != nil {
		return dbErr(helperapi.CodeDBRestartFailed, map[string]any{"engine": "postgresql-" + c.version, "detail": errDetail(err)})
	}
	return nil
}

// pgCluster creates the main cluster of a line that has none (installing
// a second line leaves it without one) and starts it.
func (d *DBs) pgCluster(ctx context.Context, _ helperapi.Caller, req helperapi.DBEngineRef, log io.Writer) (any, error) {
	e, err := d.engine(ctx, req.Engine)
	if err != nil {
		return nil, err
	}
	if e.kind != helperapi.DBPostgreSQL {
		return nil, dbErr(helperapi.CodeDBEngineMissing, map[string]any{"engine": req.Engine})
	}
	if _, err := d.pgCluster0(ctx, e); err == nil {
		return nil, dbErr(helperapi.CodeDBExists, map[string]any{"name": e.line + "/main"})
	} else if asError(err).Code != helperapi.CodeDBNoCluster {
		return nil, err
	}
	if err := runCommand(ctx, log, "pg_createcluster", e.line, "main", "--start"); err != nil {
		return nil, dbErr(helperapi.CodeDBQuery, map[string]any{"detail": errDetail(err)})
	}
	return nil, nil
}

func (d *DBs) pgDump(ctx context.Context, log io.Writer, e dbEngine, name string) (helperapi.DumpInfo, error) {
	c, err := d.pgOnline(ctx, e)
	if err != nil {
		return helperapi.DumpInfo{}, err
	}
	rows, err := d.psql(ctx, nil, e, c, "postgres", pgQuery("SELECT 'd' FROM pg_database WHERE datname = %s;", pgStr(name)))
	if err != nil {
		return helperapi.DumpInfo{}, err
	}
	if len(rows) == 0 {
		return helperapi.DumpInfo{}, dbErr(helperapi.CodeDBNotFound, map[string]any{"name": name})
	}
	// The file itself is the dump's standard output, so pg_dump can seek
	// and record where each table's data starts.
	return d.writeDump(e.id, name, ".dump", false, func(w io.Writer) error { return d.pgDumpTo(ctx, log, e, c, name, w) })
}

// pgDumpTo writes a database's dump (custom format) to w. Owners and
// privileges stay out: the dump restores as any user.
func (d *DBs) pgDumpTo(ctx context.Context, log io.Writer, e dbEngine, c pgClusterInfo, name string, w io.Writer) error {
	args := []string{"-u", "postgres", "--", pgBin(e, "pg_dump"), "-Fc", "--no-owner", "--no-privileges", "-p", strconv.Itoa(c.port), "-d", name}
	_, _ = fmt.Fprintf(log, "$ pg_dump -Fc --no-owner --no-privileges -d %s\n", name)
	if _, err := dbRun(ctx, execx.Cmd{Name: "runuser", Args: args, Stdout: w, Output: log, Env: pgAdminEnv, Timeout: dumpTimeout, MaxOutput: 64 << 10}); err != nil {
		return pgError(err, e)
	}
	return nil
}

// pgRestore loads a dump as the given user over TCP with its password, so
// the dump has that user's rights only. The client itself is unprivileged:
// psql runs a script's meta-commands (\!, \o |command, \copy ... from
// program) with its own rights. With systemd it runs in a unit of its own,
// as a user allocated for this run (nothing it starts outlives the unit,
// and no other run shares its user), with the password in a credential
// file only it can read; a psql that knows \restrict also refuses every
// meta-command of the dump.
func (d *DBs) pgRestore(ctx context.Context, log io.Writer, e dbEngine, req helperapi.DBRestoreRequest) error {
	c, err := d.pgOnline(ctx, e)
	if err != nil {
		return err
	}
	unit := systemdRunning()
	var runAs *execx.RunAs
	if !unit {
		if runAs, err = dumpReaderIdentity(); err != nil {
			return err
		}
	}
	in, err := d.openDump(e.id, req.File)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	conn := []string{"-h", "127.0.0.1", "-p", strconv.Itoa(c.port), "-U", req.User, "-d", req.Name}
	var cmd execx.Cmd
	var input io.Reader = in
	if strings.HasSuffix(req.File, ".dump") {
		cmd = execx.Cmd{Name: pgBin(e, "pg_restore"), Args: append([]string{"--no-owner", "--no-privileges", "--clean", "--if-exists", "--exit-on-error"}, conn...)}
	} else {
		cmd = execx.Cmd{Name: pgBin(e, "psql"), Args: append([]string{"-X", "-q", "-v", "ON_ERROR_STOP=1", "-f", "-"}, conn...)}
		if v, ok := d.pgRestrictable(ctx, e); ok {
			key, err := randomAlnum(32)
			if err != nil {
				return err
			}
			script := pgUnrestricted(in)
			defer func() { _ = script.Close() }()
			input = io.MultiReader(strings.NewReader(`\restrict `+key+"\n"), script)
		} else {
			_, _ = fmt.Fprintf(log, "Note: psql %s cannot refuse the dump's meta-commands (\\restrict needs 13.22, 14.19, 15.14, 16.10, 17.6 or later).\n", v)
		}
	}
	cmd.Stdin, cmd.Output, cmd.Timeout, cmd.MaxOutput = input, log, dumpTimeout, 64<<10
	cmd.Env = []string{"PGCONNECT_TIMEOUT=10"}
	_, _ = fmt.Fprintf(log, "$ %s -U %s -d %s < %s\n", path.Base(cmd.Name), req.User, req.Name, req.File)
	if unit {
		_, err = runInUnit(ctx, unitRun{Cmd: cmd, DynamicUser: true,
			Credentials:   map[string][]byte{"pgpass": []byte(pgpassLine(c.port, req.User, req.Password))},
			CredentialEnv: map[string]string{"PGPASSFILE": "pgpass"}})
	} else {
		cmd.RunAs = runAs
		cmd.Env = append(cmd.Env, "PGPASSWORD="+req.Password)
		_, err = dbRun(ctx, cmd)
	}
	if err != nil {
		return pgError(err, e)
	}
	return nil
}

// pgpassLine is a password file's line for the restore's connection; ':'
// and '\' are escaped as libpq reads them.
func pgpassLine(port int, user, password string) string {
	esc := strings.NewReplacer(`\`, `\\`, ":", `\:`).Replace
	return "127.0.0.1:" + strconv.Itoa(port) + ":*:" + esc(user) + ":" + esc(password) + "\n"
}

var psqlVersion = regexp.MustCompile(`\(PostgreSQL\) ([0-9]+)(?:\.([0-9]+))?`)

// pgRestrictable reports whether the line's psql knows \restrict (the
// releases of August 2025: 17.6, 16.10, 15.14, 14.19, 13.22, and 18), and
// its version for the log.
func (d *DBs) pgRestrictable(ctx context.Context, e dbEngine) (string, bool) {
	res, err := dbRun(ctx, execx.Cmd{Name: pgBin(e, "psql"), Args: []string{"--version"}, Timeout: 30 * time.Second, MaxOutput: 4096})
	if err != nil {
		return "?", false
	}
	m := psqlVersion.FindStringSubmatch(string(res.Stdout))
	if m == nil {
		return "?", false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	v := m[1]
	if m[2] != "" {
		v += "." + m[2]
	}
	first := map[int]int{13: 22, 14: 19, 15: 14, 16: 10, 17: 6}
	if major >= 18 {
		return v, true
	}
	need, known := first[major]
	return v, known && minor >= need
}

// randomAlnum returns n random letters and digits.
func randomAlnum(n int) (string, error) {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b), nil
}

var (
	// The lines pg_dump (17.6 and later) wraps a plain dump in. Under the
	// helper's own \restrict they would stop the restore: they go.
	pgRestrictLine = regexp.MustCompile(`^\\(?:un)?restrict [A-Za-z0-9]+\r?\n?$`)
	pgCopyStart    = regexp.MustCompile(`^COPY .* FROM stdin;\r?\n$`)
)

// pipeCopy is the reading end of a copy running in a goroutine; Close
// stops the copy and waits for it, so that its source can be closed.
type pipeCopy struct {
	*io.PipeReader
	done chan struct{}
}

func (p pipeCopy) Close() error {
	err := p.PipeReader.Close()
	<-p.done
	return err
}

// pgUnrestricted passes a plain dump on without its own \restrict and
// \unrestrict lines (outside COPY data, which is passed as it is).
// Closing it stops the copy.
func pgUnrestricted(in io.Reader) io.ReadCloser {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		br := bufio.NewReaderSize(in, 64<<10)
		start, inCopy := true, false
		for {
			chunk, err := br.ReadSlice('\n')
			whole := !errors.Is(err, bufio.ErrBufferFull)
			drop := false
			if start && len(chunk) > 0 {
				switch {
				case inCopy:
					inCopy = strings.TrimRight(string(chunk), "\r\n") != `\.`
				case whole && pgCopyStart.Match(chunk):
					inCopy = true
				case whole && pgRestrictLine.Match(chunk):
					drop = true
				}
			}
			if !drop && len(chunk) > 0 {
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
	return pipeCopy{PipeReader: pr, done: done}
}
