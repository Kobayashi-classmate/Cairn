// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// One nginx user serves every site, and it can read every site that serves
// files. What keeps a site's links from serving another site's files is
// "disable_symlinks if_not_owner": nginx serves a link only when it has the
// owner of what it points to, and each site's files belong to users of its
// own. A project's own links may still cross its two users (Laravel's
// public/storage made by the owner, or by root, pointing into a directory
// the runner created). For a site that allows that, alignLinks gives each
// such link the owner of its target, so that nginx serves it; it only ever
// hands a link to the site's own owner or runner, so a link to anything
// else (another site, the system) stays refused.

// maxAlignEntries bounds the walk over a site's files.
const maxAlignEntries = 200000

// alignLinks gives every link below the site's app directory that leads to
// a file of the site's owner or runner the owner of that file. Only links
// of root, the owner or the runner, with one name, are changed.
func (st *Sites) alignLinks(ctx context.Context, log io.Writer, code string) error {
	ouid, _, err := lookupUser(ownerOf(code))
	if err != nil {
		return err
	}
	ruid, _, err := lookupUser(runnerOf(code))
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(st.path(filepath.Join(siteDirOf(code), "app")))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	seen, changed := 0, 0
	err = walkTree(ctx, root, ".", func(e walkEntry) error {
		if seen++; seen > maxAlignEntries {
			_, _ = fmt.Fprintf(log, "The site has more than %d files; links further on were left as they are.\n", maxAlignEntries)
			return fs.SkipAll
		}
		if e.Info.Mode()&fs.ModeSymlink == 0 {
			return nil
		}
		uid, err := alignLink(e.Dir, e.Name, e.Info, ouid, ruid)
		if err != nil {
			return fmt.Errorf("link %s: %w", e.Path, err)
		}
		if uid >= 0 {
			changed++
			_, _ = fmt.Fprintf(log, "The link app/%s now belongs to %s, like the file it points to.\n", e.Path,
				map[bool]string{true: ownerOf(code), false: runnerOf(code)}[uid == ouid])
		}
		return nil
	})
	if err != nil {
		return err
	}
	if changed > 0 {
		_, _ = fmt.Fprintf(log, "%d link(s) of the site changed owner.\n", changed)
	}
	return nil
}

// alignLink changes the owner of the link name in dir (info: its Lstat) to
// the owner of what it points to, when that is ouid or ruid and the link's
// owner is root, ouid or ruid. It returns the new owner, or -1 when the
// link was left alone. Everything is done on the link itself, opened with
// O_PATH|O_NOFOLLOW: a link swapped meanwhile for something else (a hard
// link to a file that is not the site's) is never changed.
func alignLink(dir *os.Root, name string, info fs.FileInfo, ouid, ruid int) (int, error) {
	d, err := dir.Open(".")
	if err != nil {
		return -1, err
	}
	defer func() { _ = d.Close() }()
	dfd := int(d.Fd()) //nolint:gosec // G115: a descriptor fits an int.
	lfd, err := unix.Openat(dfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return -1, nil // gone meanwhile
		}
		return -1, err
	}
	defer func() { _ = unix.Close(lfd) }()
	var ls unix.Stat_t
	if err := unix.Fstat(lfd, &ls); err != nil {
		return -1, err
	}
	want, ok := info.Sys().(*syscall.Stat_t)
	if !ok || ls.Mode&unix.S_IFMT != unix.S_IFLNK || ls.Nlink != 1 || ls.Dev != want.Dev || ls.Ino != want.Ino {
		return -1, nil
	}
	luid := int(ls.Uid)
	if luid != 0 && luid != ouid && luid != ruid {
		return -1, nil
	}
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(lfd, "", buf)
	if err != nil {
		return -1, err
	}
	target := string(buf[:n])
	// Where the link leads only decides which of the site's two users gets
	// it; nginx checks the owners again at every request.
	var ts unix.Stat_t
	if filepath.IsAbs(target) {
		err = unix.Stat(target, &ts)
	} else {
		err = unix.Fstatat(dfd, target, &ts, 0)
	}
	if err != nil {
		return -1, nil //nolint:nilerr // a dangling link serves nothing either way
	}
	tuid := int(ts.Uid)
	if tuid == luid || tuid != ouid && tuid != ruid {
		return -1, nil
	}
	if err := unix.Fchownat(lfd, "", tuid, -1, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return -1, err
	}
	return tuid, nil
}
