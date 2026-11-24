// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Working in trees that other users own (a site's files, a destination of
// theirs). A path is resolved anew at every use, so an owner who swaps a
// directory for a link between two steps would have the next step act on
// whatever the link points to: another site's files, read or changed as
// root. So a path is resolved once, into an open directory (a root of its
// own) and a name in it (see fileRoot), and every later step goes through
// that directory. A walk never uses a path of more than one component: it
// looks at a name (Lstat) in the directory it is in, enters a directory
// through a root of its own opened by that one name, and checks that what
// it opened is what it looked at (os.SameFile). An entry that changed
// meanwhile is skipped, never followed; changes go through descriptors
// checked the same way (a root follows a link whatever the open flags
// say, so O_NOFOLLOW alone keeps none out), or by single name; entries
// move between two open directories by their names in them (renameAt).

// errSwapped reports an entry that is no longer what was looked at.
var errSwapped = errors.New("the entry changed meanwhile")

// maxTreeDepth is how deep a tree is walked or copied. A path the kernel
// takes ends long before that, but directories can be nested further one
// level at a time, and a walk holds a descriptor and a path for every
// level it is in: without an end, the owner of a tree decides how much of
// both the helper spends on it.
const maxTreeDepth = 1024

// errTooDeep reports a tree nested deeper than maxTreeDepth.
var errTooDeep = errors.New("the directories are nested too deep")

// tooDeep reports whether the path p (relative, "/" between its names)
// is at the depth where a walk stops.
func tooDeep(p string) bool { return strings.Count(p, "/") >= maxTreeDepth }

// openNoLink opens p of root itself for reading, never what a link in
// its last component leads to and never waiting on a pipe. A root follows
// such a link whatever the flags say, so the entry is looked at first and
// what was opened is compared with it.
func openNoLink(root *os.Root, p string) (*os.File, error) {
	info, err := root.Lstat(p)
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, &fs.PathError{Op: "open", Path: p, Err: syscall.ELOOP}
	}
	f, err := root.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !os.SameFile(info, fi) {
		_ = f.Close()
		return nil, &fs.PathError{Op: "open", Path: p, Err: syscall.ELOOP}
	}
	return f, nil
}

// enterHook runs just before a directory is entered (tests swap the
// directory for a link there).
var enterHook func(dir *os.Root, name string)

// stepHook runs between the look and the act of an operation on one entry,
// with the stage's name and the path involved (tests swap a directory for
// a link there).
var stepHook func(stage, p string)

func step(stage, p string) {
	if stepHook != nil {
		stepHook(stage, p)
	}
}

// enterDir opens the directory name of dir as a root of its own, checked
// to be the directory info (its Lstat) describes. Anything else, a link
// to a directory included, is errSwapped.
func enterDir(dir *os.Root, name string, info fs.FileInfo) (*os.Root, error) {
	if enterHook != nil {
		enterHook(dir, name)
	}
	return enter(dir, name, info)
}

func enter(dir *os.Root, name string, info fs.FileInfo) (*os.Root, error) {
	if !info.IsDir() {
		return nil, errSwapped
	}
	sub, err := dir.OpenRoot(name)
	if err != nil {
		// A link to elsewhere, or gone: not the directory looked at. A
		// directory still in place failed for a reason of its own.
		if now, lerr := dir.Lstat(name); lerr == nil && now.IsDir() && os.SameFile(info, now) {
			return nil, err
		}
		return nil, errSwapped
	}
	got, err := sub.Stat(".")
	if err != nil || !os.SameFile(info, got) {
		_ = sub.Close()
		return nil, errSwapped
	}
	return sub, nil
}

// subDir opens the directory name of dir as a root of its own: the
// directory itself, never what a link there leads to (ENOTDIR, as for
// anything else that is no directory).
func subDir(dir *os.Root, name string) (*os.Root, error) {
	info, err := dir.Lstat(name)
	if err != nil {
		return nil, err
	}
	sub, err := enter(dir, name, info)
	if errors.Is(err, errSwapped) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: syscall.ENOTDIR}
	}
	return sub, err
}

// openEntry opens name in dir itself (never blocking on a pipe:
// O_NONBLOCK), checked to be what info, its Lstat, describes. That check
// is what keeps a link out: a root follows a link put there since the
// look, whatever the flags say, and what it leads to is not what was
// looked at.
func openEntry(dir *os.Root, name string, info fs.FileInfo) (*os.File, fs.FileInfo, error) {
	f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		// A link to elsewhere, or gone: not the entry looked at. One
		// still in place failed for a reason of its own.
		if now, lerr := dir.Lstat(name); lerr == nil && os.SameFile(info, now) {
			return nil, nil, err
		}
		return nil, nil, errSwapped
	}
	fi, err := f.Stat()
	if err == nil && !os.SameFile(info, fi) {
		err = errSwapped
	}
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// dirNames lists a directory's entries, sorted.
func dirNames(d *os.Root) ([]string, error) {
	f, err := d.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(-1)
	sort.Strings(names)
	return names, err
}

// dirOwner is who new entries of the open directory d belong to.
func dirOwner(d *os.Root) (int, int, error) {
	fi, err := d.Stat(".")
	if err != nil {
		return 0, 0, err
	}
	uid, gid := fileOwner(fi)
	return uid, gid, nil
}

// renameEntry is renameat2(2). Tests replace it (a rename that meets
// another file system).
var renameEntry = unix.Renameat2

// renameAt moves the entry sname of the open directory sdir to dname of
// ddir: plain names in directories that are held open, no path to follow.
// With keep, an entry that is at dname is never replaced (fs.ErrExist).
func renameAt(sdir *os.Root, sname string, ddir *os.Root, dname string, keep bool) error {
	// The system call would resolve anything more than a name from the
	// directory on, links and ".." included: the roots' guard ends here.
	for _, n := range []string{sname, dname} {
		if n == "" || n == "." || n == ".." || strings.ContainsRune(n, '/') {
			return &os.LinkError{Op: "rename", Old: sname, New: dname, Err: syscall.EINVAL}
		}
	}
	sfd, err := sdir.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = sfd.Close() }()
	dfd := sfd
	if ddir != sdir {
		if dfd, err = ddir.Open("."); err != nil {
			return err
		}
		defer func() { _ = dfd.Close() }()
	}
	var flags uint
	if keep {
		flags = unix.RENAME_NOREPLACE
	}
	err = renameEntry(int(sfd.Fd()), sname, int(dfd.Fd()), dname, flags)
	if keep && (errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS)) {
		// A file system that cannot refuse to replace: look, then rename.
		if _, lerr := ddir.Lstat(dname); lerr == nil {
			err = unix.EEXIST
		} else {
			err = renameEntry(int(sfd.Fd()), sname, int(dfd.Fd()), dname, 0)
		}
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: sname, New: dname, Err: err}
	}
	return nil
}

// openParent opens the directory holding p, resolved inside root (which
// is the place to stay in already: an open directory, see fileRoot), and
// returns p's name in it; "" or "." is the root itself, named ".". done
// closes what was opened.
func openParent(root *os.Root, p string) (dir *os.Root, name string, done func(), err error) {
	if p == "" || p == "." {
		return root, ".", func() {}, nil
	}
	parent := parentOf(p)
	if parent == "" {
		return root, p, func() {}, nil
	}
	d, err := root.OpenRoot(parent)
	if err != nil {
		return nil, "", nil, err
	}
	return d, path.Base(p), func() { _ = d.Close() }, nil
}

// walkEntry is one entry of a walk.
type walkEntry struct {
	Dir  *os.Root    // the directory the entry is in
	Name string      // its name there ("." for a start that is the root)
	Path string      // its path, as the start was named
	Rel  string      // its path from the start ("" for the start)
	Info fs.FileInfo // what Lstat found
}

// open opens the entry itself; see openEntry.
func (e walkEntry) open() (*os.File, fs.FileInfo, error) { return openEntry(e.Dir, e.Name, e.Info) }

// walkTree calls fn for start and, below it, for every entry, parents
// before their contents (fn returning fs.SkipDir for a directory leaves
// its contents out, fs.SkipAll ends the walk). Links are entries, never
// followed: a start that is a link is passed to fn by itself. A directory
// that changed between the look and the entering is not entered.
func walkTree(ctx context.Context, root *os.Root, start string, fn func(e walkEntry) error) error {
	if start == "." {
		start = ""
	}
	dir, name, done, err := openParent(root, start)
	if err != nil {
		return notFound(err, start)
	}
	defer done()
	return walkNamed(ctx, dir, name, start, false, fn)
}

// walkNamed is walkTree for the entry name of the open directory dir,
// known to the caller as p. With lenient, a directory that cannot be
// entered or listed is left out instead of ending the walk.
func walkNamed(ctx context.Context, dir *os.Root, name, p string, lenient bool, fn func(e walkEntry) error) error {
	info, err := dir.Lstat(name)
	if err != nil {
		return notFound(err, p)
	}
	err = walkAt(ctx, walkEntry{Dir: dir, Name: name, Path: p, Info: info}, lenient, fn)
	if errors.Is(err, fs.SkipDir) || errors.Is(err, fs.SkipAll) {
		return nil
	}
	return err
}

func walkAt(ctx context.Context, e walkEntry, lenient bool, fn func(walkEntry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := fn(e)
	if errors.Is(err, fs.SkipDir) {
		return nil
	}
	if err != nil || !e.Info.IsDir() {
		return err
	}
	if tooDeep(e.Rel) {
		if lenient {
			return nil
		}
		return errTooDeep
	}
	sub, err := enterDir(e.Dir, e.Name, e.Info)
	if errors.Is(err, errSwapped) || err != nil && lenient {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = sub.Close() }()
	names, err := dirNames(sub)
	if err != nil && !lenient {
		return err
	}
	for _, n := range names {
		info, err := sub.Lstat(n)
		if err != nil {
			continue // gone meanwhile
		}
		if err := walkAt(ctx, walkEntry{Dir: sub, Name: n, Path: join(e.Path, n), Rel: join(e.Rel, n), Info: info}, lenient, fn); err != nil {
			return err
		}
	}
	return nil
}

// foreignLink reports whether fi is a regular file with other names whose
// owner is not owner (the owner of the tree being changed): a hard link
// someone planted to a file that is not theirs (another site's, or a
// system file where hard links are not protected) must not change owner
// or mode with the tree. The tree's own hard links (pnpm's store, links
// unpacked from an archive) are the owner's files and change with it.
func foreignLink(fi fs.FileInfo, owner int) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && fi.Mode().IsRegular() && st.Nlink > 1 && int(st.Uid) != owner
}

// uidOf is an entry's owner.
func uidOf(fi fs.FileInfo) int {
	uid, _ := fileOwner(fi)
	return uid
}
