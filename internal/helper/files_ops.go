// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// File operations: each path is handled on its own; one that fails is
// logged and listed, and the others go on.

type batch struct {
	log   io.Writer
	res   helperapi.FileOpResult
	codes []string // of the failures
}

func (b *batch) failed(p string, err error) {
	b.res.Failed = append(b.res.Failed, p)
	msg, code := err.Error(), ""
	if he, ok := helperapi.AsError(err); ok {
		msg, code = he.Code, he.Code
	}
	b.codes = append(b.codes, code)
	_, _ = fmt.Fprintf(b.log, "! %s: %s\n", p, msg)
}

func (b *batch) done(format string, args ...any) {
	b.res.Done++
	_, _ = fmt.Fprintf(b.log, format+"\n", args...)
}

// result is the operation's result; it fails when nothing was done, with
// the failures' own code when they all share one (file.cross_device, say).
func (b *batch) result() (any, error) {
	if b.res.Done > 0 || len(b.res.Failed) == 0 {
		return b.res, nil
	}
	params := map[string]any{"failed": len(b.res.Failed), "path": b.res.Failed[0]}
	for _, c := range b.codes {
		if c == "" || c != b.codes[0] {
			return b.res, fileErr(helperapi.CodeOpFailed, params)
		}
	}
	return b.res, fileErr(b.codes[0], params)
}

// noFollow opens an entry without following a link in its last component,
// so that changes go to the entry itself and nothing it was swapped for.
func noFollow(root *os.Root, p string) (*os.File, fs.FileInfo, error) {
	file, err := root.OpenFile(rel(p), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, nil, fileErr(helperapi.CodeFileIsLink, map[string]any{"path": p})
		}
		return nil, nil, notFound(err, p)
	}
	fi, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, fi, nil
}

// within reports whether p is base or below it.
func within(p, base string) bool { return base == "" || p == base || strings.HasPrefix(p, base+"/") }

// linked reports whether fi is a regular file with more than one name
// (backup files must have none: see openBackup).
func linked(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && fi.Mode().IsRegular() && st.Nlink > 1
}

// copyTree copies src to dst (which must not exist); new entries belong to
// the owner of dst's directory. Links are copied as links; devices, pipes
// and sockets are skipped. Both sides are walked one level at a time (see
// walkTree): a directory swapped for a link meanwhile, in the source or
// in the copy being made, is never followed.
func copyTree(ctx context.Context, root *os.Root, src, dst string, uid, gid int, log io.Writer) error {
	sdir, sname, sdone, err := openParent(root, src)
	if err != nil {
		return notFound(err, src)
	}
	defer sdone()
	ddir, dname, ddone, err := openParent(root, dst)
	if err != nil {
		return notFound(err, dst)
	}
	defer ddone()
	info, err := sdir.Lstat(sname)
	if err != nil {
		return notFound(err, src)
	}
	return copyAt(ctx, sdir, sname, info, ddir, dname, uid, gid, src, log)
}

func copyAt(ctx context.Context, sdir *os.Root, sname string, info fs.FileInfo, ddir *os.Root, dname string, uid, gid int, p string, log io.Writer) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch m := info.Mode(); {
	case m&fs.ModeSymlink != 0:
		target, err := sdir.Readlink(sname)
		if err != nil {
			return err
		}
		if err := ddir.Symlink(target, dname); err != nil {
			return err
		}
		return ddir.Lchown(dname, uid, gid)
	case m.IsDir():
		ssub, err := enterDir(sdir, sname, info)
		if errors.Is(err, errSwapped) {
			_, _ = fmt.Fprintf(log, "  skipped %s (it changed meanwhile)\n", p)
			return nil
		}
		if err != nil {
			return err
		}
		defer func() { _ = ssub.Close() }()
		if err := ddir.Mkdir(dname, m.Perm()|0o700); err != nil {
			return err
		}
		made, err := ddir.Lstat(dname)
		if err != nil {
			return err
		}
		dsub, err := enterDir(ddir, dname, made)
		if err != nil {
			return fileErr(helperapi.CodeFileIsLink, map[string]any{"path": p})
		}
		defer func() { _ = dsub.Close() }()
		if err := dsub.Lchown(".", uid, gid); err != nil {
			return err
		}
		names, err := dirNames(ssub)
		if err != nil {
			return err
		}
		for _, n := range names {
			ci, err := ssub.Lstat(n)
			if err != nil {
				continue // gone meanwhile
			}
			if err := copyAt(ctx, ssub, n, ci, dsub, n, uid, gid, join(p, n), log); err != nil {
				return err
			}
		}
		return dsub.Chmod(".", m.Perm())
	case m.IsRegular():
		in, fi, err := openEntry(sdir, sname, info)
		if errors.Is(err, errSwapped) {
			_, _ = fmt.Fprintf(log, "  skipped %s (it changed meanwhile)\n", p)
			return nil
		}
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := ddir.OpenFile(dname, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, io.LimitReader(in, fi.Size())); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Chown(uid, gid); err != nil {
			_ = out.Close()
			return err
		}
		// Setuid and setgid never come along.
		if err := out.Chmod(m.Perm()); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	}
	_, _ = fmt.Fprintf(log, "  skipped %s (not a file, directory or link)\n", p)
	return nil
}

func (f *Files) transferChecks(req helperapi.FileTransferRequest, root *os.Root) error {
	if protected(req.Root, req.Dest) && req.Dest != "" {
		return protectedErr(req.Dest)
	}
	fi, err := root.Stat(rel(req.Dest))
	if err != nil {
		return notFound(err, req.Dest)
	}
	if !fi.IsDir() {
		return fileErr(helperapi.CodeFileNotDir, map[string]any{"path": req.Dest})
	}
	return nil
}

// makeRoom makes room at dst: it must not exist unless overwrite, and
// then what is there is moved aside (not removed yet, so that a copy or
// move that fails loses nothing). settle(true) removes it once the new
// entry is at dst; settle(false) puts it back, dst being empty again.
func makeRoom(root *os.Root, dst string, overwrite bool) (settle func(placed bool), err error) {
	if _, err := root.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
		return func(bool) {}, nil
	} else if err != nil {
		return nil, err
	}
	if !overwrite {
		return nil, fileErr(helperapi.CodeFileExists, map[string]any{"path": dst})
	}
	aside := join(parentOf(dst), randomName(".cairn-old-"))
	if err := root.Rename(dst, aside); err != nil {
		return nil, err
	}
	return func(placed bool) {
		if placed {
			_ = root.RemoveAll(aside)
			return
		}
		_ = root.Rename(aside, dst)
	}, nil
}

func (f *Files) copyOp(ctx context.Context, _ helperapi.Caller, req helperapi.FileTransferRequest, log io.Writer) (any, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := f.transferChecks(req, root); err != nil {
		return nil, err
	}
	uid, gid, err := ownerOfDir(root, req.Dest)
	if err != nil {
		return nil, err
	}
	b := &batch{log: log}
	for _, p := range req.Paths {
		dst := join(req.Dest, path.Base(p))
		switch {
		case protected(req.Root, p):
			b.failed(p, protectedErr(p))
		case within(req.Dest, p):
			b.failed(p, fileErr(helperapi.CodeFileIntoItself, map[string]any{"path": p}))
		case dst == p:
			b.failed(p, fileErr(helperapi.CodeFileExists, map[string]any{"path": dst}))
		default:
			settle, err := makeRoom(root, dst, req.Overwrite)
			if err != nil {
				b.failed(p, err)
				continue
			}
			if err := copyTree(ctx, root, p, dst, uid, gid, log); err != nil {
				_ = root.RemoveAll(dst)
				settle(false)
				b.failed(p, err)
				continue
			}
			settle(true)
			b.done("copied %s to %s", p, dst)
		}
	}
	return b.result()
}

// chownTree gives p and everything below it (links not followed, p
// included) to uid:gid, one level at a time (see walkTree); files with
// other names that belong to someone else are left alone.
func chownTree(ctx context.Context, root *os.Root, p string, uid, gid int, log io.Writer) error {
	owner := -1
	return walkTree(ctx, root, p, func(e walkEntry) error {
		if e.Rel == "" {
			owner = uidOf(e.Info)
		}
		m := e.Info.Mode()
		if m&fs.ModeSymlink != 0 {
			// The link itself, by its one name: nothing to follow.
			return e.Dir.Lchown(e.Name, uid, gid)
		}
		if !m.IsDir() && !m.IsRegular() {
			return e.Dir.Lchown(e.Name, uid, gid)
		}
		f, fi, err := e.open()
		if errors.Is(err, errSwapped) {
			return nil
		}
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if e.Rel != "" && foreignLink(fi, owner) {
			_, _ = fmt.Fprintf(log, "  skipped %s (a hard link to a file of another owner)\n", e.Path)
			return nil
		}
		return f.Chown(uid, gid)
	})
}

func (f *Files) moveOp(ctx context.Context, _ helperapi.Caller, req helperapi.FileTransferRequest, log io.Writer) (any, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := f.transferChecks(req, root); err != nil {
		return nil, err
	}
	uid, gid, err := ownerOfDir(root, req.Dest)
	if err != nil {
		return nil, err
	}
	b := &batch{log: log}
	for _, p := range req.Paths {
		dst := join(req.Dest, path.Base(p))
		switch {
		case protected(req.Root, p):
			b.failed(p, protectedErr(p))
			continue
		case within(req.Dest, p):
			b.failed(p, fileErr(helperapi.CodeFileIntoItself, map[string]any{"path": p}))
			continue
		case dst == p:
			b.done("%s is already there", p)
			continue
		}
		srcUID, srcGID, err := ownerOfDir(root, parentOf(p))
		if err != nil {
			b.failed(p, err)
			continue
		}
		settle, err := makeRoom(root, dst, req.Overwrite)
		if err != nil {
			b.failed(p, err)
			continue
		}
		placed := false
		err = root.Rename(p, dst)
		if errors.Is(err, syscall.EXDEV) {
			// Another file system: copy, then remove.
			if err = copyTree(ctx, root, p, dst, uid, gid, log); err == nil {
				placed = true
				err = root.RemoveAll(p)
			} else {
				_ = root.RemoveAll(dst)
			}
		} else if err == nil {
			placed = true
			if srcUID != uid || srcGID != gid {
				// Moved to another owner's directory: it becomes theirs.
				err = chownTree(ctx, root, dst, uid, gid, log)
			}
		}
		settle(placed)
		if err != nil {
			b.failed(p, notFound(err, p))
			continue
		}
		b.done("moved %s to %s", p, dst)
	}
	return b.result()
}

// Trash -------------------------------------------------------------------

// trashMetaMax bounds a trash entry's description (a path and a few
// fields).
const trashMetaMax = 64 << 10

type trashMeta struct {
	Path      string `json:"path"`
	Type      string `json:"type"`
	Size      int64  `json:"size"`
	DeletedAt int64  `json:"deletedAt"`
	User      string `json:"user,omitempty"`
}

func (f *Files) deleteOp(ctx context.Context, c helperapi.Caller, req helperapi.FileDeleteRequest, log io.Writer) (any, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	trash := trashOf(req.Root)
	if !req.Permanent {
		if err := root.MkdirAll(trash, 0o700); err != nil {
			return nil, err
		}
		_ = root.Chmod(trash, 0o700)
	}
	b := &batch{log: log}
	for _, p := range req.Paths {
		if ctx.Err() != nil {
			return b.res, ctx.Err()
		}
		if protected(req.Root, p) {
			b.failed(p, protectedErr(p))
			continue
		}
		fi, err := root.Lstat(p)
		if err != nil {
			b.failed(p, notFound(err, p))
			continue
		}
		if req.Permanent {
			if err := root.RemoveAll(p); err != nil {
				b.failed(p, err)
				continue
			}
			b.done("deleted %s", p)
			continue
		}
		id := randomName("")
		box := trash + "/" + id
		if err := root.Mkdir(box, 0o700); err != nil {
			b.failed(p, err)
			continue
		}
		inBox := box + "/" + path.Base(p)
		if err := root.Rename(p, inBox); err != nil {
			_ = root.Remove(box)
			if errors.Is(err, syscall.EXDEV) {
				err = fileErr(helperapi.CodeFileCrossDevice, map[string]any{"path": p})
			}
			b.failed(p, err)
			continue
		}
		meta, _ := json.Marshal(trashMeta{Path: p, Type: typeOf(fi.Mode()), Size: fi.Size(), DeletedAt: time.Now().Unix(), User: c.Username})
		if err := root.WriteFile(box+".json", meta, 0o600); err != nil {
			// Without its description the entry could never be listed,
			// restored or purged: put it back.
			_ = root.Remove(box + ".json")
			if rerr := root.Rename(inBox, p); rerr == nil {
				_ = root.Remove(box)
			}
			b.failed(p, err)
			continue
		}
		b.done("moved %s to the trash", p)
	}
	return b.result()
}

func (f *Files) trashEntries(root *os.Root, trash string) ([]helperapi.TrashEntry, error) {
	d, err := root.Open(trash)
	if errors.Is(err, fs.ErrNotExist) {
		return []helperapi.TrashEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	out := []helperapi.TrashEntry{}
	for _, n := range names {
		id, ok := strings.CutSuffix(n, ".json")
		if !ok || len(id) != 16 {
			continue
		}
		data, err := readRegular(root, trash+"/"+n, trashMetaMax)
		var m trashMeta
		if err != nil || json.Unmarshal(data, &m) != nil {
			continue
		}
		out = append(out, helperapi.TrashEntry{ID: id, Path: m.Path, Type: m.Type, Size: m.Size, DeletedAt: m.DeletedAt, User: m.User})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeletedAt > out[j].DeletedAt })
	return out, nil
}

func (f *Files) trashList(_ context.Context, _ helperapi.Caller, req helperapi.FileRootRef) (helperapi.TrashList, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return helperapi.TrashList{}, err
	}
	defer func() { _ = root.Close() }()
	entries, err := f.trashEntries(root, trashOf(req.Root))
	return helperapi.TrashList{Entries: entries}, err
}

func (f *Files) restoreOp(_ context.Context, _ helperapi.Caller, req helperapi.TrashRequest, log io.Writer) (any, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	trash := trashOf(req.Root)
	b := &batch{log: log}
	for _, id := range req.IDs {
		data, err := readRegular(root, trash+"/"+id+".json", trashMetaMax)
		var m trashMeta
		if err != nil || json.Unmarshal(data, &m) != nil || !helperapi.ValidFilePath(m.Path) || m.Path == "" {
			b.failed(id, fileErr(helperapi.CodeFileNotFound, map[string]any{"id": id}))
			continue
		}
		if err := mkdirAdopted(root, parentOf(m.Path)); err != nil {
			b.failed(m.Path, err)
			continue
		}
		settle, err := makeRoom(root, m.Path, req.Overwrite)
		if err != nil {
			b.failed(m.Path, err)
			continue
		}
		if err := root.Rename(trash+"/"+id+"/"+path.Base(m.Path), m.Path); err != nil {
			settle(false)
			b.failed(m.Path, notFound(err, m.Path))
			continue
		}
		settle(true)
		_ = root.Remove(trash + "/" + id)
		_ = root.Remove(trash + "/" + id + ".json")
		b.done("restored %s", m.Path)
	}
	return b.result()
}

// mkdirAdopted makes dir and its missing parents as file.mkdir would:
// each new directory belongs to the owner of the one it is made in. A
// link in the way is refused, never followed.
func mkdirAdopted(root *os.Root, dir string) error {
	if dir == "" {
		return nil
	}
	if err := mkdirAdopted(root, parentOf(dir)); err != nil {
		return err
	}
	fi, err := root.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := root.Mkdir(dir, defaultDirMod); err != nil {
			return err
		}
		return adopt(root, dir)
	case err != nil:
		return notFound(err, dir)
	case !fi.IsDir():
		return fileErr(helperapi.CodeFileNotDir, map[string]any{"path": dir})
	}
	return nil
}

func (f *Files) purgeOp(ctx context.Context, _ helperapi.Caller, req helperapi.TrashPurgeRequest, log io.Writer) (any, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	trash := trashOf(req.Root)
	ids := req.IDs
	if len(ids) == 0 {
		entries, err := f.trashEntries(root, trash)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.DeletedAt < req.OlderThan {
				ids = append(ids, e.ID)
			}
		}
	}
	b := &batch{log: log}
	for _, id := range ids {
		if ctx.Err() != nil {
			return b.res, ctx.Err()
		}
		_, derr := root.Lstat(trash + "/" + id)
		_, merr := root.Lstat(trash + "/" + id + ".json")
		if errors.Is(derr, fs.ErrNotExist) && errors.Is(merr, fs.ErrNotExist) {
			b.failed(id, fileErr(helperapi.CodeFileNotFound, map[string]any{"id": id}))
			continue
		}
		if err := root.RemoveAll(trash + "/" + id); err != nil {
			b.failed(id, err)
			continue
		}
		if err := root.Remove(trash + "/" + id + ".json"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			b.failed(id, err)
			continue
		}
		b.done("purged %s", id)
	}
	return b.result()
}

// Permissions and owners ----------------------------------------------------

// walkChange applies fn to p and, when recursive, to everything below it,
// one level at a time (see walkTree): links are left alone (a link at p is
// not walked either), a directory that changes meanwhile is not entered,
// and files below p with other names that belong to someone else (hard
// links planted in the tree) are skipped and logged.
func walkChange(ctx context.Context, root *os.Root, p string, recursive bool, log io.Writer, fn func(f *os.File, fi fs.FileInfo) error) error {
	owner := -1
	apply := func(e walkEntry) error {
		m := e.Info.Mode()
		if m&fs.ModeSymlink != 0 || !m.IsDir() && !m.IsRegular() {
			return nil
		}
		file, fi, err := e.open()
		if errors.Is(err, errSwapped) {
			return fs.SkipDir // whatever it is now is not walked
		}
		if err != nil {
			return notFound(err, e.Path)
		}
		defer func() { _ = file.Close() }()
		if e.Rel == "" {
			owner = uidOf(fi)
		} else if foreignLink(fi, owner) {
			_, _ = fmt.Fprintf(log, "  skipped %s (a hard link to a file of another owner)\n", e.Path)
			return nil
		}
		return fn(file, fi)
	}
	if !recursive {
		dir, name, done, err := openParent(root, p)
		if err != nil {
			return notFound(err, p)
		}
		defer done()
		info, err := dir.Lstat(name)
		if err != nil {
			return notFound(err, p)
		}
		if err := apply(walkEntry{Dir: dir, Name: name, Path: p, Info: info}); !errors.Is(err, fs.SkipDir) {
			return err
		}
		return nil
	}
	return walkTree(ctx, root, p, apply)
}

func (f *Files) chmodOp(ctx context.Context, _ helperapi.Caller, req helperapi.FileChmodRequest, log io.Writer) (any, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	dirMode := req.DirMode
	if dirMode == 0 {
		dirMode = req.Mode
	}
	b := &batch{log: log}
	for _, p := range req.Paths {
		if protected(req.Root, p) {
			b.failed(p, protectedErr(p))
			continue
		}
		err := walkChange(ctx, root, p, req.Recursive, log, func(file *os.File, fi fs.FileInfo) error {
			if fi.IsDir() {
				return file.Chmod(goMode(dirMode & helperapi.DirModeMask))
			}
			return file.Chmod(goMode(req.Mode & helperapi.FileModeMask))
		})
		if err != nil {
			b.failed(p, err)
			continue
		}
		b.done("changed the permissions of %s", p)
	}
	return b.result()
}

func (f *Files) chownOp(ctx context.Context, _ helperapi.Caller, req helperapi.FileChownRequest, log io.Writer) (any, error) {
	uid, gid := -1, -1
	if req.User != "" {
		u, err := user.Lookup(req.User)
		if err != nil {
			return nil, fileErr(helperapi.CodeFileUserUnknown, map[string]any{"user": req.User})
		}
		uid, _ = strconv.Atoi(u.Uid)
	}
	if req.Group != "" {
		g, err := user.LookupGroup(req.Group)
		if err != nil {
			return nil, fileErr(helperapi.CodeFileUserUnknown, map[string]any{"group": req.Group})
		}
		gid, _ = strconv.Atoi(g.Gid)
	}
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	b := &batch{log: log}
	for _, p := range req.Paths {
		if protected(req.Root, p) {
			b.failed(p, protectedErr(p))
			continue
		}
		err := walkChange(ctx, root, p, req.Recursive, log, func(file *os.File, _ fs.FileInfo) error {
			return file.Chown(uid, gid)
		})
		if err != nil {
			b.failed(p, err)
			continue
		}
		b.done("changed the owner of %s", p)
	}
	return b.result()
}

// Uploads ---------------------------------------------------------------

// placeOp moves a finished upload into place: renamed when the panel's
// directory is on the same file system, copied otherwise.
func (f *Files) placeOp(_ context.Context, _ helperapi.Caller, req helperapi.FilePlaceRequest, log io.Writer) (any, error) {
	p := join(req.Dir, req.Name)
	if protected(req.Root, p) || (req.Dir == "" && req.Name == trashName) {
		return nil, protectedErr(p)
	}
	data, err := os.OpenRoot(f.path(f.DataDir))
	if err != nil {
		return nil, err
	}
	defer func() { _ = data.Close() }()
	srcName := req.Upload + ".part"
	srcDir, err := data.Open("uploads")
	if err != nil {
		return nil, fileErr(helperapi.CodeFileNotFound, map[string]any{"upload": req.Upload})
	}
	defer func() { _ = srcDir.Close() }()
	if fi, err := data.Lstat("uploads/" + srcName); err != nil || !fi.Mode().IsRegular() {
		return nil, fileErr(helperapi.CodeFileNotFound, map[string]any{"upload": req.Upload})
	}
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	uid, gid, err := ownerOfDir(root, req.Dir)
	if err != nil {
		return nil, err
	}
	if fi, err := root.Lstat(p); err == nil {
		if !req.Overwrite {
			return nil, fileErr(helperapi.CodeFileExists, map[string]any{"path": p})
		}
		if fi.IsDir() {
			return nil, fileErr(helperapi.CodeFileIsDir, map[string]any{"path": p})
		}
	}
	dstDir, err := root.Open(rel(req.Dir))
	if err != nil {
		return nil, notFound(err, req.Dir)
	}
	defer func() { _ = dstDir.Close() }()
	tmp := randomName(".cairn-upload-")
	// renameat on directory descriptors: plain names, no path to follow.
	err = unix.Renameat(int(srcDir.Fd()), srcName, int(dstDir.Fd()), tmp)
	if errors.Is(err, syscall.EXDEV) {
		_, _ = fmt.Fprintln(log, "the upload is on another file system: copying")
		err = copyUpload(data, "uploads/"+srcName, root, join(req.Dir, tmp))
	}
	if err != nil {
		return nil, err
	}
	tmpPath := join(req.Dir, tmp)
	if err := root.Lchown(tmpPath, uid, gid); err != nil {
		_ = root.Remove(tmpPath)
		return nil, err
	}
	if err := root.Chmod(tmpPath, defaultFileMod); err != nil {
		_ = root.Remove(tmpPath)
		return nil, err
	}
	if err := root.Rename(tmpPath, p); err != nil {
		_ = root.Remove(tmpPath)
		return nil, err
	}
	fi, err := root.Lstat(p)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "placed %s (%d bytes)\n", p, fi.Size())
	return entryOf(root, p, fi, newNames()), nil
}

func copyUpload(src *os.Root, from string, dst *os.Root, to string) error {
	in, err := src.OpenFile(from, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := dst.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = dst.Remove(to)
		return err
	}
	if err := out.Close(); err != nil {
		_ = dst.Remove(to)
		return err
	}
	return src.Remove(from)
}
