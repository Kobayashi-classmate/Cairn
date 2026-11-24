// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Configuration files of services. Some live in directories that the
// service's own account owns (/etc/postgresql/<version>/main belongs to
// postgres, /etc/redis to redis), so that account can put a link, a pipe
// or a second name of another file where the helper expects the
// configuration file, or swap a directory on the way for a link, at any
// moment. Resolving such a path by name would have root read or replace
// whatever it points to. A configuration path is therefore never
// resolved by name: its directory is opened one component at a time,
// following no link but one that only root can have made, and the file is
// read, created, changed and renamed through descriptors of that
// directory only.

// Reasons a configuration path is refused.
const (
	confLink      = "link"          // the file or a directory on the way is a symbolic link
	confSpecial   = "not_regular"   // a pipe, socket, device or directory where a file belongs
	confHardLinks = "hard_links"    // the file has other names, elsewhere perhaps
	confNotDir    = "not_directory" // something other than a directory on the way
)

// confUnsafeError reports a configuration path that is not a plain file
// in plain directories.
type confUnsafeError struct {
	Path   string
	Reason string
}

func (e *confUnsafeError) Error() string {
	return fmt.Sprintf("%s is refused: %s", e.Path, e.Reason)
}

// asConfUnsafe returns the refusal inside err, if that is what it is.
func asConfUnsafe(err error) (*confUnsafeError, bool) {
	var ue *confUnsafeError
	ok := errors.As(err, &ue)
	return ue, ok
}

// confReadMax bounds what is read of a configuration file the helper
// parses itself (pg_hba.conf, redis.conf).
const confReadMax = 4 << 20

// confLinkHops bounds the links followed on the way to one directory.
const confLinkHops = 8

// openConfDir opens the directory dir (an absolute path, resolved beneath
// root, which is trusted: "" is "/") one component at a time. A link on
// the way is followed only where nobody but root can have put it (see
// rootsLink): an administrator who moved /var/lib/redis or /etc/nginx to
// another disk and left a link keeps working. Any other link is refused.
// With create, missing directories are made (0755, owned by root), but
// never what a link points to: that must be there.
func openConfDir(root, dir string, create bool) (*os.File, error) {
	if root == "" {
		root = "/"
	}
	return openConfDirVia(root, filepath.Clean("/"+dir), create, confLinkHops)
}

func openConfDirVia(root, p string, create bool, hops int) (*os.File, error) {
	f, target, rest, err := walkConfDir(root, p, create)
	if err != nil || target == "" {
		return f, err
	}
	if hops == 0 {
		return nil, &confUnsafeError{Path: p, Reason: confLink}
	}
	there, err := openConfDirVia(root, target, false, hops-1)
	if err != nil {
		return nil, err
	}
	_ = there.Close()
	return openConfDirVia(root, filepath.Join(target, rest), create, hops-1)
}

// walkConfDir opens p (absolute and clean, beneath root) without going
// through any link. When it meets a link of root's that may be followed,
// it returns the link's target as an absolute path and what was left of p
// after the link instead.
func walkConfDir(root, p string, create bool) (dir *os.File, target, rest string, err error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", "", &fs.PathError{Op: "open", Path: root, Err: err}
	}
	parts := strings.Split(p, "/")
	cur := "/" // the directory fd is, with no link in it
	for i, c := range parts {
		if c == "" {
			continue
		}
		next, err := openDirAt(fd, c)
		if errors.Is(err, unix.ENOENT) && create {
			if err = unix.Mkdirat(fd, c, 0o755); err == nil || errors.Is(err, unix.EEXIST) {
				next, err = openDirAt(fd, c)
			}
		}
		if err != nil {
			to, ok := rootsLink(fd, c)
			if !ok {
				err = confEntryErr(fd, c, filepath.Join(cur, c), err, true)
			}
			_ = unix.Close(fd)
			if !ok {
				return nil, "", "", err
			}
			// cur has no link in it, so a relative target is resolved
			// by name as the kernel would resolve it.
			if !filepath.IsAbs(to) {
				to = filepath.Join(cur, to)
			}
			return nil, filepath.Clean(to), filepath.Join(parts[i+1:]...), nil
		}
		_ = unix.Close(fd)
		fd, cur = next, filepath.Join(cur, c)
	}
	return os.NewFile(uintptr(fd), cur), "", "", nil
}

// rootsLink reads the symbolic link name of the open directory dirfd when
// only root can have put it there: the link is root's, and so is the
// directory, which nobody else may write to. A link in a directory that a
// service's account owns never qualifies, whoever it belongs to.
func rootsLink(dirfd int, name string) (string, bool) {
	var dir, link unix.Stat_t
	if unix.Fstat(dirfd, &dir) != nil || dir.Uid != 0 || dir.Mode&0o022 != 0 {
		return "", false
	}
	if unix.Fstatat(dirfd, name, &link, unix.AT_SYMLINK_NOFOLLOW) != nil || link.Mode&unix.S_IFMT != unix.S_IFLNK || link.Uid != 0 {
		return "", false
	}
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(dirfd, name, buf)
	if err != nil || n <= 0 || n >= len(buf) {
		return "", false
	}
	return string(buf[:n]), true
}

func openDirAt(dirfd int, name string) (int, error) {
	return unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// confEntryErr explains why name in dirfd could not be opened: a link or
// something else in the way is a refusal, anything else an error of p.
func confEntryErr(dirfd int, name, p string, err error, wantDir bool) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		var st unix.Stat_t
		if unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
			switch {
			case st.Mode&unix.S_IFMT == unix.S_IFLNK:
				return &confUnsafeError{Path: p, Reason: confLink}
			case wantDir:
				return &confUnsafeError{Path: p, Reason: confNotDir}
			}
		}
		return &confUnsafeError{Path: p, Reason: confLink}
	}
	return &fs.PathError{Op: "open", Path: p, Err: err}
}

// openConfIn opens name in dir for reading: a regular file with no other
// name, never a link (O_NOFOLLOW) and never waiting on a pipe
// (O_NONBLOCK). p names it in errors.
func openConfIn(dir *os.File, name, p string) (*os.File, fs.FileInfo, error) {
	dfd := int(dir.Fd())
	fd, err := unix.Openat(dfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENXIO) { // a socket
			return nil, nil, &confUnsafeError{Path: p, Reason: confSpecial}
		}
		return nil, nil, confEntryErr(dfd, name, p, err, false)
	}
	f := os.NewFile(uintptr(fd), p)
	info, err := f.Stat()
	if err == nil {
		err = confFileOK(info, p)
	}
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, info, nil
}

// confFileOK refuses anything but a regular file with a single name: a
// second name may be a file of root's elsewhere on the file system.
func confFileOK(info fs.FileInfo, p string) error {
	if !info.Mode().IsRegular() {
		return &confUnsafeError{Path: p, Reason: confSpecial}
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink != 1 {
		return &confUnsafeError{Path: p, Reason: confHardLinks}
	}
	return nil
}

// openConfFile opens the configuration file p (beneath root) for reading.
func openConfFile(root, p string) (*os.File, fs.FileInfo, error) {
	dir, err := openConfDir(root, filepath.Dir(p), false)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = dir.Close() }()
	return openConfIn(dir, filepath.Base(p), p)
}

// errConfTooLarge reports a configuration file larger than asked for.
var errConfTooLarge = errors.New("configuration file too large")

// readConfFile reads the configuration file p (beneath root), at most max
// bytes; the information is that of the file read.
func readConfFile(root, p string, maxSize int64) ([]byte, fs.FileInfo, error) {
	f, info, err := openConfFile(root, p)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	if info.Size() > maxSize {
		return nil, info, errConfTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > maxSize {
		return nil, info, errConfTooLarge
	}
	return data, info, nil
}

// confWrite says how writeConfFile treats a file that is not there yet.
type confWrite struct {
	// Create allows a new file, with Mode and owned by root; otherwise a
	// missing file is an error.
	Create bool
	Mode   os.FileMode
	// MkdirAll makes missing directories on the way (0755, root).
	MkdirAll bool
}

// writeConfFile replaces the configuration file p (beneath root) with
// data atomically. An existing file must be one readConfFile would read;
// its mode, owner and SELinux label carry over to the new content. The
// new file is made, filled, given its owner, mode and label and synced
// through its own descriptor before it takes the old one's name, so
// nothing the directory's owner does meanwhile can turn any of these
// steps onto another file.
func writeConfFile(root, p string, data []byte, how confWrite) error {
	dir, err := openConfDir(root, filepath.Dir(p), how.MkdirAll)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	dfd := int(dir.Fd())
	name := filepath.Base(p)

	mode, uid, gid := how.Mode.Perm(), -1, -1
	var label []byte
	old, info, err := openConfIn(dir, name, p)
	switch {
	case err == nil:
		mode = info.Mode().Perm()
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(st.Uid), int(st.Gid)
		}
		label = fileLabel(int(old.Fd()))
		_ = old.Close()
	case errors.Is(err, fs.ErrNotExist) && how.Create:
	default:
		return err
	}

	tmp, tfd, err := createTempAt(dfd)
	if err != nil {
		return &fs.PathError{Op: "create", Path: filepath.Join(filepath.Dir(p), ".cairn-*"), Err: err}
	}
	f := os.NewFile(uintptr(tfd), tmp)
	placed := false
	defer func() {
		_ = f.Close()
		if !placed {
			_ = unix.Unlinkat(dfd, tmp, 0)
		}
	}()
	relabel := false
	err = func() error {
		if _, err := f.Write(data); err != nil {
			return err
		}
		if uid >= 0 {
			if err := unix.Fchown(tfd, uid, gid); err != nil {
				return err
			}
		}
		// After the owner: a change of owner clears set-ID bits.
		if err := unix.Fchmod(tfd, uint32(mode)); err != nil {
			return err
		}
		if len(label) > 0 {
			// Where the label cannot be copied, the policy's own one is
			// set once the file is in place.
			relabel = unix.Fsetxattr(tfd, selinuxXattr, label, 0) != nil
		}
		return f.Sync()
	}()
	if err != nil {
		return fmt.Errorf("writing %s: %w", p, err)
	}
	// renameat replaces whatever has the name by then without following
	// it: a link put there meanwhile is replaced, not written through.
	if err := unix.Renameat(dfd, tmp, dfd, name); err != nil {
		return &fs.PathError{Op: "rename", Path: p, Err: err}
	}
	placed = true
	_ = dir.Sync()
	if relabel {
		// restorecon only ever sets the policy's label for the path.
		full := filepath.Join(root, p)
		if _, err := queryCommand(context.Background(), "restorecon", full); err != nil {
			return fmt.Errorf("restoring the SELinux label of %s: %w", p, err)
		}
	}
	return nil
}

// removeConfFile removes the configuration file p (beneath root) by its
// name in its directory; a missing file is not an error.
func removeConfFile(root, p string) error {
	dir, err := openConfDir(root, filepath.Dir(p), false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	err = unix.Unlinkat(int(dir.Fd()), filepath.Base(p), 0)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return &fs.PathError{Op: "remove", Path: p, Err: err}
	}
	return nil
}

// createTempAt makes a new private file in dirfd under a fresh name.
func createTempAt(dirfd int) (string, int, error) {
	for range 16 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", -1, err
		}
		name := ".cairn-" + hex.EncodeToString(b[:])
		fd, err := unix.Openat(dirfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		return name, fd, err
	}
	return "", -1, unix.EEXIST
}

// fileLabel is the SELinux label of an open file (nil without one).
func fileLabel(fd int) []byte {
	buf := make([]byte, 256)
	for {
		n, err := unix.Fgetxattr(fd, selinuxXattr, buf)
		if errors.Is(err, unix.ERANGE) && len(buf) < 64<<10 {
			buf = make([]byte, len(buf)*4)
			continue
		}
		if err != nil || n <= 0 {
			return nil
		}
		return buf[:n]
	}
}
