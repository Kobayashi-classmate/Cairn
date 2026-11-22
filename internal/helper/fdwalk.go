// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"sort"
	"syscall"
)

// Walking trees that other users own (a site's files, a destination of
// theirs). A path is resolved anew at every use, so an owner who swaps a
// directory for a link between two steps of a walk would have the next
// step act on whatever the link points to: another site's files, read or
// changed as root. The walk below never uses a path of more than one
// component: it looks at a name (Lstat) in the directory it is in, enters
// a directory through a root of its own opened by that one name, and
// checks that what it opened is what it looked at (os.SameFile). An entry
// that changed meanwhile is skipped, never followed; changes go through
// descriptors opened with O_NOFOLLOW, or by single name.

// errSwapped reports an entry that is no longer what was looked at.
var errSwapped = errors.New("the entry changed meanwhile")

// enterHook runs just before a directory is entered (tests swap the
// directory for a link there).
var enterHook func(dir *os.Root, name string)

// enterDir opens the directory name of dir as a root of its own, checked
// to be the directory info (its Lstat) describes. Anything else, a link
// to a directory included, is errSwapped.
func enterDir(dir *os.Root, name string, info fs.FileInfo) (*os.Root, error) {
	if enterHook != nil {
		enterHook(dir, name)
	}
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

// openEntry opens name in dir itself (never a link: O_NOFOLLOW; never
// blocking on a pipe: O_NONBLOCK), checked to be what info describes.
func openEntry(dir *os.Root, name string, info fs.FileInfo) (*os.File, fs.FileInfo, error) {
	f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, fs.ErrNotExist) {
			return nil, nil, errSwapped
		}
		return nil, nil, err
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

// openParent opens the directory holding p (resolved inside root, as
// every request path is) and returns p's name in it; "" or "." is the
// root itself, named ".". done closes what was opened.
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
	Path string      // its path in the walked root
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
	info, err := dir.Lstat(name)
	if err != nil {
		return notFound(err, start)
	}
	err = walkAt(ctx, walkEntry{Dir: dir, Name: name, Path: start, Info: info}, fn)
	if errors.Is(err, fs.SkipDir) || errors.Is(err, fs.SkipAll) {
		return nil
	}
	return err
}

func walkAt(ctx context.Context, e walkEntry, fn func(walkEntry) error) error {
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
	sub, err := enterDir(e.Dir, e.Name, e.Info)
	if errors.Is(err, errSwapped) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = sub.Close() }()
	names, err := dirNames(sub)
	if err != nil {
		return err
	}
	for _, n := range names {
		info, err := sub.Lstat(n)
		if err != nil {
			continue // gone meanwhile
		}
		if err := walkAt(ctx, walkEntry{Dir: sub, Name: n, Path: join(e.Path, n), Rel: join(e.Rel, n), Info: info}, fn); err != nil {
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
