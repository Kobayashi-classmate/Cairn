// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// The file manager: every request names a root and a path inside it, and
// the helper works through an os.Root of that root, so no path (and no
// link) leads outside it. New entries belong to the owner of the
// directory they are made in, so that a site's files stay its owner's.

const (
	trashName      = ".cairn-trash"
	systemTrash    = "var/lib/cairn-helper/trash"
	accessFile     = "access.json"
	defaultFileMod = 0o644
	defaultDirMod  = 0o755
)

// Files gives the file manager and the terminal their access.
type Files struct {
	// Root prefixes every system path ("" means /; tests).
	Root string
	// DataDir is the panel's data directory: its backups/ is a root, its
	// uploads/ holds finished uploads.
	DataDir   string
	PanelUser string
	// StateDir keeps the access switches.
	StateDir string
	// RootAccess is the server's [security] root_access: "never" keeps
	// full-disk access and root terminals off.
	RootAccess string
	Log        *slog.Logger

	mu    sync.Mutex
	terms int
	// shells are the shells started without systemd, by PID, until they
	// end (see termClose).
	shells map[int]*os.Process
}

// RegisterFiles adds the file and terminal actions.
func RegisterFiles(r *Registry, o *Ops, f *Files) {
	none := func(any) []string { return nil }
	Register(r, helperapi.ActionFileRoots, func(_ context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.FileRoots, error) {
		return f.roots(), nil
	})
	Register(r, helperapi.ActionAccessSet, f.setAccess)
	Register(r, helperapi.ActionFileList, f.list)
	Register(r, helperapi.ActionFileStat, f.stat)
	Register(r, helperapi.ActionFileRead, f.read)
	Register(r, helperapi.ActionFileWrite, f.write)
	RegisterFD(r, helperapi.ActionFileOpen, f.open)
	Register(r, helperapi.ActionFileMkdir, f.mkdir)
	Register(r, helperapi.ActionFileTouch, f.touch)
	Register(r, helperapi.ActionFileRename, f.rename)
	Register(r, helperapi.ActionFileSearch, f.search)
	Register(r, helperapi.ActionTrashList, f.trashList)
	Register(r, helperapi.ActionFileArchiving, func(_ context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.Archivers, error) {
		return f.archivers(), nil
	})
	RegisterOp(r, o, helperapi.ActionFileCopy, OpSpec[helperapi.FileTransferRequest]{Locks: func(q helperapi.FileTransferRequest) []string { return none(q) }, Run: f.copyOp})
	RegisterOp(r, o, helperapi.ActionFileMove, OpSpec[helperapi.FileTransferRequest]{Locks: func(q helperapi.FileTransferRequest) []string { return none(q) }, Run: f.moveOp})
	RegisterOp(r, o, helperapi.ActionFileDelete, OpSpec[helperapi.FileDeleteRequest]{Locks: func(q helperapi.FileDeleteRequest) []string { return none(q) }, Run: f.deleteOp})
	RegisterOp(r, o, helperapi.ActionFileChmod, OpSpec[helperapi.FileChmodRequest]{Locks: func(q helperapi.FileChmodRequest) []string { return none(q) }, Run: f.chmodOp})
	RegisterOp(r, o, helperapi.ActionFileChown, OpSpec[helperapi.FileChownRequest]{Locks: func(q helperapi.FileChownRequest) []string { return none(q) }, Run: f.chownOp})
	RegisterOp(r, o, helperapi.ActionFileCompress, OpSpec[helperapi.FileCompressRequest]{Locks: func(q helperapi.FileCompressRequest) []string { return none(q) }, Run: f.compressOp,
		CancelWhileRunning: true})
	RegisterOp(r, o, helperapi.ActionFileExtract, OpSpec[helperapi.FileExtractRequest]{Locks: func(q helperapi.FileExtractRequest) []string { return none(q) }, Run: f.extractOp,
		CancelWhileRunning: true})
	RegisterOp(r, o, helperapi.ActionFilePlace, OpSpec[helperapi.FilePlaceRequest]{Locks: func(q helperapi.FilePlaceRequest) []string { return none(q) }, Run: f.placeOp})
	RegisterOp(r, o, helperapi.ActionTrashRestore, OpSpec[helperapi.TrashRequest]{Locks: func(q helperapi.TrashRequest) []string { return none(q) }, Run: f.restoreOp})
	RegisterOp(r, o, helperapi.ActionTrashPurge, OpSpec[helperapi.TrashPurgeRequest]{Locks: func(q helperapi.TrashPurgeRequest) []string { return none(q) }, Run: f.purgeOp})
	Register(r, helperapi.ActionTermUsers, func(_ context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.TermUsers, error) {
		return f.termUsers(), nil
	})
	RegisterFD(r, helperapi.ActionTermOpen, f.termOpen)
	Register(r, helperapi.ActionTermClose, f.termClose)
}

func fileErr(code string, params map[string]any) error {
	return &helperapi.Error{Code: code, Params: params}
}

func (f *Files) path(p string) string { return filepath.Join(f.Root, p) }

// Access switches ---------------------------------------------------------

func (f *Files) access() helperapi.AccessSettings {
	var s helperapi.AccessSettings
	if data, err := os.ReadFile(filepath.Join(f.StateDir, accessFile)); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if f.RootAccess == "never" {
		s = helperapi.AccessSettings{Locked: true}
	}
	return s
}

func (f *Files) setAccess(_ context.Context, c helperapi.Caller, s helperapi.AccessSettings) (helperapi.AccessSettings, error) {
	if f.RootAccess == "never" && (s.FullDisk || s.RootTerminal) {
		return f.access(), fileErr(helperapi.CodeAccessLocked, nil)
	}
	s.Locked = false
	data, _ := json.Marshal(s)
	if err := os.MkdirAll(f.StateDir, 0o700); err != nil {
		return s, err
	}
	if err := writeFileMode(filepath.Join(f.StateDir, accessFile), data, 0o600); err != nil {
		return s, err
	}
	f.Log.Info("access switches changed", "fullDisk", s.FullDisk, "rootTerminal", s.RootTerminal, "user", c.Username, "remote", c.Remote)
	return f.access(), nil
}

// Roots -------------------------------------------------------------------

// rootPath is a root's directory; "" for an id that names no root (never
// the whole disk by default).
func (f *Files) rootPath(id string) string {
	switch id {
	case helperapi.RootSites:
		return sitesDir
	case helperapi.RootBackups:
		return path.Join(f.DataDir, "backups")
	case helperapi.RootSystem:
		return "/"
	}
	return ""
}

func (f *Files) roots() helperapi.FileRoots {
	out := helperapi.FileRoots{Settings: f.access()}
	for _, id := range []string{helperapi.RootSites, helperapi.RootBackups, helperapi.RootSystem} {
		r := helperapi.FileRoot{ID: id, Path: f.rootPath(id)}
		switch id {
		case helperapi.RootSystem:
			r.Available = out.Settings.FullDisk
		case helperapi.RootBackups:
			r.Available = true // made on first use
		default:
			fi, err := os.Stat(f.path(r.Path))
			r.Available = err == nil && fi.IsDir()
		}
		out.Roots = append(out.Roots, r)
	}
	return out
}

// fileRoot is a request's root. A path of a request is resolved in it
// once, into an open directory (a root of its own) and a name in it, and
// everything after that goes through the open directory: what is swapped
// along the path meanwhile changes nothing (see fdwalk.go).
//
// Links on the way are followed where they stay inside the root. Sites
// are apart from each other besides: an entry of the directory that holds
// them (the sites root itself; srv/www in the system root) is never
// followed as a link, and a path into one is resolved in that entry's own
// root, so that a link a site's owner made leads to their own site or
// nowhere: not to another site, and not to the rest of the system.
type fileRoot struct {
	id  string
	top *os.Root
	// apart: the entries of the directory sites ("" is the root itself)
	// are kept apart.
	apart bool
	sites string
}

func (r *fileRoot) Close() error { return r.top.Close() }

// openRoot opens a root for a request; an unknown id is refused.
func (f *Files) openRoot(id string) (*fileRoot, error) {
	unavailable := fileErr(helperapi.CodeFileRootUnavailable, map[string]any{"root": id})
	out := &fileRoot{id: id}
	switch id {
	case helperapi.RootSystem:
		if !f.access().FullDisk {
			return nil, unavailable
		}
		out.apart, out.sites = true, strings.TrimPrefix(sitesDir, "/")
	case helperapi.RootSites:
		out.apart = true
	case helperapi.RootBackups:
		r, err := f.backupsRoot()
		if err != nil {
			return nil, err
		}
		out.top = r
		return out, nil
	default:
		return nil, unavailable
	}
	r, err := os.OpenRoot(f.path(f.rootPath(id)))
	if err != nil {
		return nil, unavailable
	}
	out.top = r
	return out, nil
}

// scope returns the root p is resolved in and p's path in it: for a path
// into a site that site's own root, the request's root otherwise. single
// reports a path that names an entry of the directory holding the sites,
// which is never followed as a link. done closes what was opened.
func (r *fileRoot) scope(p string) (base *os.Root, rest string, single bool, done func(), err error) {
	none := func() {}
	inner := p
	if r.sites != "" {
		var below bool
		if inner, below = strings.CutPrefix(p, r.sites+"/"); !below {
			return r.top, p, false, none, nil
		}
	}
	if !r.apart || inner == "" {
		return r.top, p, false, none, nil
	}
	holder, closeHolder := r.top, none
	if r.sites != "" {
		// The way to the sites is the system's own.
		if holder, err = r.top.OpenRoot(r.sites); err != nil {
			return nil, "", false, nil, notFound(err, p)
		}
		closeHolder = func() { _ = holder.Close() }
	}
	site, tail, deeper := strings.Cut(inner, "/")
	if !deeper {
		return holder, site, true, closeHolder, nil
	}
	sub, err := subDir(holder, site)
	if err != nil {
		closeHolder()
		return nil, "", false, nil, notFound(err, p)
	}
	return sub, tail, false, func() { _ = sub.Close(); closeHolder() }, nil
}

// dir opens the directory p as a root of its own. With follow, p itself
// may be a link to a directory (where links are followed at all: see
// fileRoot); without, it must be a directory, as wherever something is
// put into it.
func (r *fileRoot) dir(p string, follow bool) (*os.Root, error) {
	base, rest, single, done, err := r.scope(p)
	if err != nil {
		return nil, err
	}
	defer done()
	notDir := fileErr(helperapi.CodeFileNotDir, map[string]any{"path": p})
	if rest == "" {
		return base.OpenRoot(".")
	}
	if follow && !single {
		d, err := base.OpenRoot(rest)
		if err != nil {
			if fi, serr := base.Stat(rest); serr == nil && !fi.IsDir() {
				return nil, notDir
			}
			return nil, notFound(err, p)
		}
		return d, nil
	}
	parent := base
	if above := parentOf(rest); above != "" {
		if parent, err = base.OpenRoot(above); err != nil {
			return nil, notFound(err, p)
		}
		defer func() { _ = parent.Close() }()
	}
	d, err := subDir(parent, path.Base(rest))
	if errors.Is(err, syscall.ENOTDIR) {
		return nil, notDir
	}
	if err != nil {
		return nil, notFound(err, p)
	}
	return d, nil
}

// parent opens the directory that holds p and returns p's name in it
// ("." for the root itself). done closes what was opened.
func (r *fileRoot) parent(p string) (dir *os.Root, name string, done func(), err error) {
	if p == "" {
		return r.top, ".", func() {}, nil
	}
	d, err := r.dir(parentOf(p), true)
	if err != nil {
		if he, ok := helperapi.AsError(err); ok && (he.Code == helperapi.CodeFileNotFound || he.Code == helperapi.CodeFileNotDir) {
			err = fileErr(helperapi.CodeFileNotFound, map[string]any{"path": p})
		}
		return nil, "", nil, err
	}
	return d, path.Base(p), func() { _ = d.Close() }, nil
}

// open opens the file p for reading: itself, or what a link at p leads to
// (where links are followed at all: see fileRoot). It never blocks, so
// that a pipe someone put there cannot hang the request.
func (r *fileRoot) open(p string) (*os.File, error) {
	base, rest, single, done, err := r.scope(p)
	if err != nil {
		return nil, err
	}
	defer done()
	if !single {
		return base.OpenFile(rel(rest), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	}
	// A root follows a link whatever the flags say: the entry is looked
	// at first, and what was opened must be what was looked at.
	isLink := fileErr(helperapi.CodeFileIsLink, map[string]any{"path": p})
	info, err := base.Lstat(rest)
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, isLink
	}
	file, _, err := openEntry(base, rest, info)
	if errors.Is(err, errSwapped) {
		return nil, isLink
	}
	return file, err
}

// mkdirs opens the directory p, made first with its missing parents as
// file.mkdir would make them: each new directory belongs to the owner of
// the one it is made in. No link on the way is followed.
func (r *fileRoot) mkdirs(p string) (*os.Root, error) {
	cur, err := r.top.OpenRoot(".")
	if err != nil || p == "" {
		return cur, err
	}
	walked := ""
	for _, c := range strings.Split(p, "/") {
		walked = join(walked, c)
		next, err := subDir(cur, c)
		if errors.Is(err, fs.ErrNotExist) {
			if err = cur.Mkdir(c, defaultDirMod); err == nil {
				next, err = adoptDir(cur, c, walked)
			}
		}
		_ = cur.Close()
		if errors.Is(err, syscall.ENOTDIR) {
			return nil, fileErr(helperapi.CodeFileNotDir, map[string]any{"path": walked})
		}
		if err != nil {
			return nil, notFound(err, walked)
		}
		cur = next
	}
	return cur, nil
}

// trash opens the root's trash, made first when create is set
// (fs.ErrNotExist when there is none): the directory itself, never a link
// put in its place.
func (r *fileRoot) trash(create bool) (*os.Root, error) {
	name := trashOf(r.id)
	if create {
		if err := r.top.MkdirAll(name, 0o700); err != nil {
			return nil, err
		}
	}
	parent := r.top
	if above := parentOf(name); above != "" {
		p, err := r.top.OpenRoot(above)
		if err != nil {
			return nil, err
		}
		defer func() { _ = p.Close() }()
		parent = p
	}
	t, err := subDir(parent, path.Base(name))
	if err != nil {
		return nil, err
	}
	if create {
		_ = t.Chmod(".", 0o700)
	}
	return t, nil
}

// backupsRoot opens (making it first) the backups root, owned by the
// panel. The data directory belongs to the unprivileged panel, so
// backups/ is opened through a root of the data directory and must be a
// real directory: a link put in its place leads nowhere, not to "/".
func (f *Files) backupsRoot() (*os.Root, error) {
	unavailable := fileErr(helperapi.CodeFileRootUnavailable, map[string]any{"root": helperapi.RootBackups})
	data, err := os.OpenRoot(f.path(f.DataDir))
	if err != nil {
		return nil, err
	}
	defer func() { _ = data.Close() }()
	if err := data.Mkdir("backups", 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	fi, err := data.Lstat("backups")
	if err != nil || !fi.IsDir() {
		return nil, unavailable
	}
	if uid, gid, err := lookupUser(f.PanelUser); err == nil {
		if err := data.Lchown("backups", uid, gid); err != nil {
			return nil, err
		}
	}
	r, err := data.OpenRoot("backups")
	if err != nil {
		return nil, unavailable
	}
	// Still the directory checked above, not something swapped in since.
	if got, err := r.Stat("."); err != nil || !os.SameFile(fi, got) {
		_ = r.Close()
		return nil, unavailable
	}
	return r, nil
}

// trashOf is a root's trash directory (relative to the root).
func trashOf(root string) string {
	if root == helperapi.RootSystem {
		return systemTrash
	}
	return trashName
}

// protected reports whether a path may not be changed by ordinary file
// actions: the trash, and the top-level directories of the system.
func protected(root, p string) bool {
	t := trashOf(root)
	if p == t || strings.HasPrefix(p, t+"/") {
		return true
	}
	return root == helperapi.RootSystem && !strings.Contains(p, "/")
}

func protectedErr(p string) error {
	return fileErr(helperapi.CodeFileProtected, map[string]any{"path": p})
}

// rel turns a request path into an os.Root path.
func rel(p string) string {
	if p == "" {
		return "."
	}
	return p
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func parentOf(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
}

// notFound maps a missing path to its protocol error.
func notFound(err error, p string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fileErr(helperapi.CodeFileNotFound, map[string]any{"path": p})
	}
	var pe *fs.PathError
	if errors.As(err, &pe) && errors.Is(pe.Err, syscall.ENOTDIR) {
		return fileErr(helperapi.CodeFileNotFound, map[string]any{"path": p})
	}
	if strings.Contains(err.Error(), "path escapes from parent") {
		return fileErr(helperapi.CodeFileNotFound, map[string]any{"path": p})
	}
	return err
}

// Entries -----------------------------------------------------------------

// names caches user and group names for a listing.
type names struct{ users, groups map[int]string }

func newNames() *names { return &names{users: map[int]string{}, groups: map[int]string{}} }

func (n *names) user(uid int) string {
	if v, ok := n.users[uid]; ok {
		return v
	}
	v := strconv.Itoa(uid)
	if u, err := user.LookupId(v); err == nil {
		v = u.Username
	}
	n.users[uid] = v
	return v
}

func (n *names) group(gid int) string {
	if v, ok := n.groups[gid]; ok {
		return v
	}
	v := strconv.Itoa(gid)
	if g, err := user.LookupGroupId(v); err == nil {
		v = g.Name
	}
	n.groups[gid] = v
	return v
}

// unixMode is the permission and special bits of a mode.
func unixMode(m fs.FileMode) uint32 {
	v := uint32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		v |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		v |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		v |= 0o1000
	}
	return v
}

// goMode is unixMode's inverse.
func goMode(v uint32) fs.FileMode {
	m := fs.FileMode(v & 0o777)
	if v&0o4000 != 0 {
		m |= fs.ModeSetuid
	}
	if v&0o2000 != 0 {
		m |= fs.ModeSetgid
	}
	if v&0o1000 != 0 {
		m |= fs.ModeSticky
	}
	return m
}

func typeOf(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return helperapi.FileTypeLink
	case m.IsDir():
		return helperapi.FileTypeDir
	case m.IsRegular():
		return helperapi.FileTypeFile
	}
	return helperapi.FileTypeOther
}

// describe is the entry p (in the request's root) that fi was found to be.
func describe(p string, fi fs.FileInfo, n *names) helperapi.FileEntry {
	e := helperapi.FileEntry{Name: path.Base(p), Path: p, Type: typeOf(fi.Mode()), Size: fi.Size(), Mode: unixMode(fi.Mode()),
		ModTime: fi.ModTime().Unix()}
	if p == "" {
		e.Name = ""
	}
	if uid, gid := fileOwner(fi); uid >= 0 {
		e.Owner, e.Group = n.user(uid), n.group(gid)
	}
	return e
}

// entryOf is describe for the entry name of the open directory dir: a
// link comes with its target.
func entryOf(dir *os.Root, name, p string, fi fs.FileInfo, n *names) helperapi.FileEntry {
	e := describe(p, fi, n)
	if e.Type == helperapi.FileTypeLink {
		e.Target, _ = dir.Readlink(name)
	}
	return e
}

func (f *Files) list(_ context.Context, _ helperapi.Caller, req helperapi.FileListRequest) (helperapi.FileList, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return helperapi.FileList{}, err
	}
	defer func() { _ = root.Close() }()
	n := newNames()
	// The directory is opened once; its entries are looked at through it.
	dir, err := root.dir(req.Path, true)
	if err != nil {
		return helperapi.FileList{}, err
	}
	defer func() { _ = dir.Close() }()
	fi, err := dir.Stat(".")
	if err != nil {
		return helperapi.FileList{}, notFound(err, req.Path)
	}
	out := helperapi.FileList{Dir: describe(req.Path, fi, n), Entries: []helperapi.FileEntry{}}
	d, err := dir.Open(".")
	if err != nil {
		return out, notFound(err, req.Path)
	}
	defer func() { _ = d.Close() }()
	for {
		batch, err := d.Readdirnames(512)
		for _, name := range batch {
			p := join(req.Path, name)
			if (!req.Hidden && strings.HasPrefix(name, ".")) || (req.Path == "" && name == trashName) || p == systemTrash {
				continue
			}
			if len(out.Entries) == helperapi.FileListMax {
				out.Truncated = true
				break
			}
			info, err := dir.Lstat(name)
			if err != nil {
				continue
			}
			out.Entries = append(out.Entries, entryOf(dir, name, p, info, n))
		}
		if err != nil || len(batch) == 0 || out.Truncated {
			break
		}
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if (a.Type == helperapi.FileTypeDir) != (b.Type == helperapi.FileTypeDir) {
			return a.Type == helperapi.FileTypeDir
		}
		if la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name); la != lb {
			return la < lb
		}
		return a.Name < b.Name
	})
	return out, nil
}

func (f *Files) stat(_ context.Context, _ helperapi.Caller, req helperapi.FileRef) (helperapi.FileEntry, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return helperapi.FileEntry{}, err
	}
	defer func() { _ = root.Close() }()
	dir, name, done, err := root.parent(req.Path)
	if err != nil {
		return helperapi.FileEntry{}, err
	}
	defer done()
	fi, err := dir.Lstat(name)
	if err != nil {
		return helperapi.FileEntry{}, notFound(err, req.Path)
	}
	return entryOf(dir, name, req.Path, fi, newNames()), nil
}

// etagOf identifies a version of a file. Saves replace the file, so the
// inode changes even when the clock is too coarse to tell two saves apart.
func etagOf(fi fs.FileInfo) string {
	var ino uint64
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		ino = st.Ino
	}
	return fmt.Sprintf("%x-%x-%x", fi.Size(), fi.ModTime().UnixNano(), ino)
}

// looksBinary reports whether data is not text the editor can show.
func looksBinary(data []byte) bool {
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	for _, b := range head {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(data)
}

func (f *Files) read(_ context.Context, _ helperapi.Caller, req helperapi.FileRef) (helperapi.FileContent, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return helperapi.FileContent{}, err
	}
	defer func() { _ = root.Close() }()
	// Non-blocking, so that a pipe someone put there cannot hang it; only
	// regular files are read (checked below).
	file, err := root.open(req.Path)
	if err != nil {
		return helperapi.FileContent{}, notFound(err, req.Path)
	}
	defer func() { _ = file.Close() }()
	fi, err := file.Stat()
	if err != nil {
		return helperapi.FileContent{}, err
	}
	switch {
	case fi.IsDir():
		return helperapi.FileContent{}, fileErr(helperapi.CodeFileIsDir, map[string]any{"path": req.Path})
	case !fi.Mode().IsRegular():
		return helperapi.FileContent{}, fileErr(helperapi.CodeFileBinary, map[string]any{"path": req.Path})
	case fi.Size() > helperapi.FileReadMax:
		return helperapi.FileContent{}, fileErr(helperapi.CodeFileTooLarge, map[string]any{"path": req.Path, "max": helperapi.FileReadMax})
	}
	data, err := io.ReadAll(io.LimitReader(file, helperapi.FileReadMax+1))
	if err != nil {
		return helperapi.FileContent{}, err
	}
	if looksBinary(data) {
		return helperapi.FileContent{}, fileErr(helperapi.CodeFileBinary, map[string]any{"path": req.Path})
	}
	return helperapi.FileContent{Entry: describe(req.Path, fi, newNames()), Content: string(data), ETag: etagOf(fi)}, nil
}

// readRegular reads a small regular file through root: not a pipe or
// device (opened non-blocking, so that a pipe cannot hang the request), at
// most limit bytes. A link at its end is followed where it stays inside
// root, as a root does whatever the flags say; nothing leaves root.
func readRegular(root *os.Root, p string, limit int64) ([]byte, error) {
	file, err := root.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	fi, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("%s is larger than %d bytes", p, limit)
	}
	return data, err
}

func randomName(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// adoptDir gives the directory name, just made in the open directory d
// (p, its path), to d's owner, and opens it: the directory itself, by its
// one name, not whatever a path to it names by now.
func adoptDir(d *os.Root, name, p string) (*os.Root, error) {
	uid, gid, err := dirOwner(d)
	if err != nil {
		return nil, err
	}
	sub, err := subDir(d, name)
	if err != nil {
		return nil, fileErr(helperapi.CodeFileIsLink, map[string]any{"path": p})
	}
	if err := sub.Lchown(".", uid, gid); err != nil {
		_ = sub.Close()
		return nil, err
	}
	return sub, nil
}

func (f *Files) write(_ context.Context, _ helperapi.Caller, req helperapi.FileWriteRequest) (helperapi.FileSaved, error) {
	if protected(req.Root, req.Path) {
		return helperapi.FileSaved{}, protectedErr(req.Path)
	}
	root, err := f.openRoot(req.Root)
	if err != nil {
		return helperapi.FileSaved{}, err
	}
	defer func() { _ = root.Close() }()
	// The directory is opened once: the look at the file, the temporary
	// file and the rename over it all happen in it, by single names.
	dir, err := root.dir(parentOf(req.Path), false)
	if err != nil {
		return helperapi.FileSaved{}, err
	}
	defer func() { _ = dir.Close() }()
	name := path.Base(req.Path)
	uid, gid, err := dirOwner(dir)
	if err != nil {
		return helperapi.FileSaved{}, err
	}
	mode := fs.FileMode(defaultFileMod)
	fi, err := dir.Lstat(name)
	switch {
	case err == nil && fi.Mode()&fs.ModeSymlink != 0:
		return helperapi.FileSaved{}, fileErr(helperapi.CodeFileIsLink, map[string]any{"path": req.Path})
	case err == nil && fi.IsDir():
		return helperapi.FileSaved{}, fileErr(helperapi.CodeFileIsDir, map[string]any{"path": req.Path})
	case err == nil && req.ETag == "":
		return helperapi.FileSaved{}, fileErr(helperapi.CodeFileExists, map[string]any{"path": req.Path})
	case err == nil && etagOf(fi) != req.ETag:
		return helperapi.FileSaved{}, fileErr(helperapi.CodeFileChanged, map[string]any{"path": req.Path})
	case err == nil:
		mode = fi.Mode().Perm() | fi.Mode()&(fs.ModeSetgid|fs.ModeSticky)
		uid, gid = fileOwner(fi)
	case !errors.Is(err, fs.ErrNotExist):
		return helperapi.FileSaved{}, notFound(err, req.Path)
	case req.ETag != "":
		return helperapi.FileSaved{}, fileErr(helperapi.CodeFileChanged, map[string]any{"path": req.Path})
	}
	tmp := randomName(".cairn-save-")
	out, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return helperapi.FileSaved{}, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = out.Close()
			_ = dir.Remove(tmp)
		}
	}()
	if _, err := io.WriteString(out, req.Content); err != nil {
		return helperapi.FileSaved{}, err
	}
	if err := out.Sync(); err != nil {
		return helperapi.FileSaved{}, err
	}
	if err := out.Chown(uid, gid); err != nil {
		return helperapi.FileSaved{}, err
	}
	if err := out.Chmod(mode); err != nil {
		return helperapi.FileSaved{}, err
	}
	if err := out.Close(); err != nil {
		return helperapi.FileSaved{}, err
	}
	step("write", req.Path)
	if req.ETag == "" {
		// A new file: one that appeared since the look is not replaced.
		if err := renameAt(dir, tmp, dir, name, true); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return helperapi.FileSaved{}, fileErr(helperapi.CodeFileExists, map[string]any{"path": req.Path})
			}
			return helperapi.FileSaved{}, err
		}
	} else if err := dir.Rename(tmp, name); err != nil {
		return helperapi.FileSaved{}, err
	}
	ok = true
	fi, err = dir.Lstat(name)
	if err != nil {
		return helperapi.FileSaved{}, err
	}
	return helperapi.FileSaved{Entry: entryOf(dir, name, req.Path, fi, newNames()), ETag: etagOf(fi)}, nil
}

// open answers with the file itself, for downloads and previews.
func (f *Files) open(_ context.Context, _ helperapi.Caller, req helperapi.FileRef) (helperapi.FileEntry, *os.File, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return helperapi.FileEntry{}, nil, err
	}
	defer func() { _ = root.Close() }()
	// Non-blocking, so that a pipe someone put there cannot hang it.
	file, err := root.open(req.Path)
	if err != nil {
		return helperapi.FileEntry{}, nil, notFound(err, req.Path)
	}
	fi, err := file.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = file.Close()
		if err == nil && fi.IsDir() {
			return helperapi.FileEntry{}, nil, fileErr(helperapi.CodeFileIsDir, map[string]any{"path": req.Path})
		}
		return helperapi.FileEntry{}, nil, fileErr(helperapi.CodeFileNotFound, map[string]any{"path": req.Path})
	}
	_ = syscall.SetNonblock(int(file.Fd()), false)
	return describe(req.Path, fi, newNames()), file, nil
}

func (f *Files) create(req helperapi.FileNewRequest, dir bool) (helperapi.FileEntry, error) {
	p := join(req.Dir, req.Name)
	if protected(req.Root, p) || (req.Dir == "" && req.Name == trashName) {
		return helperapi.FileEntry{}, protectedErr(p)
	}
	root, err := f.openRoot(req.Root)
	if err != nil {
		return helperapi.FileEntry{}, err
	}
	defer func() { _ = root.Close() }()
	// The directory is opened once; the new entry is made in it and given
	// to its owner itself (an open file, a directory entered by its name).
	d, err := root.dir(req.Dir, false)
	if err != nil {
		return helperapi.FileEntry{}, err
	}
	defer func() { _ = d.Close() }()
	uid, gid, err := dirOwner(d)
	if err != nil {
		return helperapi.FileEntry{}, err
	}
	var file *os.File
	if dir {
		err = d.Mkdir(req.Name, defaultDirMod)
	} else {
		file, err = d.OpenFile(req.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, defaultFileMod)
	}
	if errors.Is(err, fs.ErrExist) {
		return helperapi.FileEntry{}, fileErr(helperapi.CodeFileExists, map[string]any{"path": p})
	}
	if err != nil {
		return helperapi.FileEntry{}, notFound(err, p)
	}
	step("create", p)
	if dir {
		var sub *os.Root
		if sub, err = adoptDir(d, req.Name, p); err == nil {
			err = sub.Close()
		}
	} else {
		err = errors.Join(file.Chown(uid, gid), file.Close())
	}
	if err != nil {
		return helperapi.FileEntry{}, err
	}
	fi, err := d.Lstat(req.Name)
	if err != nil {
		return helperapi.FileEntry{}, err
	}
	return entryOf(d, req.Name, p, fi, newNames()), nil
}

func (f *Files) mkdir(_ context.Context, _ helperapi.Caller, req helperapi.FileNewRequest) (helperapi.FileEntry, error) {
	return f.create(req, true)
}

func (f *Files) touch(_ context.Context, _ helperapi.Caller, req helperapi.FileNewRequest) (helperapi.FileEntry, error) {
	return f.create(req, false)
}

func (f *Files) rename(_ context.Context, _ helperapi.Caller, req helperapi.FileRenameRequest) (helperapi.FileEntry, error) {
	to := join(parentOf(req.Path), req.Name)
	if protected(req.Root, req.Path) || protected(req.Root, to) || (parentOf(req.Path) == "" && req.Name == trashName) {
		return helperapi.FileEntry{}, protectedErr(req.Path)
	}
	root, err := f.openRoot(req.Root)
	if err != nil {
		return helperapi.FileEntry{}, err
	}
	defer func() { _ = root.Close() }()
	// Both names are names in the one directory, opened once.
	dir, name, done, err := root.parent(req.Path)
	if err != nil {
		return helperapi.FileEntry{}, err
	}
	defer done()
	if _, err := dir.Lstat(name); err != nil {
		return helperapi.FileEntry{}, notFound(err, req.Path)
	}
	exists := fileErr(helperapi.CodeFileExists, map[string]any{"path": to})
	if _, err := dir.Lstat(req.Name); err == nil {
		return helperapi.FileEntry{}, exists
	}
	step("rename", req.Path)
	// Never over an entry made there since the look.
	if err := renameAt(dir, name, dir, req.Name, true); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return helperapi.FileEntry{}, exists
		}
		return helperapi.FileEntry{}, notFound(err, req.Path)
	}
	fi, err := dir.Lstat(req.Name)
	if err != nil {
		return helperapi.FileEntry{}, err
	}
	return entryOf(dir, req.Name, to, fi, newNames()), nil
}

// search walks below a directory (links are not followed) for names or
// lines that contain the query.
func (f *Files) search(ctx context.Context, _ helperapi.Caller, req helperapi.FileSearchRequest) (helperapi.FileSearchResult, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return helperapi.FileSearchResult{}, err
	}
	defer func() { _ = root.Close() }()
	out := helperapi.FileSearchResult{Matches: []helperapi.FileMatch{}}
	q := req.Query
	fold := func(s string) string {
		if req.CaseSensitive {
			return s
		}
		return strings.ToLower(s)
	}
	q = fold(q)
	visited := 0
	stop := errors.New("stop")
	add := func(m helperapi.FileMatch) error {
		out.Matches = append(out.Matches, m)
		if len(out.Matches) >= helperapi.FileSearchMax {
			out.Truncated = true
			return stop
		}
		return nil
	}
	// The directory is opened once and walked one level at a time (see
	// walkTree); what cannot be read is skipped. A file is searched by
	// itself, by its name in its directory.
	name := "."
	dir, err := root.dir(req.Path, true)
	done := func() { _ = dir.Close() }
	if errCode, ok := helperapi.AsError(err); ok && errCode.Code == helperapi.CodeFileNotDir {
		dir, name, done, err = root.parent(req.Path)
	}
	if err != nil {
		return out, err
	}
	defer done()
	err = walkNamed(ctx, dir, name, req.Path, true, func(e walkEntry) error {
		if visited++; visited > 200000 {
			out.Truncated = true
			return stop
		}
		rp, mode := e.Path, e.Info.Mode()
		if mode.IsDir() && (rp == trashName || rp == systemTrash) {
			return fs.SkipDir
		}
		if rp != "" && strings.Contains(fold(path.Base(rp)), q) {
			if err := add(helperapi.FileMatch{Path: rp, Type: typeOf(mode)}); err != nil {
				return err
			}
		}
		if !req.Content || !mode.IsRegular() || e.Info.Size() > helperapi.FileSearchBytes {
			return nil
		}
		file, _, err := e.open()
		if err != nil {
			return nil //nolint:nilerr // skipped
		}
		data, err := io.ReadAll(io.LimitReader(file, helperapi.FileSearchBytes+1))
		_ = file.Close()
		if err != nil || len(data) > helperapi.FileSearchBytes || looksBinary(data) {
			return nil //nolint:nilerr // skipped
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(fold(line), q) {
				text := strings.TrimSpace(line)
				if len(text) > 200 {
					text = text[:200]
				}
				if err := add(helperapi.FileMatch{Path: rp, Type: helperapi.FileTypeFile, Line: i + 1, Text: text}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil && ctx.Err() != nil {
		out.Truncated = true
		return out, nil
	}
	if err != nil && !errors.Is(err, stop) {
		return out, notFound(err, req.Path)
	}
	return out, nil
}
