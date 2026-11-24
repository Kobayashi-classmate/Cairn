// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// Backups run plans: each target becomes one file in the panel's
// backups/plans/<plan>/ (tar + zstd for sites and directories, the
// database's own dump otherwise), encrypted for the plan's age
// recipients when it has any, copied to the plan's bucket, and thinned
// out by the plan's retention, here and there alike.

const (
	backupStamp   = "20060102-150405"
	runsKept      = 100
	presignTTL    = 15 * time.Minute
	backupTimeout = 12 * time.Hour
)

// Backups keeps and runs backup plans.
type Backups struct {
	Files *Files
	DBs   *DBs
	Log   *slog.Logger
}

// newStore connects to a bucket. Tests replace it.
var newStore = newMinioStore

// RegisterBackups adds the backup actions.
func RegisterBackups(r *Registry, o *Ops, b *Backups) {
	plan := func(id string) []string { return []string{"backup-" + id} }
	Register(r, helperapi.ActionBackupPlanApply, func(_ context.Context, _ helperapi.Caller, p helperapi.BackupPlan) (helperapi.Empty, error) {
		return helperapi.Empty{}, b.savePlan(p)
	})
	Register(r, helperapi.ActionBackupPlanRemove, b.removePlan)
	RegisterOp(r, o, helperapi.ActionBackupRun, OpSpec[helperapi.PlanRef]{Locks: func(q helperapi.PlanRef) []string { return plan(q.Plan) },
		Run: func(ctx context.Context, _ helperapi.Caller, q helperapi.PlanRef, log io.Writer) (any, error) {
			return b.Run(ctx, q.Plan, log)
		}, CancelWhileRunning: true})
	Register(r, helperapi.ActionBackupList, b.list)
	Register(r, helperapi.ActionBackupRuns, func(_ context.Context, _ helperapi.Caller, q helperapi.PlanRef) (helperapi.BackupRuns, error) {
		return helperapi.BackupRuns{Runs: b.runs(q.Plan)}, nil
	})
	RegisterOp(r, o, helperapi.ActionBackupDelete, OpSpec[helperapi.BackupFilesRequest]{Locks: func(q helperapi.BackupFilesRequest) []string { return plan(q.Plan) },
		Run: b.deleteOp})
	Register(r, helperapi.ActionBackupPresign, b.presign)
	RegisterOp(r, o, helperapi.ActionBackupFetch, OpSpec[helperapi.BackupFilesRequest]{Locks: func(q helperapi.BackupFilesRequest) []string { return plan(q.Plan) },
		Run: b.fetchOp, CancelWhileRunning: true})
	RegisterOp(r, o, helperapi.ActionBackupRestore, OpSpec[helperapi.BackupRestoreRequest]{Locks: func(q helperapi.BackupRestoreRequest) []string { return plan(q.Plan) },
		Run: b.restoreOp})
	Register(r, helperapi.ActionBackupTest, b.testStorage)
}

func backupErr(code string, params map[string]any) error {
	return &helperapi.Error{Code: code, Params: params}
}

func (b *Backups) stateDir(parts ...string) string {
	return filepath.Join(append([]string{b.Files.StateDir, "backup"}, parts...)...)
}

func localDir(plan string) string { return "backups/plans/" + plan }

// Plans -------------------------------------------------------------------

func (b *Backups) savePlan(p helperapi.BackupPlan) error {
	if p.Storage == nil {
		p.KeepLocal = true
	}
	data, _ := json.Marshal(p)
	if err := os.MkdirAll(b.stateDir("plans"), 0o700); err != nil {
		return err
	}
	return writeFileMode(b.stateDir("plans", p.ID+".json"), data, 0o600)
}

func (b *Backups) plan(id string) (helperapi.BackupPlan, error) {
	var p helperapi.BackupPlan
	data, err := os.ReadFile(b.stateDir("plans", id+".json")) //nolint:gosec // G304: a validated plan id.
	if err != nil || json.Unmarshal(data, &p) != nil || p.ID != id {
		return p, backupErr(helperapi.CodeBackupPlanNotFound, map[string]any{"plan": id})
	}
	return p, nil
}

func (b *Backups) removePlan(ctx context.Context, _ helperapi.Caller, req helperapi.PlanRemoveRequest) (helperapi.Empty, error) {
	p, err := b.plan(req.Plan)
	if err != nil {
		return helperapi.Empty{}, err
	}
	if req.Files {
		if root, _, _, err := b.panelRoot(); err == nil {
			_ = root.RemoveAll(localDir(p.ID))
			_ = root.Close()
		}
		if p.Storage != nil {
			if st, err := newStore(*p.Storage); err == nil {
				objs, _ := st.list(ctx, p.ID)
				for _, o := range objs {
					_ = st.remove(ctx, p.ID+"/"+o.name)
				}
			}
		}
	}
	_ = os.Remove(b.stateDir("runs", p.ID+".jsonl"))
	return helperapi.Empty{}, os.Remove(b.stateDir("plans", p.ID+".json"))
}

// panelRoot opens the panel's data directory and finds its owner.
func (b *Backups) panelRoot() (*os.Root, int, int, error) {
	uid, gid, err := lookupUser(b.Files.PanelUser)
	if err != nil {
		return nil, 0, 0, err
	}
	root, err := os.OpenRoot(b.Files.path(b.Files.DataDir))
	return root, uid, gid, err
}

// ownedDirs makes dir (and its parents) inside root, owned by uid:gid.
func ownedDirs(root *os.Root, dir string, uid, gid int) error {
	parts := strings.Split(dir, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		if err := root.Mkdir(p, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		fi, err := root.Lstat(p)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", p)
		}
		if err := root.Lchown(p, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

// Names ---------------------------------------------------------------------

// backupName is <target key>_<UTC time><ext>.
func backupName(t helperapi.BackupTarget, at time.Time, ext string, encrypted bool) string {
	n := t.Key() + "_" + at.UTC().Format(backupStamp) + ext
	if encrypted {
		n += ".age"
	}
	return n
}

// parseBackupName returns a backup's target key and time.
func parseBackupName(name string) (string, time.Time, bool) {
	i := strings.LastIndex(name, "_")
	if i <= 0 || len(name) < i+1+len(backupStamp) {
		return "", time.Time{}, false
	}
	at, err := time.Parse(backupStamp, name[i+1:i+1+len(backupStamp)])
	if err != nil {
		return "", time.Time{}, false
	}
	return name[:i], at, true
}

// Running -------------------------------------------------------------------

// lockPlan keeps two runs of a plan apart, across processes (the timer
// runs plans in a process of its own).
func (b *Backups) lockPlan(id string) (func(), error) {
	if err := os.MkdirAll(b.stateDir("locks"), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(b.stateDir("locks", id+".lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: a validated plan id.
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, backupErr(helperapi.CodeBackupRunning, map[string]any{"plan": id})
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// Run runs a plan: from an operation, or from a task's timer.
func (b *Backups) Run(ctx context.Context, id string, log io.Writer) (helperapi.BackupRun, error) {
	p, err := b.plan(id)
	if err != nil {
		return helperapi.BackupRun{}, err
	}
	unlock, err := b.lockPlan(id)
	if err != nil {
		return helperapi.BackupRun{}, err
	}
	defer unlock()
	now := time.Now()
	run := helperapi.BackupRun{Started: now.Unix()}
	root, uid, gid, err := b.panelRoot()
	if err != nil {
		return run, err
	}
	defer func() { _ = root.Close() }()
	if err := ownedDirs(root, localDir(id), uid, gid); err != nil {
		return run, err
	}
	var store objectStore
	if p.Storage != nil {
		if store, err = newStore(*p.Storage); err != nil {
			return run, backupErr(helperapi.CodeBackupStorage, map[string]any{"detail": err.Error()})
		}
	}
	var failure error
	fail := func(err error) {
		_, _ = fmt.Fprintf(log, "! %v\n", err)
		if failure == nil {
			failure = err
		}
	}
	for _, t := range p.Targets {
		if ctx.Err() != nil {
			fail(ctx.Err())
			break
		}
		name, size, err := b.produce(ctx, root, p, t, now, uid, gid, log)
		if err != nil {
			fail(fmt.Errorf("%s: %w", t.Key(), err))
			continue
		}
		run.Files, run.Size = append(run.Files, name), run.Size+size
		_, _ = fmt.Fprintf(log, "%s: %d bytes\n", name, size)
		if store == nil {
			continue
		}
		if err := upload(ctx, root, store, p.ID, name, size); err != nil {
			fail(backupErr(helperapi.CodeBackupStorage, map[string]any{"file": name, "detail": err.Error()}))
			continue
		}
		_, _ = fmt.Fprintf(log, "%s: copied to %s\n", name, p.Storage.Bucket)
		if !p.KeepLocal {
			_ = root.Remove(localDir(id) + "/" + name)
		}
	}
	run.Deleted = b.prune(ctx, p, root, store, log)
	run.Finished, run.OK = time.Now().Unix(), failure == nil
	if failure != nil {
		run.Error = asError(failure)
	}
	b.record(id, run)
	if failure != nil {
		return run, failure
	}
	return run, nil
}

// produce writes one target's backup and returns its name and size.
func (b *Backups) produce(ctx context.Context, root *os.Root, p helperapi.BackupPlan, t helperapi.BackupTarget, now time.Time,
	uid, gid int, log io.Writer) (string, int64, error) {
	ext, zstded := ".tar.zst", true
	var engine dbEngine
	if t.Kind == helperapi.BackupDatabase {
		var err error
		if engine, err = b.DBs.engine(ctx, t.Engine); err != nil {
			return "", 0, err
		}
		ext = ".sql.zst"
		if engine.kind == helperapi.DBPostgreSQL {
			ext, zstded = ".dump", false // compressed already
		}
	}
	name := backupName(t, now, ext, len(p.Recipients) > 0)
	tmp := localDir(p.ID) + "/.partial-" + name
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
			_ = root.Remove(tmp)
		}
	}()
	var w io.Writer = bufio.NewWriterSize(file, 1<<20)
	bw := w.(*bufio.Writer)
	var closers []io.Closer
	if len(p.Recipients) > 0 {
		var recipients []age.Recipient
		for _, r := range p.Recipients {
			rc, err := age.ParseX25519Recipient(r)
			if err != nil {
				return "", 0, err
			}
			recipients = append(recipients, rc)
		}
		aw, err := age.Encrypt(w, recipients...)
		if err != nil {
			return "", 0, err
		}
		w, closers = aw, append(closers, aw)
	}
	if zstded {
		zw, err := zstd.NewWriter(w)
		if err != nil {
			return "", 0, err
		}
		w, closers = zw, append(closers, zw)
	}
	switch t.Kind {
	case helperapi.BackupSite:
		err = b.tarSite(ctx, t.Site, w, log)
	case helperapi.BackupDirectory:
		err = b.tarDirectory(ctx, t, w, log)
	default:
		if engine.kind == helperapi.DBMySQL {
			err = b.DBs.mysqlDumpTo(ctx, log, engine, t.Database, w)
		} else {
			var c pgClusterInfo
			if c, err = b.DBs.pgOnline(ctx, engine); err == nil {
				err = b.DBs.pgDumpTo(ctx, log, engine, c, t.Database, w)
			}
		}
	}
	if err != nil {
		return "", 0, err
	}
	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i].Close(); err != nil {
			return "", 0, err
		}
	}
	if err := bw.Flush(); err != nil {
		return "", 0, err
	}
	if err := file.Sync(); err != nil {
		return "", 0, err
	}
	if err := file.Chown(uid, gid); err != nil {
		return "", 0, err
	}
	fi, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if err := file.Close(); err != nil {
		return "", 0, err
	}
	if err := root.Rename(tmp, localDir(p.ID)+"/"+name); err != nil {
		return "", 0, err
	}
	ok = true
	return name, fi.Size(), nil
}

// tarTree writes what is below start (links as links, not followed) as
// a tar stream, names relative to start; skip leaves directories out. The
// tree is walked one level at a time (see walkTree): a start that is a
// link is not walked, and neither is a directory swapped for one.
func tarTree(ctx context.Context, root *os.Root, start string, tw *tar.Writer, skip func(rel string) bool, log io.Writer) error {
	return walkTree(ctx, root, start, func(e walkEntry) error {
		name := e.Rel
		if name == "" {
			return nil
		}
		info := e.Info
		if skip != nil && skip(name) {
			if info.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		var target string
		if info.Mode()&fs.ModeSymlink != 0 {
			var err error
			if target, err = e.Dir.Readlink(e.Name); err != nil {
				return nil //nolint:nilerr // gone meanwhile
			}
		} else if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		h, err := tar.FileInfoHeader(info, target)
		if err != nil {
			return err
		}
		h.Name, h.Format = name, tar.FormatPAX
		if info.IsDir() {
			h.Name += "/"
		}
		if !info.Mode().IsRegular() {
			return tw.WriteHeader(h)
		}
		in, fi, err := e.open()
		if err != nil {
			_, _ = fmt.Fprintf(log, "  skipped %s: %v\n", e.Path, err)
			return nil // gone or swapped meanwhile
		}
		defer func() { _ = in.Close() }()
		h.Size = fi.Size()
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		// A file growing or shrinking meanwhile still fills its entry
		// exactly.
		return copyN(tw, in, h.Size)
	})
}

// tarSite archives a site's directory (without tmp/) and its settings.
func (b *Backups) tarSite(ctx context.Context, code string, w io.Writer, log io.Writer) error {
	root, err := b.Files.openRoot(helperapi.RootSites)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if fi, err := root.top.Lstat(code); err != nil || !fi.IsDir() {
		return backupErr(helperapi.CodeFileNotFound, map[string]any{"site": code})
	}
	tw := tar.NewWriter(w)
	if spec, err := os.ReadFile(b.Files.path(path.Join(siteStateDir, code+".json"))); err == nil { //nolint:gosec // G304: a validated site code.
		if err := tw.WriteHeader(&tar.Header{Name: ".cairn/site.json", Mode: 0o600, Size: int64(len(spec)), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := tw.Write(spec); err != nil {
			return err
		}
	}
	if err := tarTree(ctx, root.top, code, tw, func(rel string) bool { return rel == "tmp" }, log); err != nil {
		return err
	}
	return tw.Close()
}

func (b *Backups) tarDirectory(ctx context.Context, t helperapi.BackupTarget, w io.Writer, log io.Writer) error {
	root, err := b.Files.openRoot(t.Root)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	// The directory is opened once and walked through a root of its own:
	// what is swapped above it meanwhile changes nothing. A link on the
	// way to it, or in its place, is followed only where the file manager
	// follows one (see fileRoot): a site's owner cannot have another
	// site's directory backed up as their own.
	dir, err := root.dir(t.Path, true)
	if err != nil {
		return backupErr(helperapi.CodeFileNotFound, map[string]any{"path": t.Path})
	}
	defer func() { _ = dir.Close() }()
	tw := tar.NewWriter(w)
	// The plans' own backups are not backed up again.
	skip := func(name string) bool {
		full := join(t.Path, name)
		return full == trashOf(t.Root) || t.Root == helperapi.RootBackups && full == "plans"
	}
	if err := tarTree(ctx, dir, "", tw, skip, log); err != nil {
		return err
	}
	return tw.Close()
}

// openBackup opens a backup file of the panel's directory for reading. The
// directory is the panel's: only a regular file with no other name is
// read (not a pipe, which would hang, nor a hard link to a file the
// panel could not read itself).
func openBackup(root *os.Root, p string) (*os.File, error) {
	f, err := root.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && (!fi.Mode().IsRegular() || linked(fi)) {
		err = fmt.Errorf("%s is not a plain file", p)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func upload(ctx context.Context, root *os.Root, st objectStore, plan, name string, size int64) error {
	f, err := openBackup(root, localDir(plan)+"/"+name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := st.put(ctx, plan+"/"+name, f, size); err != nil {
		return err
	}
	got, err := st.stat(ctx, plan+"/"+name)
	if err != nil {
		return err
	}
	if got != size {
		return fmt.Errorf("the bucket holds %d bytes of %d", got, size)
	}
	return nil
}

// keep picks, from backups newest first, those the retention keeps.
func keep(times []time.Time, r helperapi.Retention) map[int]bool {
	kept := map[int]bool{}
	for i := 0; i < len(times) && i < r.Last; i++ {
		kept[i] = true
	}
	bucket := func(n int, key func(time.Time) string) {
		seen := map[string]bool{}
		for i, at := range times {
			k := key(at.Local())
			if seen[k] {
				continue
			}
			if len(seen) == n {
				return
			}
			seen[k] = true
			kept[i] = true
		}
	}
	bucket(r.Daily, func(t time.Time) string { return t.Format("2006-01-02") })
	bucket(r.Weekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-%02d", y, w) })
	bucket(r.Monthly, func(t time.Time) string { return t.Format("2006-01") })
	return kept
}

// doomed lists the names retention drops.
func doomed(names []string, r helperapi.Retention) []string {
	type item struct {
		name string
		at   time.Time
	}
	groups := map[string][]item{}
	for _, n := range names {
		if key, at, ok := parseBackupName(n); ok {
			groups[key] = append(groups[key], item{n, at})
		}
	}
	var out []string
	for _, items := range groups {
		sort.Slice(items, func(i, j int) bool { return items[i].at.After(items[j].at) })
		times := make([]time.Time, len(items))
		for i, it := range items {
			times[i] = it.at
		}
		kept := keep(times, r)
		for i, it := range items {
			if !kept[i] {
				out = append(out, it.name)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (b *Backups) localNames(root *os.Root, plan string) []string {
	d, err := root.Open(localDir(plan))
	if err != nil {
		return nil
	}
	defer func() { _ = d.Close() }()
	names, _ := d.Readdirnames(-1)
	var out []string
	for _, n := range names {
		if helperapi.ValidBackupName(n) {
			out = append(out, n)
		}
	}
	return out
}

// prune applies the retention here and in the bucket.
func (b *Backups) prune(ctx context.Context, p helperapi.BackupPlan, root *os.Root, st objectStore, log io.Writer) int {
	n := 0
	for _, name := range doomed(b.localNames(root, p.ID), p.Retention) {
		if root.Remove(localDir(p.ID)+"/"+name) == nil {
			_, _ = fmt.Fprintf(log, "removed %s (retention)\n", name)
			n++
		}
	}
	if st == nil {
		return n
	}
	objs, err := st.list(ctx, p.ID)
	if err != nil {
		_, _ = fmt.Fprintf(log, "! listing %s: %v\n", p.Storage.Bucket, err)
		return n
	}
	var names []string
	for _, o := range objs {
		names = append(names, o.name)
	}
	for _, name := range doomed(names, p.Retention) {
		if st.remove(ctx, p.ID+"/"+name) == nil {
			_, _ = fmt.Fprintf(log, "removed %s from %s (retention)\n", name, p.Storage.Bucket)
			n++
		}
	}
	return n
}

// Run records -----------------------------------------------------------------

func (b *Backups) record(plan string, run helperapi.BackupRun) {
	runs := append([]helperapi.BackupRun{run}, b.runs(plan)...)
	if len(runs) > runsKept {
		runs = runs[:runsKept]
	}
	var buf strings.Builder
	for i := len(runs) - 1; i >= 0; i-- {
		line, _ := json.Marshal(runs[i])
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.MkdirAll(b.stateDir("runs"), 0o700); err == nil {
		_ = writeFileMode(b.stateDir("runs", plan+".jsonl"), []byte(buf.String()), 0o600)
	}
}

// runs lists a plan's runs, newest first.
func (b *Backups) runs(plan string) []helperapi.BackupRun {
	data, _ := os.ReadFile(b.stateDir("runs", plan+".jsonl")) //nolint:gosec // G304: a validated plan id.
	out := []helperapi.BackupRun{}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var r helperapi.BackupRun
		if json.Unmarshal([]byte(lines[i]), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// Listing, deleting, downloading -------------------------------------------------

func (b *Backups) list(ctx context.Context, _ helperapi.Caller, req helperapi.PlanRef) (helperapi.BackupList, error) {
	p, err := b.plan(req.Plan)
	if err != nil {
		return helperapi.BackupList{}, err
	}
	out := helperapi.BackupList{Files: []helperapi.BackupFile{}}
	byName := map[string]*helperapi.BackupFile{}
	add := func(name string, size int64, local bool) {
		key, at, ok := parseBackupName(name)
		if !ok {
			return
		}
		f := byName[name]
		if f == nil {
			f = &helperapi.BackupFile{Name: name, Target: key, Size: size, Created: at.Unix(), Encrypted: strings.HasSuffix(name, ".age")}
			byName[name] = f
		}
		if local {
			f.Local = true
		} else {
			f.Remote = true
		}
	}
	if root, _, _, err := b.panelRoot(); err == nil {
		for _, n := range b.localNames(root, p.ID) {
			if fi, err := root.Lstat(localDir(p.ID) + "/" + n); err == nil && fi.Mode().IsRegular() {
				add(n, fi.Size(), true)
			}
		}
		_ = root.Close()
	}
	if p.Storage != nil {
		st, err := newStore(*p.Storage)
		var objs []objectInfo
		if err == nil {
			objs, err = st.list(ctx, p.ID)
		}
		if err != nil {
			out.RemoteError = &helperapi.Error{Code: helperapi.CodeBackupStorage, Params: map[string]any{"detail": err.Error()}}
		}
		for _, o := range objs {
			add(o.name, o.size, false)
		}
	}
	for _, f := range byName {
		out.Files = append(out.Files, *f)
	}
	sort.Slice(out.Files, func(i, j int) bool {
		if out.Files[i].Created != out.Files[j].Created {
			return out.Files[i].Created > out.Files[j].Created
		}
		return out.Files[i].Name < out.Files[j].Name
	})
	return out, nil
}

func (b *Backups) deleteOp(ctx context.Context, _ helperapi.Caller, req helperapi.BackupFilesRequest, log io.Writer) (any, error) {
	p, err := b.plan(req.Plan)
	if err != nil {
		return nil, err
	}
	root, _, _, err := b.panelRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var st objectStore
	if req.Remote && p.Storage != nil {
		if st, err = newStore(*p.Storage); err != nil {
			return nil, backupErr(helperapi.CodeBackupStorage, map[string]any{"detail": err.Error()})
		}
	}
	bt := &batch{log: log}
	for _, n := range req.Names {
		lerr := root.Remove(localDir(p.ID) + "/" + n)
		rerr := fs.ErrNotExist
		if st != nil {
			rerr = st.remove(ctx, p.ID+"/"+n)
		}
		if errors.Is(lerr, fs.ErrNotExist) && rerr != nil {
			bt.failed(n, backupErr(helperapi.CodeBackupNotFound, map[string]any{"name": n}))
			continue
		}
		bt.done("deleted %s", n)
	}
	return bt.result()
}

func (b *Backups) presign(ctx context.Context, _ helperapi.Caller, req helperapi.BackupFilesRequest) (helperapi.PresignResponse, error) {
	p, err := b.plan(req.Plan)
	if err != nil {
		return helperapi.PresignResponse{}, err
	}
	if p.Storage == nil {
		return helperapi.PresignResponse{}, backupErr(helperapi.CodeBackupNoStorage, nil)
	}
	st, err := newStore(*p.Storage)
	if err != nil {
		return helperapi.PresignResponse{}, backupErr(helperapi.CodeBackupStorage, map[string]any{"detail": err.Error()})
	}
	u, err := st.presign(ctx, p.ID+"/"+req.Names[0], presignTTL)
	if err != nil {
		return helperapi.PresignResponse{}, backupErr(helperapi.CodeBackupStorage, map[string]any{"detail": err.Error()})
	}
	return helperapi.PresignResponse{URL: u, Expires: time.Now().Add(presignTTL).Unix()}, nil
}

// fetchLocal brings a remote copy into the panel's directory unless it is
// there already.
func (b *Backups) fetchLocal(ctx context.Context, p helperapi.BackupPlan, root *os.Root, name string, uid, gid int, log io.Writer) error {
	if fi, err := root.Lstat(localDir(p.ID) + "/" + name); err == nil && fi.Mode().IsRegular() {
		return nil
	}
	if p.Storage == nil {
		return backupErr(helperapi.CodeBackupNotFound, map[string]any{"name": name})
	}
	st, err := newStore(*p.Storage)
	if err != nil {
		return backupErr(helperapi.CodeBackupStorage, map[string]any{"detail": err.Error()})
	}
	in, err := st.get(ctx, p.ID+"/"+name)
	if err != nil {
		return backupErr(helperapi.CodeBackupNotFound, map[string]any{"name": name, "detail": err.Error()})
	}
	defer func() { _ = in.Close() }()
	if err := ownedDirs(root, localDir(p.ID), uid, gid); err != nil {
		return err
	}
	// The directory is the panel's: whatever it put at the temporary name
	// (a hard link to another file, say) is removed, never written into.
	tmp := localDir(p.ID) + "/.partial-" + name
	if err := root.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, in)
	if err == nil {
		err = out.Chown(uid, gid)
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = root.Remove(tmp)
		return backupErr(helperapi.CodeBackupStorage, map[string]any{"detail": err.Error()})
	}
	_, _ = fmt.Fprintf(log, "fetched %s (%d bytes)\n", name, n)
	return root.Rename(tmp, localDir(p.ID)+"/"+name)
}

func (b *Backups) fetchOp(ctx context.Context, _ helperapi.Caller, req helperapi.BackupFilesRequest, log io.Writer) (any, error) {
	p, err := b.plan(req.Plan)
	if err != nil {
		return nil, err
	}
	root, uid, gid, err := b.panelRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	bt := &batch{log: log}
	for _, n := range req.Names {
		if err := b.fetchLocal(ctx, p, root, n, uid, gid, log); err != nil {
			bt.failed(n, err)
			continue
		}
		bt.done("%s is in the panel's directory", n)
	}
	return bt.result()
}

// Restoring -------------------------------------------------------------------

// dbOfBackup reads the engine and database out of a database backup's
// target key: db-mysql-<name> or db-postgresql-<line>-<name>.
func dbOfBackup(key string) (string, string, bool) { return helperapi.DatabaseOfBackup(key) }

func (b *Backups) restoreOp(ctx context.Context, _ helperapi.Caller, req helperapi.BackupRestoreRequest, log io.Writer) (any, error) {
	p, err := b.plan(req.Plan)
	if err != nil {
		return nil, err
	}
	key, _, ok := parseBackupName(req.Name)
	if !ok {
		return nil, backupErr(helperapi.CodeBackupNotFound, map[string]any{"name": req.Name})
	}
	// A database backup that names no database is not unpacked as files
	// instead (its destination was never validated).
	if _, _, isDB := dbOfBackup(key); strings.HasPrefix(key, "db-") && !isDB {
		return nil, backupErr(helperapi.CodeBackupNotFound, map[string]any{"name": req.Name})
	}
	root, uid, gid, err := b.panelRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	local := localDir(p.ID) + "/" + req.Name
	if _, err := root.Lstat(local); err != nil {
		// Fetched for this restore only: the plan keeps no local copies.
		if err := b.fetchLocal(ctx, p, root, req.Name, uid, gid, log); err != nil {
			return nil, err
		}
		defer func() { _ = root.Remove(local) }()
	}
	f, err := openBackup(root, local)
	if err != nil {
		return nil, backupErr(helperapi.CodeBackupNotFound, map[string]any{"name": req.Name})
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	name := req.Name
	if base, ok := strings.CutSuffix(name, ".age"); ok {
		if req.Identity == "" {
			return nil, backupErr(helperapi.CodeBackupIdentity, nil)
		}
		id, err := age.ParseX25519Identity(req.Identity)
		if err != nil {
			return nil, backupErr(helperapi.CodeBackupBadIdentity, nil)
		}
		if r, err = age.Decrypt(f, id); err != nil {
			return nil, backupErr(helperapi.CodeBackupBadIdentity, nil)
		}
		name = base
	}
	if strings.HasSuffix(name, ".zst") {
		zr, err := zstd.NewReader(r)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		r, name = zr, strings.TrimSuffix(name, ".zst")
	}
	if engine, database, ok := dbOfBackup(key); ok {
		ext := ".sql"
		if strings.HasSuffix(name, ".dump") {
			ext = ".dump"
		}
		// The same budget as other extractions: a compressed bomb must not
		// fill the panel's file system.
		budget := spaceAt(root, "")
		info, err := b.DBs.writeDump(engine, database, ext, false, func(w io.Writer) error {
			n, err := io.Copy(w, io.LimitReader(r, budget+1))
			if err == nil && n > budget {
				err = errNoSpace
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		_, _ = fmt.Fprintf(log, "dump %s of %s is ready to restore\n", info.File, engine)
		return helperapi.BackupRestoreResult{Engine: engine, Dump: info.File}, nil
	}
	dest, err := b.Files.openRoot(req.Dest.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dest.Close() }()
	if protected(req.Dest.Root, req.Dest.Path) && req.Dest.Path != "" {
		return nil, protectedErr(req.Dest.Path)
	}
	droot, dfd, duid, dgid, err := openDest(dest, req.Dest.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = droot.Close(); _ = dfd.Close() }()
	x := &extractor{ctx: ctx, rootID: req.Dest.Root, root: droot, dest: req.Dest.Path, uid: duid, gid: dgid, overwrite: req.Overwrite,
		b: &batch{log: log}, budget: spaceOf(dfd), files: map[string]bool{}}
	if err := x.tarFrom(r); err != nil {
		var he *helperapi.Error
		if !errors.As(err, &he) {
			err = fileErr(helperapi.CodeFileBadArchive, map[string]any{"detail": tailString(err.Error(), 200)})
		}
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "%d entries restored into /%s, %d skipped\n", x.b.res.Done, req.Dest.Path, len(x.b.res.Failed))
	return helperapi.BackupRestoreResult{Done: x.b.res.Done}, nil
}

func (b *Backups) testStorage(ctx context.Context, _ helperapi.Caller, req helperapi.StorageTestRequest) (helperapi.Empty, error) {
	st, err := newStore(req.Storage)
	if err == nil {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		_, err = st.list(ctx, "")
	}
	if err != nil {
		return helperapi.Empty{}, backupErr(helperapi.CodeBackupStorage, map[string]any{"detail": tailString(err.Error(), 300)})
	}
	return helperapi.Empty{}, nil
}
