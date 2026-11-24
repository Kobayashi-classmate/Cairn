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

// noFollow opens the entry name of the open directory dir (p, its path)
// without following a link there, so that what is read is the entry itself
// and nothing it was swapped for. A root follows a link whatever the
// flags say: the entry is looked at first, and what was opened must be
// what was looked at (see openEntry).
func noFollow(dir *os.Root, name, p string) (*os.File, fs.FileInfo, error) {
	isLink := fileErr(helperapi.CodeFileIsLink, map[string]any{"path": p})
	info, err := dir.Lstat(name)
	if err != nil {
		return nil, nil, notFound(err, p)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, isLink
	}
	file, fi, err := openEntry(dir, name, info)
	if errors.Is(err, errSwapped) {
		return nil, nil, isLink
	}
	return file, fi, err
}

// within reports whether p is base or below it.
func within(p, base string) bool { return base == "" || p == base || strings.HasPrefix(p, base+"/") }

// linked reports whether fi is a regular file with more than one name
// (backup files must have none: see openBackup).
func linked(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && fi.Mode().IsRegular() && st.Nlink > 1
}

// copyAt copies the entry sname of sdir (info, its Lstat; p, its path) to
// dname of ddir, which must not exist; new entries belong to uid:gid, the
// owner of the directory copied into. Links are copied as links; devices,
// pipes and sockets are skipped. Both sides are open directories, walked
// one level at a time (see walkTree): a directory swapped for a link
// meanwhile, in the source or in the copy being made, is never followed.
// It returns what it made at dname (nil when nothing), also when it fails
// halfway: see unmake.
func copyAt(ctx context.Context, sdir *os.Root, sname string, info fs.FileInfo, ddir *os.Root, dname string, uid, gid int, p string, log io.Writer) (fs.FileInfo, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	switch m := info.Mode(); {
	case m&fs.ModeSymlink != 0:
		target, err := sdir.Readlink(sname)
		if err != nil {
			return nil, err
		}
		if err := ddir.Symlink(target, dname); err != nil {
			return nil, err
		}
		made, _ := ddir.Lstat(dname)
		return made, ddir.Lchown(dname, uid, gid)
	case m.IsDir():
		ssub, err := enterDir(sdir, sname, info)
		if errors.Is(err, errSwapped) {
			_, _ = fmt.Fprintf(log, "  skipped %s (it changed meanwhile)\n", p)
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		defer func() { _ = ssub.Close() }()
		if err := ddir.Mkdir(dname, m.Perm()|0o700); err != nil {
			return nil, err
		}
		made, err := ddir.Lstat(dname)
		if err != nil {
			return nil, err
		}
		dsub, err := enterDir(ddir, dname, made)
		if err != nil {
			return made, fileErr(helperapi.CodeFileIsLink, map[string]any{"path": p})
		}
		defer func() { _ = dsub.Close() }()
		if err := dsub.Lchown(".", uid, gid); err != nil {
			return made, err
		}
		names, err := dirNames(ssub)
		if err != nil {
			return made, err
		}
		for _, n := range names {
			ci, err := ssub.Lstat(n)
			if err != nil {
				continue // gone meanwhile
			}
			if _, err := copyAt(ctx, ssub, n, ci, dsub, n, uid, gid, join(p, n), log); err != nil {
				return made, err
			}
		}
		return made, dsub.Chmod(".", m.Perm())
	case m.IsRegular():
		in, fi, err := openEntry(sdir, sname, info)
		if errors.Is(err, errSwapped) {
			_, _ = fmt.Fprintf(log, "  skipped %s (it changed meanwhile)\n", p)
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		defer func() { _ = in.Close() }()
		out, err := ddir.OpenFile(dname, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		made, err := out.Stat()
		if err == nil {
			_, err = io.Copy(out, io.LimitReader(in, fi.Size()))
		}
		if err == nil {
			err = out.Chown(uid, gid)
		}
		if err == nil {
			// Setuid and setgid never come along.
			err = out.Chmod(m.Perm())
		}
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return made, err
	}
	_, _ = fmt.Fprintf(log, "  skipped %s (not a file, directory or link)\n", p)
	return nil, nil
}

// unmake removes what a copy that failed made at name of the open
// directory dir, and only that: an entry that took its place meanwhile is
// someone else's and stays.
func unmake(dir *os.Root, name string, made fs.FileInfo) error {
	if made == nil {
		return nil
	}
	now, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(made, now) {
		return nil // not the copy's any more
	}
	if !made.IsDir() {
		return dir.Remove(name)
	}
	sub, err := enter(dir, name, made)
	if errors.Is(err, errSwapped) {
		return nil // not the copy's any more
	}
	if err != nil {
		return err
	}
	names, err := dirNames(sub)
	for _, n := range names {
		err = errors.Join(err, sub.RemoveAll(n))
	}
	_ = sub.Close()
	return errors.Join(err, dir.Remove(name))
}

// transferDest opens the directory a copy or move puts its entries into,
// once, and finds its owner: every entry goes into that open directory.
func transferDest(root *fileRoot, req helperapi.FileTransferRequest) (*os.Root, int, int, error) {
	if protected(req.Root, req.Dest) && req.Dest != "" {
		return nil, 0, 0, protectedErr(req.Dest)
	}
	dest, err := root.dir(req.Dest, false)
	if err != nil {
		return nil, 0, 0, err
	}
	uid, gid, err := dirOwner(dest)
	if err != nil {
		_ = dest.Close()
		return nil, 0, 0, err
	}
	return dest, uid, gid, nil
}

// makeRoom makes room for name in the open directory dir (p, its path): it
// must not exist unless overwrite, and then what is there is moved out of
// the way (not removed yet, so that a copy or move that fails loses
// nothing). A directory goes to the trash, where it stays once it is
// replaced: replacing never removes a tree for good. Anything else is
// moved aside in dir and removed once replaced. settle(true) is called
// when the new entry is at name; settle(false) puts back what was there,
// name being free again. What settle cannot do is logged.
func makeRoom(dir *os.Root, name, p string, overwrite bool, bin *trashBin, log io.Writer) (settle func(placed bool), err error) {
	fi, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return func(bool) {}, nil
	} else if err != nil {
		return nil, err
	}
	if !overwrite {
		return nil, fileErr(helperapi.CodeFileExists, map[string]any{"path": p})
	}
	if fi.IsDir() {
		id, err := bin.take(dir, name, p, fi)
		if err != nil {
			return nil, err
		}
		return func(placed bool) {
			if placed {
				_, _ = fmt.Fprintf(log, "  the directory that was at %s is in the trash\n", p)
			} else if err := bin.putBack(id, dir, name); err != nil {
				_, _ = fmt.Fprintf(log, "! %s: the directory that was there stays in the trash: %v\n", p, err)
			}
		}, nil
	}
	aside := randomName(".cairn-old-")
	if err := dir.Rename(name, aside); err != nil {
		return nil, err
	}
	return func(placed bool) {
		var err error
		if placed {
			// One entry, not a tree: a directory swapped in meanwhile stays.
			err = dir.Remove(aside)
		} else {
			err = renameAt(dir, aside, dir, name, true)
		}
		if err != nil {
			_, _ = fmt.Fprintf(log, "! %s: what was there is left as %s: %v\n", p, aside, err)
		}
	}, nil
}

func (f *Files) copyOp(ctx context.Context, c helperapi.Caller, req helperapi.FileTransferRequest, log io.Writer) (any, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	dest, uid, gid, err := transferDest(root, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dest.Close() }()
	bin := &trashBin{root: root, user: c.Username}
	defer bin.close()
	b := &batch{log: log}
	for _, p := range req.Paths {
		name := path.Base(p)
		dst := join(req.Dest, name)
		switch {
		case protected(req.Root, p):
			b.failed(p, protectedErr(p))
		case protected(req.Root, dst):
			b.failed(p, protectedErr(dst))
		case within(req.Dest, p):
			b.failed(p, fileErr(helperapi.CodeFileIntoItself, map[string]any{"path": p}))
		case dst == p:
			b.failed(p, fileErr(helperapi.CodeFileExists, map[string]any{"path": dst}))
		default:
			err := func() error {
				// The source's directory is opened once, too.
				sdir, sname, done, err := root.parent(p)
				if err != nil {
					return err
				}
				defer done()
				settle, err := makeRoom(dest, name, dst, req.Overwrite, bin, log)
				if err != nil {
					return err
				}
				// Looked at once room is made: an entry that is its own
				// destination (by another path to it) is not here any
				// more, and is put back.
				info, err := sdir.Lstat(sname)
				if err != nil {
					settle(false)
					return notFound(err, p)
				}
				made, err := copyAt(ctx, sdir, sname, info, dest, name, uid, gid, p, log)
				if err != nil {
					if rerr := unmake(dest, name, made); rerr != nil {
						_, _ = fmt.Fprintf(log, "! %s: the unfinished copy is left there: %v\n", dst, rerr)
					}
				}
				// What was there goes only once something took its place.
				settle(err == nil && made != nil)
				return err
			}()
			if err != nil {
				b.failed(p, err)
				continue
			}
			b.done("copied %s to %s", p, dst)
		}
	}
	return b.result()
}

// chownTree gives the entry name of the open directory dir (p, its path)
// and everything below it (links not followed) to uid:gid, one level at a
// time (see walkTree); files with other names that belong to someone else
// are left alone.
func chownTree(ctx context.Context, dir *os.Root, name, p string, uid, gid int, log io.Writer) error {
	owner := -1
	return walkNamed(ctx, dir, name, p, false, func(e walkEntry) error {
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

func (f *Files) moveOp(ctx context.Context, c helperapi.Caller, req helperapi.FileTransferRequest, log io.Writer) (any, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	dest, uid, gid, err := transferDest(root, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dest.Close() }()
	bin := &trashBin{root: root, user: c.Username}
	defer bin.close()
	b := &batch{log: log}
	for _, p := range req.Paths {
		name := path.Base(p)
		dst := join(req.Dest, name)
		switch {
		case protected(req.Root, p):
			b.failed(p, protectedErr(p))
			continue
		case protected(req.Root, dst):
			b.failed(p, protectedErr(dst))
			continue
		case within(req.Dest, p):
			b.failed(p, fileErr(helperapi.CodeFileIntoItself, map[string]any{"path": p}))
			continue
		case dst == p:
			b.done("%s is already there", p)
			continue
		}
		err := func() error {
			// The source's directory is opened once, too: the entry moves
			// from one open directory to the other by its names in them.
			sdir, sname, done, err := root.parent(p)
			if err != nil {
				return err
			}
			defer done()
			info, err := sdir.Lstat(sname)
			if err != nil {
				return notFound(err, p)
			}
			srcUID, srcGID, err := dirOwner(sdir)
			if err != nil {
				return err
			}
			settle, err := makeRoom(dest, name, dst, req.Overwrite, bin, log)
			if err != nil {
				return err
			}
			// Room was made: never over an entry put there since.
			err = renameAt(sdir, sname, dest, name, true)
			switch {
			case errors.Is(err, syscall.EXDEV):
				// Another file system: copy, then remove.
				var made fs.FileInfo
				if made, err = copyAt(ctx, sdir, sname, info, dest, name, uid, gid, p, log); err != nil {
					if rerr := unmake(dest, name, made); rerr != nil {
						_, _ = fmt.Fprintf(log, "! %s: the unfinished copy is left there: %v\n", dst, rerr)
					}
					settle(false)
					return err
				}
				if made == nil {
					// Skipped: nothing took the place of what was there,
					// and the entry stays where it is.
					settle(false)
					return fmt.Errorf("%s was not copied to the other file system", p)
				}
				settle(true)
				return sdir.RemoveAll(sname)
			case errors.Is(err, fs.ErrExist):
				settle(false)
				return fileErr(helperapi.CodeFileExists, map[string]any{"path": dst})
			case err != nil:
				settle(false)
				return notFound(err, p)
			}
			settle(true)
			if srcUID != uid || srcGID != gid {
				// Moved to another owner's directory: it becomes theirs.
				return chownTree(ctx, dest, name, dst, uid, gid, log)
			}
			return nil
		}()
		if err != nil {
			b.failed(p, err)
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

// trashBin is a root's trash, opened (and made) when first needed and
// held open from then on: entries go into it by descriptor.
type trashBin struct {
	root *fileRoot
	user string
	dir  *os.Root
}

func (b *trashBin) open() (*os.Root, error) {
	if b.dir == nil {
		d, err := b.root.trash(true)
		if err != nil {
			return nil, err
		}
		b.dir = d
	}
	return b.dir, nil
}

func (b *trashBin) close() {
	if b.dir != nil {
		_ = b.dir.Close()
	}
}

// take moves the entry name of the open directory dir (p, its path; fi,
// its Lstat) into the trash and returns the trash entry's id. The entry's
// description is written first: without one it could never be listed,
// restored or purged.
func (b *trashBin) take(dir *os.Root, name, p string, fi fs.FileInfo) (string, error) {
	trash, err := b.open()
	if err != nil {
		return "", err
	}
	id := randomName("")
	if err := trash.Mkdir(id, 0o700); err != nil {
		return "", err
	}
	drop := func() {
		_ = trash.Remove(id + ".json")
		_ = trash.Remove(id)
	}
	box, err := subDir(trash, id)
	if err != nil {
		drop()
		return "", err
	}
	defer func() { _ = box.Close() }()
	meta, _ := json.Marshal(trashMeta{Path: p, Type: typeOf(fi.Mode()), Size: fi.Size(), DeletedAt: time.Now().Unix(), User: b.user})
	if err := trash.WriteFile(id+".json", meta, 0o600); err != nil {
		drop()
		return "", err
	}
	if err := renameAt(dir, name, box, name, true); err != nil {
		drop()
		if errors.Is(err, syscall.EXDEV) {
			err = fileErr(helperapi.CodeFileCrossDevice, map[string]any{"path": p})
		}
		return "", err
	}
	return id, nil
}

// putBack undoes take: the entry returns to name of dir, which must be
// free, and its trash entry is gone.
func (b *trashBin) putBack(id string, dir *os.Root, name string) error {
	trash, err := b.open()
	if err != nil {
		return err
	}
	box, err := subDir(trash, id)
	if err != nil {
		return err
	}
	defer func() { _ = box.Close() }()
	if err := renameAt(box, name, dir, name, true); err != nil {
		return err
	}
	_ = trash.Remove(id + ".json")
	_ = trash.Remove(id)
	return nil
}

func (f *Files) deleteOp(ctx context.Context, c helperapi.Caller, req helperapi.FileDeleteRequest, log io.Writer) (any, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	bin := &trashBin{root: root, user: c.Username}
	defer bin.close()
	if !req.Permanent {
		if _, err := bin.open(); err != nil {
			return nil, err
		}
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
		err := func() error {
			// The entry's directory is opened once: the entry looked at
			// is the one removed, or moved to the trash, by its name there.
			dir, name, done, err := root.parent(p)
			if err != nil {
				return err
			}
			defer done()
			fi, err := dir.Lstat(name)
			if err != nil {
				return notFound(err, p)
			}
			step("delete", p)
			if req.Permanent {
				return dir.RemoveAll(name)
			}
			_, err = bin.take(dir, name, p, fi)
			return err
		}()
		if err != nil {
			b.failed(p, err)
			continue
		}
		if req.Permanent {
			b.done("deleted %s", p)
		} else {
			b.done("moved %s to the trash", p)
		}
	}
	return b.result()
}

// trashEntries lists the entries of an open trash.
func trashEntries(trash *os.Root) ([]helperapi.TrashEntry, error) {
	names, err := dirNames(trash)
	if err != nil {
		return nil, err
	}
	out := []helperapi.TrashEntry{}
	for _, n := range names {
		id, ok := strings.CutSuffix(n, ".json")
		if !ok || len(id) != 16 {
			continue
		}
		data, err := readRegular(trash, n, trashMetaMax)
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
	trash, err := root.trash(false)
	if errors.Is(err, fs.ErrNotExist) {
		return helperapi.TrashList{Entries: []helperapi.TrashEntry{}}, nil
	}
	if err != nil {
		return helperapi.TrashList{}, err
	}
	defer func() { _ = trash.Close() }()
	entries, err := trashEntries(trash)
	return helperapi.TrashList{Entries: entries}, err
}

func (f *Files) restoreOp(_ context.Context, c helperapi.Caller, req helperapi.TrashRequest, log io.Writer) (any, error) {
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	trash, err := root.trash(false)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	bin := &trashBin{root: root, user: c.Username, dir: trash}
	defer bin.close()
	b := &batch{log: log}
	for _, id := range req.IDs {
		var m trashMeta
		if trash != nil {
			data, err := readRegular(trash, id+".json", trashMetaMax)
			if err != nil || json.Unmarshal(data, &m) != nil || !helperapi.ValidFilePath(m.Path) {
				m.Path = ""
			}
		}
		if m.Path == "" {
			b.failed(id, fileErr(helperapi.CodeFileNotFound, map[string]any{"id": id}))
			continue
		}
		// A description is only as good as the trash it is in: it puts
		// nothing where a request could not.
		if protected(req.Root, m.Path) {
			b.failed(m.Path, protectedErr(m.Path))
			continue
		}
		err := func() error {
			name := path.Base(m.Path)
			box, err := subDir(trash, id)
			if err != nil {
				return notFound(err, m.Path)
			}
			defer func() { _ = box.Close() }()
			// Where it was, opened once (its missing parents made again).
			dir, err := root.mkdirs(parentOf(m.Path))
			if err != nil {
				return err
			}
			defer func() { _ = dir.Close() }()
			settle, err := makeRoom(dir, name, m.Path, req.Overwrite, bin, log)
			if err != nil {
				return err
			}
			if err := renameAt(box, name, dir, name, true); err != nil {
				settle(false)
				switch {
				case errors.Is(err, syscall.EXDEV):
					return fileErr(helperapi.CodeFileCrossDevice, map[string]any{"path": m.Path})
				case errors.Is(err, fs.ErrExist):
					return fileErr(helperapi.CodeFileExists, map[string]any{"path": m.Path})
				}
				return notFound(err, m.Path)
			}
			settle(true)
			return nil
		}()
		if err != nil {
			b.failed(m.Path, err)
			continue
		}
		_ = trash.Remove(id)
		_ = trash.Remove(id + ".json")
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
	trash, err := root.trash(false)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if trash != nil {
		defer func() { _ = trash.Close() }()
	}
	ids := req.IDs
	if len(ids) == 0 && trash != nil {
		entries, err := trashEntries(trash)
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
		missing := trash == nil
		if !missing {
			_, derr := trash.Lstat(id)
			_, merr := trash.Lstat(id + ".json")
			missing = errors.Is(derr, fs.ErrNotExist) && errors.Is(merr, fs.ErrNotExist)
		}
		if missing {
			b.failed(id, fileErr(helperapi.CodeFileNotFound, map[string]any{"id": id}))
			continue
		}
		if err := trash.RemoveAll(id); err != nil {
			b.failed(id, err)
			continue
		}
		if err := trash.Remove(id + ".json"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			b.failed(id, err)
			continue
		}
		b.done("purged %s", id)
	}
	return b.result()
}

// Permissions and owners ----------------------------------------------------

// walkChange applies fn to the entry name of the open directory dir (p,
// its path) and, when recursive, to everything below it, one level at a
// time (see walkTree): links are left alone (a link at p is not walked
// either), a directory that changes meanwhile is not entered, and files
// below p with other names that belong to someone else (hard links
// planted in the tree) are skipped and logged.
func walkChange(ctx context.Context, dir *os.Root, name, p string, recursive bool, log io.Writer, fn func(f *os.File, fi fs.FileInfo) error) error {
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
		info, err := dir.Lstat(name)
		if err != nil {
			return notFound(err, p)
		}
		if err := apply(walkEntry{Dir: dir, Name: name, Path: p, Info: info}); !errors.Is(err, fs.SkipDir) {
			return err
		}
		return nil
	}
	return walkNamed(ctx, dir, name, p, false, apply)
}

// changeEach runs change for every path of a request, each through its
// directory, opened once.
func changeEach(ctx context.Context, root *fileRoot, paths []string, recursive bool, b *batch, what string, fn func(f *os.File, fi fs.FileInfo) error) {
	for _, p := range paths {
		if protected(root.id, p) {
			b.failed(p, protectedErr(p))
			continue
		}
		dir, name, done, err := root.parent(p)
		if err == nil {
			err = walkChange(ctx, dir, name, p, recursive, b.log, fn)
			done()
		}
		if err != nil {
			b.failed(p, err)
			continue
		}
		b.done("changed the %s of %s", what, p)
	}
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
	changeEach(ctx, root, req.Paths, req.Recursive, b, "permissions", func(file *os.File, fi fs.FileInfo) error {
		if fi.IsDir() {
			return file.Chmod(goMode(dirMode & helperapi.DirModeMask))
		}
		return file.Chmod(goMode(req.Mode & helperapi.FileModeMask))
	})
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
	changeEach(ctx, root, req.Paths, req.Recursive, b, "owner", func(file *os.File, _ fs.FileInfo) error {
		return file.Chown(uid, gid)
	})
	return b.result()
}

// Uploads ---------------------------------------------------------------

// placeOp moves a finished upload into place: renamed when the panel's
// directory is on the same file system, copied otherwise. The destination
// is opened once; the upload is opened where the panel left it and given
// its owner and mode as that open file, then gets its name in the open
// destination in one step: no name in the destination is ever changed by
// path.
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
	missing := fileErr(helperapi.CodeFileNotFound, map[string]any{"upload": req.Upload})
	srcName := req.Upload + ".part"
	uploads, err := data.OpenRoot("uploads")
	if err != nil {
		return nil, missing
	}
	defer func() { _ = uploads.Close() }()
	// The panel's directory is not root's: only a regular file with no
	// other name is taken from it (see openBackup), the entry itself and
	// not what a link there leads to.
	info, err := uploads.Lstat(srcName)
	if err != nil || !info.Mode().IsRegular() {
		return nil, missing
	}
	in, was, err := openEntry(uploads, srcName, info)
	if err != nil {
		return nil, missing
	}
	defer func() { _ = in.Close() }()
	if linked(was) {
		return nil, missing
	}
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	dir, err := root.dir(req.Dir, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	uid, gid, err := dirOwner(dir)
	if err != nil {
		return nil, err
	}
	exists := fileErr(helperapi.CodeFileExists, map[string]any{"path": p})
	isDir := fileErr(helperapi.CodeFileIsDir, map[string]any{"path": p})
	if fi, err := dir.Lstat(req.Name); err == nil {
		if !req.Overwrite {
			return nil, exists
		}
		if fi.IsDir() {
			return nil, isDir
		}
	}
	step("place", req.Dir)
	err = errors.Join(in.Chown(uid, gid), in.Chmod(defaultFileMod))
	if err == nil {
		err = renameAt(uploads, srcName, dir, req.Name, !req.Overwrite)
	}
	if errors.Is(err, syscall.EXDEV) {
		_, _ = fmt.Fprintln(log, "the upload is on another file system: copying")
		if err = copyUpload(in, dir, req.Name, uid, gid, !req.Overwrite); err == nil {
			err = uploads.Remove(srcName)
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrExist):
			err = exists
		case errors.Is(err, syscall.EISDIR):
			err = isDir
		}
		// Not placed: it is the panel's upload again, as it was.
		wuid, wgid := fileOwner(was)
		return nil, errors.Join(err, in.Chown(wuid, wgid), in.Chmod(was.Mode().Perm()))
	}
	step("placed", p)
	fi, err := dir.Lstat(req.Name)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "placed %s (%d bytes)\n", p, fi.Size())
	return entryOf(dir, req.Name, p, fi, newNames()), nil
}

// copyUpload copies the open upload to name of the open directory dir,
// for uid:gid: into a new file of dir that is given its owner and mode
// while open, then renamed to name there (with keep, never over an
// entry).
func copyUpload(in *os.File, dir *os.Root, name string, uid, gid int, keep bool) error {
	tmp := randomName(".cairn-upload-")
	out, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Chown(uid, gid)
	}
	if err == nil {
		err = out.Chmod(defaultFileMod)
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = renameAt(dir, tmp, dir, name, keep)
	}
	if err != nil {
		_ = dir.Remove(tmp)
	}
	return err
}
