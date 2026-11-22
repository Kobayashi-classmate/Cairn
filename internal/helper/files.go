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

// openRoot opens a root for a request; an unknown id is refused.
func (f *Files) openRoot(id string) (*os.Root, error) {
	unavailable := fileErr(helperapi.CodeFileRootUnavailable, map[string]any{"root": id})
	switch id {
	case helperapi.RootSystem:
		if !f.access().FullDisk {
			return nil, unavailable
		}
	case helperapi.RootSites:
	case helperapi.RootBackups:
		return f.backupsRoot()
	default:
		return nil, unavailable
	}
	r, err := os.OpenRoot(f.path(f.rootPath(id)))
	if err != nil {
		return nil, unavailable
	}
	return r, nil
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

func entryOf(root *os.Root, p string, fi fs.FileInfo, n *names) helperapi.FileEntry {
	e := helperapi.FileEntry{Name: fi.Name(), Path: p, Type: typeOf(fi.Mode()), Size: fi.Size(), Mode: unixMode(fi.Mode()),
		ModTime: fi.ModTime().Unix()}
	if p == "" {
		e.Name = ""
	}
	if uid, gid := fileOwner(fi); uid >= 0 {
		e.Owner, e.Group = n.user(uid), n.group(gid)
	}
	if e.Type == helperapi.FileTypeLink {
		e.Target, _ = root.Readlink(rel(p))
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
	fi, err := root.Stat(rel(req.Path))
	if err != nil {
		return helperapi.FileList{}, notFound(err, req.Path)
	}
	if !fi.IsDir() {
		return helperapi.FileList{}, fileErr(helperapi.CodeFileNotDir, map[string]any{"path": req.Path})
	}
	out := helperapi.FileList{Dir: entryOf(root, req.Path, fi, n), Entries: []helperapi.FileEntry{}}
	d, err := root.Open(rel(req.Path))
	if err != nil {
		return out, notFound(err, req.Path)
	}
	defer func() { _ = d.Close() }()
	for {
		batch, err := d.ReadDir(512)
		for _, de := range batch {
			name := de.Name()
			p := join(req.Path, name)
			if (!req.Hidden && strings.HasPrefix(name, ".")) || (req.Path == "" && name == trashName) || p == systemTrash {
				continue
			}
			if len(out.Entries) == helperapi.FileListMax {
				out.Truncated = true
				break
			}
			info, err := de.Info()
			if err != nil {
				continue
			}
			out.Entries = append(out.Entries, entryOf(root, p, info, n))
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
	fi, err := root.Lstat(rel(req.Path))
	if err != nil {
		return helperapi.FileEntry{}, notFound(err, req.Path)
	}
	return entryOf(root, req.Path, fi, newNames()), nil
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
	file, err := root.OpenFile(rel(req.Path), os.O_RDONLY|syscall.O_NONBLOCK, 0)
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
	e := entryOf(root, req.Path, fi, newNames())
	e.Name = path.Base("/" + req.Path)
	return helperapi.FileContent{Entry: e, Content: string(data), ETag: etagOf(fi)}, nil
}

// readRegular reads a small regular file through root: not through a link
// at its end, not a pipe or device (opened non-blocking, so that a pipe
// cannot hang the request), at most limit bytes.
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

// ownerOfDir is who new entries of a directory belong to.
func ownerOfDir(root *os.Root, dir string) (int, int, error) {
	fi, err := root.Lstat(rel(dir))
	if err != nil {
		return 0, 0, notFound(err, dir)
	}
	if !fi.IsDir() {
		return 0, 0, fileErr(helperapi.CodeFileNotDir, map[string]any{"path": dir})
	}
	uid, gid := fileOwner(fi)
	return uid, gid, nil
}

// adopt gives a new entry to its directory's owner.
func adopt(root *os.Root, p string) error {
	uid, gid, err := ownerOfDir(root, parentOf(p))
	if err != nil {
		return err
	}
	return root.Lchown(p, uid, gid)
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
	dir := parentOf(req.Path)
	uid, gid, err := ownerOfDir(root, dir)
	if err != nil {
		return helperapi.FileSaved{}, err
	}
	mode := fs.FileMode(defaultFileMod)
	fi, err := root.Lstat(req.Path)
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
	tmp := join(dir, randomName(".cairn-save-"))
	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return helperapi.FileSaved{}, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = out.Close()
			_ = root.Remove(tmp)
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
	if err := root.Rename(tmp, req.Path); err != nil {
		return helperapi.FileSaved{}, err
	}
	ok = true
	fi, err = root.Lstat(req.Path)
	if err != nil {
		return helperapi.FileSaved{}, err
	}
	return helperapi.FileSaved{Entry: entryOf(root, req.Path, fi, newNames()), ETag: etagOf(fi)}, nil
}

// open answers with the file itself, for downloads and previews.
func (f *Files) open(_ context.Context, _ helperapi.Caller, req helperapi.FileRef) (helperapi.FileEntry, *os.File, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return helperapi.FileEntry{}, nil, err
	}
	defer func() { _ = root.Close() }()
	// Non-blocking, so that a pipe someone put there cannot hang it.
	file, err := root.OpenFile(rel(req.Path), os.O_RDONLY|syscall.O_NONBLOCK, 0)
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
	e := entryOf(root, req.Path, fi, newNames())
	e.Name = path.Base("/" + req.Path)
	return e, file, nil
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
	if _, _, err := ownerOfDir(root, req.Dir); err != nil {
		return helperapi.FileEntry{}, err
	}
	if dir {
		err = root.Mkdir(p, defaultDirMod)
	} else {
		var file *os.File
		if file, err = root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, defaultFileMod); err == nil {
			err = file.Close()
		}
	}
	if errors.Is(err, fs.ErrExist) {
		return helperapi.FileEntry{}, fileErr(helperapi.CodeFileExists, map[string]any{"path": p})
	}
	if err != nil {
		return helperapi.FileEntry{}, notFound(err, p)
	}
	if err := adopt(root, p); err != nil {
		return helperapi.FileEntry{}, err
	}
	fi, err := root.Lstat(p)
	if err != nil {
		return helperapi.FileEntry{}, err
	}
	return entryOf(root, p, fi, newNames()), nil
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
	if _, err := root.Lstat(req.Path); err != nil {
		return helperapi.FileEntry{}, notFound(err, req.Path)
	}
	if _, err := root.Lstat(to); err == nil {
		return helperapi.FileEntry{}, fileErr(helperapi.CodeFileExists, map[string]any{"path": to})
	}
	if err := root.Rename(req.Path, to); err != nil {
		return helperapi.FileEntry{}, notFound(err, req.Path)
	}
	fi, err := root.Lstat(to)
	if err != nil {
		return helperapi.FileEntry{}, err
	}
	return entryOf(root, to, fi, newNames()), nil
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
	err = fs.WalkDir(root.FS(), rel(req.Path), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // unreadable entries are skipped
		}
		if ctx.Err() != nil {
			out.Truncated = true
			return stop
		}
		if visited++; visited > 200000 {
			out.Truncated = true
			return stop
		}
		rp := strings.TrimPrefix(p, "./")
		if rp == "." {
			rp = ""
		}
		if d.IsDir() && (rp == trashName || rp == systemTrash) {
			return fs.SkipDir
		}
		if rp != "" && strings.Contains(fold(d.Name()), q) {
			if err := add(helperapi.FileMatch{Path: rp, Type: typeOf(d.Type())}); err != nil {
				return err
			}
		}
		if !req.Content || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > helperapi.FileSearchBytes {
			return nil //nolint:nilerr // skipped
		}
		data, err := readRegular(root, p, helperapi.FileSearchBytes)
		if err != nil || looksBinary(data) {
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
	if err != nil && !errors.Is(err, stop) {
		return out, notFound(err, req.Path)
	}
	return out, nil
}
