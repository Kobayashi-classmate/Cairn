// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// Databases: the system's MariaDB or MySQL, every installed PostgreSQL
// line (one cluster each) and Redis or Valkey. The helper administers
// them as their superuser (root over the socket, the postgres role over
// peer authentication), but only ever with statements it builds itself
// from validated names; a dump being restored runs as a database user
// instead, so it cannot do more than that user.

// dbRun runs a database tool. Tests replace it; they must never start a
// real client.
var dbRun = func(ctx context.Context, c execx.Cmd) (execx.Result, error) { return execx.Run(ctx, c) }

// dumpReader is the system account a PostgreSQL client runs as while it
// reads a dump being restored on a system without systemd (containers);
// with systemd it runs in a unit of its own with a user allocated for the
// run (see pgRestore). A dump is untrusted input, and a client acts on
// parts of it with its own rights (psql's \!, \o |command, \copy ... from
// program, \o file): run as root, that would be root. The client logs in
// over TCP with the database user's password, so it needs no account of
// its own, and it reads the dump from standard input, opened by the
// helper, so the account needs no access to the file either.
const dumpReader = "nobody"

// dumpReaderIdentity returns the identity of dumpReader.
func dumpReaderIdentity() (*execx.RunAs, error) {
	uid, gid, err := lookupUser(dumpReader)
	if err != nil {
		return nil, fmt.Errorf("the %s account: %w", dumpReader, err)
	}
	if uid <= 0 || gid <= 0 {
		return nil, fmt.Errorf("the %s account is privileged (%d:%d)", dumpReader, uid, gid)
	}
	return &execx.RunAs{Uid: uint32(uid), Gid: uint32(gid)}, nil //nolint:gosec // G115: positive 31-bit ids (lookupUser, parseID).
}

const (
	dbTimeout     = 2 * time.Minute
	dumpTimeout   = 6 * time.Hour
	dbBackupsDir  = "backups/db" // under the panel's data directory
	dbHelperTmp   = "/var/lib/cairn-helper/tmp"
	dbManagedNote = "Managed by Cairn. Change it in the panel instead of editing this file."
)

// DBs manages the databases of this system.
type DBs struct {
	Software *Software
	// Unsupported, when set, is why databases are not available here.
	Unsupported string
	// OnlySoftware, when set, limits the engines to these catalog entries
	// (EL: MariaDB and Redis/Valkey for now).
	OnlySoftware []string
	// Root prefixes every system path ("" means /; tests).
	Root string
	// DataDir is the panel's data directory; dumps live in its
	// backups/db/<engine>/, owned by PanelUser.
	DataDir   string
	PanelUser string
	// Firewall opens an engine's port while remote access is on (nil:
	// leave the firewall alone).
	Firewall *Firewall
}

// RegisterDBs adds the database actions.
func RegisterDBs(r *Registry, o *Ops, d *DBs) {
	lock := func(engine string) []string { return []string{"db-" + engine} }
	Register(r, helperapi.ActionDBStatus, func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.DBStatus, error) {
		return d.Status(ctx), nil
	})
	RegisterOp(r, o, helperapi.ActionDBCreate, OpSpec[helperapi.DBCreateRequest]{
		Locks: func(q helperapi.DBCreateRequest) []string { return lock(q.Engine) }, Run: d.create})
	RegisterOp(r, o, helperapi.ActionDBDrop, OpSpec[helperapi.DBRef]{
		Locks: func(q helperapi.DBRef) []string { return lock(q.Engine) }, Run: d.drop})
	RegisterOp(r, o, helperapi.ActionDBUserSet, OpSpec[helperapi.DBUserRequest]{
		Locks: func(q helperapi.DBUserRequest) []string { return lock(q.Engine) }, Run: d.userSet})
	RegisterOp(r, o, helperapi.ActionDBUserDrop, OpSpec[helperapi.DBRef]{
		Locks: func(q helperapi.DBRef) []string { return lock(q.Engine) }, Run: d.userDrop})
	RegisterOp(r, o, helperapi.ActionDBRemote, OpSpec[helperapi.DBRemoteRequest]{
		Locks: func(q helperapi.DBRemoteRequest) []string { return lock(q.Engine) }, Run: d.remote})
	RegisterOp(r, o, helperapi.ActionDBService, OpSpec[helperapi.DBServiceRequest]{
		Locks: func(q helperapi.DBServiceRequest) []string { return lock(q.Engine) }, Run: d.service})
	RegisterOp(r, o, helperapi.ActionDBPGCluster, OpSpec[helperapi.DBEngineRef]{
		Locks: func(q helperapi.DBEngineRef) []string { return lock(q.Engine) }, Run: d.pgCluster})
	// Dumps only read: they need no engine lock, and stopping one midway
	// leaves nothing behind.
	RegisterOp(r, o, helperapi.ActionDBDump, OpSpec[helperapi.DBDumpRequest]{
		Locks: func(q helperapi.DBDumpRequest) []string { return []string{"db-dump-" + q.Engine} }, Run: d.dump,
		CancelWhileRunning: true})
	RegisterOp(r, o, helperapi.ActionDBRestore, OpSpec[helperapi.DBRestoreRequest]{
		Locks: func(q helperapi.DBRestoreRequest) []string { return lock(q.Engine) }, Run: d.restore})
	RegisterOp(r, o, helperapi.ActionRedisConfig, OpSpec[helperapi.RedisConfigRequest]{
		Locks: func(helperapi.RedisConfigRequest) []string { return lock(helperapi.DBRedis) }, Run: d.redisConfig})
	Register(r, helperapi.ActionRedisScan, d.redisScan)
	Register(r, helperapi.ActionRedisGet, d.redisGet)
	Register(r, helperapi.ActionRedisDelete, d.redisDelete)
	Register(r, helperapi.ActionRedisSetTTL, d.redisTTL)
}

func dbErr(code string, params map[string]any) error {
	return &helperapi.Error{Code: code, Params: params}
}

func (d *DBs) path(p string) string { return filepath.Join(d.Root, p) }

// dbEngine is an installed database engine.
type dbEngine struct {
	id, kind, software, line, service, version string
}

// engines lists the installed database engines, MySQL first, PostgreSQL
// lines newest first.
func (d *DBs) engines(ctx context.Context) ([]dbEngine, error) {
	st, err := d.Software.Status(ctx)
	if err != nil {
		return nil, err
	}
	var out []dbEngine
	add := func(id, kind, sw string, newestFirst bool) {
		if d.OnlySoftware != nil && !slices.Contains(d.OnlySoftware, sw) {
			return
		}
		lines := make([]string, 0, len(st.Installed[sw]))
		for l := range st.Installed[sw] {
			lines = append(lines, l)
		}
		sort.Slice(lines, func(i, j int) bool { return versionLess(lines[j], lines[i]) == newestFirst })
		for _, l := range lines {
			e := dbEngine{id: id, kind: kind, software: sw, line: l, service: d.service0(sw, l)}
			if pkgs := st.Installed[sw][l]; len(pkgs) > 0 {
				e.version = pkgs[0].Version
			}
			if kind == helperapi.DBPostgreSQL {
				e.id = "postgresql-" + l
			}
			out = append(out, e)
			if kind != helperapi.DBPostgreSQL {
				return // one MySQL and one Redis per system
			}
		}
	}
	// One engine of each kind, the first installed in catalog order, except
	// PostgreSQL, whose lines each have a cluster of their own.
	for _, k := range []struct{ kind, capability string }{
		{helperapi.DBMySQL, catalog.CapMySQL}, {helperapi.DBPostgreSQL, catalog.CapPostgreSQL}, {helperapi.DBRedis, catalog.CapRedis},
	} {
		for _, sw := range d.Software.Catalog.Providing(k.capability) {
			if k.kind == helperapi.DBPostgreSQL {
				add("", k.kind, sw.ID, true)
			} else if !slices.ContainsFunc(out, func(e dbEngine) bool { return e.kind == k.kind }) {
				add(k.kind, k.kind, sw.ID, true)
			}
		}
	}
	return out, nil
}

// versionLess compares dotted numeric versions ("10.11" < "11.8").
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x < y
		}
	}
	return len(as) < len(bs)
}

// service0 is the first service of an installed line.
func (d *DBs) service0(sw, line string) string {
	s, ok := d.Software.Catalog.Find(sw)
	if !ok {
		return ""
	}
	if o, ok := d.Software.currentOffer(s, line); ok && len(o.Services) > 0 {
		return o.Services[0]
	}
	return ""
}

// engine resolves an engine ID of a request.
func (d *DBs) engine(ctx context.Context, id string) (dbEngine, error) {
	if d.Unsupported != "" {
		return dbEngine{}, dbErr(helperapi.CodeDBUnsupported, map[string]any{"reason": d.Unsupported})
	}
	all, err := d.engines(ctx)
	if err != nil {
		return dbEngine{}, err
	}
	for _, e := range all {
		if e.id == id {
			return e, nil
		}
	}
	return dbEngine{}, dbErr(helperapi.CodeDBEngineMissing, map[string]any{"engine": id})
}

// Status reports every installed engine with its databases and users.
func (d *DBs) Status(ctx context.Context) helperapi.DBStatus {
	s := helperapi.DBStatus{Supported: d.Unsupported == "", Reason: d.Unsupported, Engines: []helperapi.DBEngineInfo{}, Unmanaged: []string{}}
	if !s.Supported {
		return s
	}
	all, err := d.engines(ctx)
	if err != nil {
		s.Reason = "status_failed"
		return s
	}
	if d.OnlySoftware != nil {
		if st, err := d.Software.Status(ctx); err == nil {
			for _, capability := range []string{catalog.CapMySQL, catalog.CapPostgreSQL, catalog.CapRedis} {
				for _, sw := range d.Software.Catalog.Providing(capability) {
					if len(st.Installed[sw.ID]) > 0 && !slices.Contains(d.OnlySoftware, sw.ID) {
						s.Unmanaged = append(s.Unmanaged, sw.ID)
					}
				}
			}
		}
	}
	for _, e := range all {
		info := helperapi.DBEngineInfo{ID: e.id, Kind: e.kind, Software: e.software, Line: e.line, Service: e.service,
			Version: e.version, Databases: []helperapi.DBInfo{}, Users: []helperapi.DBUserInfo{}}
		var err error
		switch e.kind {
		case helperapi.DBMySQL:
			err = d.mysqlStatus(ctx, e, &info)
		case helperapi.DBPostgreSQL:
			err = d.pgStatus(ctx, e, &info)
		case helperapi.DBRedis:
			err = d.redisStatus(ctx, e, &info)
		}
		if err != nil {
			info.Error = asError(err)
		}
		s.Engines = append(s.Engines, info)
	}
	return s
}

func (d *DBs) create(ctx context.Context, _ helperapi.Caller, req helperapi.DBCreateRequest, log io.Writer) (any, error) {
	e, err := d.engine(ctx, req.Engine)
	if err != nil {
		return nil, err
	}
	if e.kind == helperapi.DBMySQL {
		return nil, d.mysqlCreate(ctx, log, e, req)
	}
	return nil, d.pgCreate(ctx, log, e, req)
}

func (d *DBs) drop(ctx context.Context, _ helperapi.Caller, req helperapi.DBRef, log io.Writer) (any, error) {
	e, err := d.engine(ctx, req.Engine)
	if err != nil {
		return nil, err
	}
	if !helperapi.ValidDBName(req.Engine, req.Name) {
		return nil, dbErr(helperapi.CodeDBReserved, map[string]any{"name": req.Name})
	}
	if e.kind == helperapi.DBMySQL {
		return nil, d.mysqlDrop(ctx, log, e, req.Name)
	}
	return nil, d.pgDrop(ctx, log, e, req.Name)
}

func (d *DBs) userSet(ctx context.Context, _ helperapi.Caller, req helperapi.DBUserRequest, log io.Writer) (any, error) {
	e, err := d.engine(ctx, req.Engine)
	if err != nil {
		return nil, err
	}
	if e.kind == helperapi.DBMySQL {
		return nil, d.mysqlUserSet(ctx, log, e, req)
	}
	return nil, d.pgUserSet(ctx, log, e, req)
}

func (d *DBs) userDrop(ctx context.Context, _ helperapi.Caller, req helperapi.DBRef, log io.Writer) (any, error) {
	e, err := d.engine(ctx, req.Engine)
	if err != nil {
		return nil, err
	}
	if !helperapi.ValidDBUser(req.Engine, req.Name) {
		return nil, dbErr(helperapi.CodeDBReserved, map[string]any{"name": req.Name})
	}
	if e.kind == helperapi.DBMySQL {
		return nil, d.mysqlUserDrop(ctx, log, e, req.Name)
	}
	return nil, d.pgUserDrop(ctx, log, e, req.Name)
}

func (d *DBs) remote(ctx context.Context, _ helperapi.Caller, req helperapi.DBRemoteRequest, log io.Writer) (any, error) {
	e, err := d.engine(ctx, req.Engine)
	if err != nil {
		return nil, err
	}
	switch e.kind {
	case helperapi.DBMySQL:
		err = d.mysqlRemote(ctx, log, e, req.Enabled)
	case helperapi.DBPostgreSQL:
		err = d.pgRemote(ctx, log, e, req.Enabled)
	default:
		err = d.redisRemote(ctx, log, e, req.Enabled)
	}
	if err != nil || d.Firewall == nil {
		return nil, err
	}
	var ports []string
	if req.Enabled {
		ports = []string{strconv.Itoa(d.enginePort(ctx, e))}
	}
	if ferr := d.Firewall.SetManaged(ctx, log, helperapi.OwnerDBPrefix+e.id, helperapi.ProtoTCP, ports); ferr != nil {
		// The database itself is set; only the firewall lags behind.
		_, _ = fmt.Fprintf(log, "Warning: the firewall could not be updated: %v\n", ferr)
	}
	return nil, nil
}

// enginePort is the TCP port an engine listens on.
func (d *DBs) enginePort(ctx context.Context, e dbEngine) int {
	switch e.kind {
	case helperapi.DBMySQL:
		return 3306
	case helperapi.DBPostgreSQL:
		if c, err := d.pgCluster0(ctx, e); err == nil && c.port > 0 {
			return c.port
		}
		return 5432
	}
	conf, _ := d.redisConf(e)
	return conf.port
}

func (d *DBs) service(ctx context.Context, _ helperapi.Caller, req helperapi.DBServiceRequest, log io.Writer) (any, error) {
	e, err := d.engine(ctx, req.Engine)
	if err != nil {
		return nil, err
	}
	if e.kind == helperapi.DBPostgreSQL {
		c, err := d.pgCluster0(ctx, e)
		if err != nil {
			return nil, err
		}
		return nil, d.pgCtl(ctx, log, c, req.Action)
	}
	return nil, d.systemctl(ctx, log, e, req.Action)
}

// systemctl starts, stops or restarts an engine's service.
func (d *DBs) systemctl(ctx context.Context, log io.Writer, e dbEngine, action string) error {
	if !systemdRunning() {
		return dbErr(helperapi.CodeDBSystemd, map[string]any{"engine": e.id})
	}
	if e.service == "" {
		return dbErr(helperapi.CodeDBEngineMissing, map[string]any{"engine": e.id})
	}
	if err := runCommand(ctx, log, "systemctl", action, e.service); err != nil {
		return dbErr(helperapi.CodeDBRestartFailed, map[string]any{"engine": e.id, "detail": errDetail(err)})
	}
	return nil
}

// errDetail is the last part of a tool's error message.
func errDetail(err error) string {
	var ee *execx.ExitError
	msg := err.Error()
	if errors.As(err, &ee) {
		msg = ee.Stderr
	}
	msg = strings.TrimSpace(msg)
	if len(msg) > 400 {
		msg = msg[len(msg)-400:]
	}
	return msg
}

// Dumps.

// backupRoot opens the panel's data directory. Everything in it belongs
// to the unprivileged panel, so the helper never follows a link out of it.
func (d *DBs) backupRoot() (*os.Root, int, int, error) {
	uid, gid, err := lookupUser(d.PanelUser)
	if err != nil {
		return nil, 0, 0, err
	}
	root, err := os.OpenRoot(d.path(d.DataDir))
	if err != nil {
		return nil, 0, 0, err
	}
	return root, uid, gid, nil
}

// dumpDir returns (creating it) the engine's dump directory inside root.
func dumpDir(root *os.Root, engine string, uid, gid int) (string, error) {
	dir := path.Join(dbBackupsDir, engine)
	parts := strings.Split(dir, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		if err := root.Mkdir(p, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		fi, err := root.Lstat(p)
		if err != nil {
			return "", err
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("%s is not a directory", p)
		}
		if err := root.Lchown(p, uid, gid); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// writeDump stores what fill writes as a new dump file of the engine and
// returns its description; fill's output is gzipped when gz is set.
func (d *DBs) writeDump(engine, database, ext string, gz bool, fill func(w io.Writer) error) (helperapi.DumpInfo, error) {
	root, uid, gid, err := d.backupRoot()
	if err != nil {
		return helperapi.DumpInfo{}, err
	}
	defer func() { _ = root.Close() }()
	dir, err := dumpDir(root, engine, uid, gid)
	if err != nil {
		return helperapi.DumpInfo{}, err
	}
	now := time.Now()
	base := database
	if base == "" {
		base = engine
	}
	base += "_" + now.UTC().Format("20060102-150405")
	tmp := path.Join(dir, ".partial-"+base+ext)
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return helperapi.DumpInfo{}, err
	}
	done := false
	defer func() {
		if !done {
			_ = f.Close()
			_ = root.Remove(tmp)
		}
	}()
	var w io.Writer = f
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(f)
		w = zw
	}
	if err := fill(w); err != nil {
		return helperapi.DumpInfo{}, err
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			return helperapi.DumpInfo{}, err
		}
	}
	// On the disk before it has its name: a dump cut short by a crash must
	// not look like a whole one.
	if err := fsync(f); err != nil {
		return helperapi.DumpInfo{}, err
	}
	if err := f.Close(); err != nil {
		return helperapi.DumpInfo{}, err
	}
	if err := root.Lchown(tmp, uid, gid); err != nil {
		return helperapi.DumpInfo{}, err
	}
	name := base + ext
	for i := 2; ; i++ {
		if _, err := root.Lstat(path.Join(dir, name)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	if err := root.Rename(tmp, path.Join(dir, name)); err != nil {
		return helperapi.DumpInfo{}, err
	}
	done = true
	fi, err := root.Lstat(path.Join(dir, name))
	if err != nil {
		return helperapi.DumpInfo{}, err
	}
	return helperapi.DumpInfo{Engine: engine, Database: database, File: name, SizeBytes: fi.Size(), CreatedAt: now.Unix()}, nil
}

// openDump opens a dump file of the engine for reading, decompressing
// .gz files.
func (d *DBs) openDump(engine, name string) (io.ReadCloser, error) {
	root, _, _, err := d.backupRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	// The directory belongs to the panel: never a link, a FIFO (opened
	// without blocking) or a hard link to a file of someone else's.
	p := path.Join(dbBackupsDir, engine, name)
	f, err := openNoLink(root, p)
	if err != nil {
		return nil, dbErr(helperapi.CodeDBDumpNotFound, map[string]any{"file": name})
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() || linkCount(fi) != 1 {
		_ = f.Close()
		return nil, dbErr(helperapi.CodeDBDumpNotFound, map[string]any{"file": name})
	}
	if !strings.HasSuffix(name, ".gz") {
		return f, nil
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, dbErr(helperapi.CodeDBQuery, map[string]any{"detail": "not a gzip file"})
	}
	return &gzipFile{Reader: zr, f: f}, nil
}

// linkCount is a file's number of hard links (1 when unknown).
func linkCount(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink) //nolint:unconvert // the field's type differs between architectures
	}
	return 1
}

type gzipFile struct {
	*gzip.Reader
	f *os.File
}

func (g *gzipFile) Close() error { return errors.Join(g.Reader.Close(), g.f.Close()) }

func (d *DBs) dump(ctx context.Context, _ helperapi.Caller, req helperapi.DBDumpRequest, log io.Writer) (any, error) {
	e, err := d.engine(ctx, req.Engine)
	if err != nil {
		return nil, err
	}
	var info helperapi.DumpInfo
	switch e.kind {
	case helperapi.DBMySQL:
		info, err = d.mysqlDump(ctx, log, e, req.Name)
	case helperapi.DBPostgreSQL:
		info, err = d.pgDump(ctx, log, e, req.Name)
	default:
		info, err = d.redisDump(ctx, log, e)
	}
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "dump %s written (%d bytes)\n", info.File, info.SizeBytes)
	return info, nil
}

func (d *DBs) restore(ctx context.Context, _ helperapi.Caller, req helperapi.DBRestoreRequest, log io.Writer) (any, error) {
	e, err := d.engine(ctx, req.Engine)
	if err != nil {
		return nil, err
	}
	if e.kind == helperapi.DBMySQL {
		return nil, d.mysqlRestore(ctx, log, e, req)
	}
	return nil, d.pgRestore(ctx, log, e, req)
}

// writeConfig writes a configuration file of a database server, keeping
// the owner, mode and SELinux label of the file it replaces (new files:
// root, 0644). Its directory may belong to the server's account: see
// writeConfFile.
func (d *DBs) writeConfig(p string, data []byte) error {
	return confDBErr(writeConfFile(d.Root, p, data, confWrite{Create: true, Mode: 0o644, MkdirAll: true}))
}

// readConfig reads a configuration file of a database server; see
// readConfFile.
func (d *DBs) readConfig(p string) ([]byte, error) {
	data, _, err := readConfFile(d.Root, p, confReadMax)
	return data, confDBErr(err)
}

// confDBErr turns the refusal of a configuration path into its error
// code; other errors pass unchanged.
func confDBErr(err error) error {
	if ue, ok := asConfUnsafe(err); ok {
		return dbErr(helperapi.CodeDBConfigUnsafe, map[string]any{"path": ue.Path, "reason": ue.Reason})
	}
	return err
}

// tempFile creates a root-only file for secrets passed to a client.
func (d *DBs) tempFile(pattern string, data []byte) (string, func(), error) {
	dir := d.path(dbHelperTmp)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return f.Name(), cleanup, nil
}

// fileOwner returns the owner of a file.
func fileOwner(fi fs.FileInfo) (int, int) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid)
	}
	return -1, -1
}
