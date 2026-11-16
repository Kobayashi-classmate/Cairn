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

// copyTree copies src to dst (which must not exist); new entries belong to
// the owner of dst's directory. Links are copied as links; devices, pipes
// and sockets are skipped.
func copyTree(ctx context.Context, root *os.Root, src, dst string, uid, gid int, log io.Writer) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	fi, err := root.Lstat(src)
	if err != nil {
		return notFound(err, src)
	}
	switch m := fi.Mode(); {
	case m&fs.ModeSymlink != 0:
		target, err := root.Readlink(src)
		if err != nil {
			return err
		}
		if err := root.Symlink(target, dst); err != nil {
			return err
		}
		return root.Lchown(dst, uid, gid)
	case m.IsDir():
		if err := root.Mkdir(dst, m.Perm()|0o700); err != nil {
			return err
		}
		if err := root.Lchown(dst, uid, gid); err != nil {
			return err
		}
		d, err := root.Open(src)
		if err != nil {
			return err
		}
		entries, err := d.ReadDir(-1)
		_ = d.Close()
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(ctx, root, src+"/"+e.Name(), dst+"/"+e.Name(), uid, gid, log); err != nil {
				return err
			}
		}
		return root.Chmod(dst, m.Perm())
	case m.IsRegular():
		in, _, err := noFollow(root, src)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
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
	_, _ = fmt.Fprintf(log, "  skipped %s (not a file, directory or link)\n", src)
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

// makeRoom makes room at dst: it must not exist unless overwrite.
func makeRoom(root *os.Root, dst string, overwrite bool) error {
	if _, err := root.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if !overwrite {
		return fileErr(helperapi.CodeFileExists, map[string]any{"path": dst})
	}
	return root.RemoveAll(dst)
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
			if err := makeRoom(root, dst, req.Overwrite); err != nil {
				b.failed(p, err)
				continue
			}
			if err := copyTree(ctx, root, p, dst, uid, gid, log); err != nil {
				_ = root.RemoveAll(dst)
				b.failed(p, err)
				continue
			}
			b.done("copied %s to %s", p, dst)
		}
	}
	return b.result()
}

// chownTree gives everything below p (links not followed) to uid:gid.
func chownTree(ctx context.Context, root *os.Root, p string, uid, gid int) error {
	return fs.WalkDir(root.FS(), rel(p), func(q string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return root.Lchown(q, uid, gid)
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
		if err := makeRoom(root, dst, req.Overwrite); err != nil {
			b.failed(p, err)
			continue
		}
		err = root.Rename(p, dst)
		if errors.Is(err, syscall.EXDEV) {
			// Another file system: copy, then remove.
			if err = copyTree(ctx, root, p, dst, uid, gid, log); err == nil {
				err = root.RemoveAll(p)
			} else {
				_ = root.RemoveAll(dst)
			}
		} else if err == nil && (srcUID != uid || srcGID != gid) {
			// Moved to another owner's directory: it becomes theirs.
			err = chownTree(ctx, root, dst, uid, gid)
		}
		if err != nil {
			b.failed(p, notFound(err, p))
			continue
		}
		b.done("moved %s to %s", p, dst)
	}
	return b.result()
}

// Trash -------------------------------------------------------------------

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
		if err := root.Rename(p, box+"/"+path.Base(p)); err != nil {
			_ = root.Remove(box)
			if errors.Is(err, syscall.EXDEV) {
				err = fileErr(helperapi.CodeFileCrossDevice, map[string]any{"path": p})
			}
			b.failed(p, err)
			continue
		}
		meta, _ := json.Marshal(trashMeta{Path: p, Type: typeOf(fi.Mode()), Size: fi.Size(), DeletedAt: time.Now().Unix(), User: c.Username})
		if err := root.WriteFile(box+".json", meta, 0o600); err != nil {
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
		data, err := root.ReadFile(trash + "/" + n)
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
		data, err := root.ReadFile(trash + "/" + id + ".json")
		var m trashMeta
		if err != nil || json.Unmarshal(data, &m) != nil || !helperapi.ValidFilePath(m.Path) || m.Path == "" {
			b.failed(id, fileErr(helperapi.CodeFileNotFound, map[string]any{"id": id}))
			continue
		}
		dir := parentOf(m.Path)
		if dir != "" {
			if err := root.MkdirAll(dir, defaultDirMod); err != nil {
				b.failed(m.Path, err)
				continue
			}
		}
		if err := makeRoom(root, m.Path, req.Overwrite); err != nil {
			b.failed(m.Path, err)
			continue
		}
		if err := root.Rename(trash+"/"+id+"/"+path.Base(m.Path), m.Path); err != nil {
			b.failed(m.Path, notFound(err, m.Path))
			continue
		}
		_ = root.Remove(trash + "/" + id)
		_ = root.Remove(trash + "/" + id + ".json")
		b.done("restored %s", m.Path)
	}
	return b.result()
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
		if err := root.RemoveAll(trash + "/" + id); err != nil {
			b.failed(id, err)
			continue
		}
		_ = root.Remove(trash + "/" + id + ".json")
		b.done("purged %s", id)
	}
	return b.res, nil
}

// Permissions and owners ----------------------------------------------------

// walkChange applies fn to p and, when recursive, to everything below it;
// links are left alone.
func walkChange(ctx context.Context, root *os.Root, p string, recursive bool, fn func(f *os.File, fi fs.FileInfo) error) error {
	apply := func(q string) error {
		file, fi, err := noFollow(root, q)
		if err != nil {
			if he, ok := helperapi.AsError(err); ok && he.Code == helperapi.CodeFileIsLink {
				return nil
			}
			return err
		}
		defer func() { _ = file.Close() }()
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			return nil
		}
		return fn(file, fi)
	}
	if !recursive {
		return apply(p)
	}
	return fs.WalkDir(root.FS(), rel(p), func(q string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		return apply(q)
	})
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
		err := walkChange(ctx, root, p, req.Recursive, func(file *os.File, fi fs.FileInfo) error {
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
		err := walkChange(ctx, root, p, req.Recursive, func(file *os.File, _ fs.FileInfo) error {
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
