// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"golang.org/x/sys/unix"

	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// Archives: zip and tar (gzip, zstd, xz; bzip2 read only) are handled
// here, entry by entry through the root, so that no entry lands outside
// the destination: names with "..", absolute links and links that leave
// the destination are skipped, and so are devices and pipes. 7z and RAR go
// through unar into a scratch directory first, then the same way.

const (
	unarBinary     = "/usr/bin/unar"
	extractEntries = 200000
	extractTimeout = 6 * time.Hour
)

func (f *Files) archivers() helperapi.Archivers {
	_, err := os.Stat(f.path(unarBinary))
	return helperapi.Archivers{SevenZip: err == nil, RAR: err == nil}
}

// archiveKind tells an archive's format from its name.
func archiveKind(name string) string {
	n := strings.ToLower(name)
	for _, k := range []struct{ suffix, kind string }{
		{".tar.gz", "tar.gz"}, {".tgz", "tar.gz"}, {".tar.zst", "tar.zst"}, {".tzst", "tar.zst"},
		{".tar.xz", "tar.xz"}, {".txz", "tar.xz"}, {".tar.bz2", "tar.bz2"}, {".tbz2", "tar.bz2"}, {".tbz", "tar.bz2"},
		{".tar", "tar"}, {".zip", "zip"}, {".7z", "unar"}, {".rar", "unar"},
		{".gz", "gz"}, {".zst", "zst"}, {".xz", "xz"}, {".bz2", "bz2"},
	} {
		if strings.HasSuffix(n, k.suffix) {
			return k.kind
		}
	}
	return ""
}

// decompress wraps r for a compressed stream kind.
func decompress(kind string, r io.Reader) (io.Reader, func(), error) {
	switch strings.TrimPrefix(kind, "tar.") {
	case "gz":
		z, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, err
		}
		return z, func() { _ = z.Close() }, nil
	case "zst":
		z, err := zstd.NewReader(r)
		if err != nil {
			return nil, nil, err
		}
		return z, z.Close, nil
	case "xz":
		z, err := xz.NewReader(r)
		if err != nil {
			return nil, nil, err
		}
		return z, func() {}, nil
	case "bz2":
		return bzip2.NewReader(r), func() {}, nil
	}
	return r, func() {}, nil
}

// extractor writes entries below dest, for its owner. Entries land only in
// the destination's own root, and every directory on an entry's way is
// looked at and entered one level at a time (see walkTree): the owner can
// swap a directory for a link between two entries, and the next entry
// would go wherever it points.
type extractor struct {
	ctx      context.Context
	rootID   string
	src      *os.Root // the archive's directory, opened once
	archive  string   // the archive's path in the request's root
	root     *os.Root // the destination's own root
	dest     string   // the destination in the request's root (names, protected paths)
	uid, gid int
	// overwrite replaces entries that are there.
	overwrite bool
	b         *batch
	budget    int64
	entries   int
	// files are the regular files this extraction wrote (paths below the
	// destination): the only ones a hard link entry may name.
	files map[string]bool
}

var errNoSpace = fileErr(helperapi.CodeFileNoSpace, nil)

// target maps an entry name to its path below the destination, or "" when
// it would leave it or is protected.
func (x *extractor) target(name string) string {
	name = strings.TrimLeft(strings.ReplaceAll(name, "\\", "/"), "/")
	name = path.Clean(name)
	if name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return ""
	}
	for _, c := range strings.Split(name, "/") {
		if !helperapi.ValidFileName(c) {
			return ""
		}
	}
	if protected(x.rootID, join(x.dest, name)) {
		return ""
	}
	return name
}

// enter enters the directory name of dir, made for the destination's
// owner when missing; anything else in its place (a link) is refused,
// never followed.
func (x *extractor) enter(dir *os.Root, name, p string) (*os.Root, bool, error) {
	made := false
	info, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		if err := dir.Mkdir(name, defaultDirMod); err != nil {
			return nil, false, err
		}
		made = true
		info, err = dir.Lstat(name)
	}
	if err != nil {
		return nil, false, err
	}
	notDir := fileErr(helperapi.CodeFileNotDir, map[string]any{"path": join(x.dest, p)})
	if !info.IsDir() {
		return nil, false, notDir
	}
	sub, err := enterDir(dir, name, info)
	if errors.Is(err, errSwapped) {
		return nil, false, notDir
	}
	if err != nil {
		return nil, false, err
	}
	if made {
		if err := sub.Lchown(".", x.uid, x.gid); err != nil {
			_ = sub.Close()
			return nil, false, err
		}
	}
	return sub, made, nil
}

// parent returns the directory p (below the destination) goes into, as a
// root of its own, and p's name in it; the directories on the way are made
// where missing. It checks again for every entry (no cache). done closes
// what was opened.
func (x *extractor) parent(p string) (*os.Root, string, func(), error) {
	var opened []*os.Root
	done := func() {
		for _, r := range opened {
			_ = r.Close()
		}
	}
	dir, walked := x.root, ""
	if d := parentOf(p); d != "" {
		for _, c := range strings.Split(d, "/") {
			walked = join(walked, c)
			sub, _, err := x.enter(dir, c, walked)
			if err != nil {
				done()
				return nil, "", nil, err
			}
			opened = append(opened, sub)
			dir = sub
		}
	}
	return dir, path.Base(p), done, nil
}

func (x *extractor) count() error {
	if x.ctx.Err() != nil {
		return x.ctx.Err()
	}
	if x.entries++; x.entries > extractEntries {
		return fileErr(helperapi.CodeFileTooLarge, map[string]any{"entries": extractEntries})
	}
	return nil
}

// free makes room for the entry name of dir (p below the destination);
// false when it is skipped.
func (x *extractor) free(dir *os.Root, name, p string) (bool, error) {
	fi, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	full := join(x.dest, p)
	if !x.overwrite {
		x.b.failed(full, fileErr(helperapi.CodeFileExists, map[string]any{"path": full}))
		return false, nil
	}
	if fi.IsDir() {
		x.b.failed(full, fileErr(helperapi.CodeFileIsDir, map[string]any{"path": full}))
		return false, nil
	}
	delete(x.files, p)
	return true, dir.Remove(name)
}

func (x *extractor) dir(name string, mode fs.FileMode) error {
	p := x.target(name)
	if p == "" {
		x.b.failed(name, fileErr(helperapi.CodeFileProtected, map[string]any{"path": name}))
		return nil
	}
	dir, base, done, err := x.parent(p)
	if err != nil {
		x.b.failed(name, err)
		return nil
	}
	defer done()
	sub, made, err := x.enter(dir, base, p)
	if err != nil {
		x.b.failed(name, err)
		return nil
	}
	defer func() { _ = sub.Close() }()
	// A directory that was there keeps its mode unless overwriting.
	if made || x.overwrite {
		_ = sub.Chmod(".", mode.Perm()|0o700)
	}
	return nil
}

func (x *extractor) file(name string, mode fs.FileMode, r io.Reader) error {
	p := x.target(name)
	if p == "" {
		x.b.failed(name, fileErr(helperapi.CodeFileProtected, map[string]any{"path": name}))
		return nil
	}
	dir, base, done, err := x.parent(p)
	if err != nil {
		x.b.failed(name, err)
		return nil
	}
	defer done()
	ok, err := x.free(dir, base, p)
	if err != nil || !ok {
		return err
	}
	// O_EXCL: a new file, never one (or a link) put there meanwhile.
	out, err := dir.OpenFile(base, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		x.b.failed(name, err)
		return nil
	}
	n, err := io.Copy(out, io.LimitReader(r, x.budget+1))
	x.budget -= n
	if err == nil && x.budget < 0 {
		err = errNoSpace
	}
	if err == nil {
		err = out.Chown(x.uid, x.gid)
	}
	if err == nil {
		// Setuid and setgid never come along.
		err = out.Chmod(mode.Perm())
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = dir.Remove(base)
		return err
	}
	x.files[p] = true
	x.b.res.Done++
	return nil
}

// source opens a regular file this extraction wrote (a hard link entry's
// source), through the same level-by-level checks.
func (x *extractor) source(p string) (*os.File, error) {
	if p == "" || !x.files[p] {
		return nil, fs.ErrNotExist
	}
	dir, base, done, err := x.parent(p)
	if err != nil {
		return nil, err
	}
	defer done()
	// The file itself, not what a link put in its place leads to (see
	// openEntry).
	info, err := dir.Lstat(base)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fs.ErrNotExist
	}
	in, _, err := openEntry(dir, base, info)
	return in, err
}

// climbs reports whether a link target has ".." after a name: such a
// target can climb out through a link made before it (x -> ., then
// y -> x/..), which the lexical check cannot see. With ".." only in
// front, the climbing happens from the link's own directory, a real one.
func climbs(target string) bool {
	down := false
	for _, c := range strings.Split(target, "/") {
		switch c {
		case "..":
			if down {
				return true
			}
		case "", ".":
		default:
			down = true
		}
	}
	return false
}

func (x *extractor) symlink(name, target string) error {
	p := x.target(name)
	if p == "" {
		x.b.failed(name, fileErr(helperapi.CodeFileProtected, map[string]any{"path": name}))
		return nil
	}
	resolved := path.Clean(path.Join(parentOf(p), target))
	if path.IsAbs(target) || target == "" || climbs(target) || resolved == ".." || strings.HasPrefix(resolved, "../") {
		x.b.failed(name, fileErr(helperapi.CodeFileIsLink, map[string]any{"path": name, "target": target}))
		return nil
	}
	dir, base, done, err := x.parent(p)
	if err != nil {
		x.b.failed(name, err)
		return nil
	}
	defer done()
	ok, err := x.free(dir, base, p)
	if err != nil || !ok {
		return err
	}
	if err := dir.Symlink(target, base); err != nil {
		x.b.failed(name, err)
		return nil
	}
	x.b.res.Done++
	return dir.Lchown(base, x.uid, x.gid)
}

// space is what an extraction may write into the file system of the open
// directory d: its free space minus a reserve.
func space(d *os.File) int64 {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(d.Fd()), &st); err != nil {
		return 0
	}
	free := int64(st.Bavail) * st.Bsize                    //nolint:gosec // G115: block counts fit.
	reserve := max(int64(st.Blocks)*st.Bsize/100, 256<<20) //nolint:gosec // G115: block counts fit.
	return free - reserve
}

// openDirFD opens the directory p of root for statfs, once: never a link
// in its last component, never blocking (a pipe swapped in fails at once,
// O_DIRECTORY, instead of hanging the open).
func openDirFD(root *os.Root, p string) (*os.File, error) {
	return root.OpenFile(rel(p), os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// spaceAt is the space of the file system of directory p (0 when it
// cannot be opened as one).
func spaceAt(root *os.Root, p string) int64 {
	d, err := openDirFD(root, p)
	if err != nil {
		return 0
	}
	defer func() { _ = d.Close() }()
	return spaceOf(d)
}

// openDest opens a destination directory of a request's root, once, as a
// root of its own (a link to it is followed where the file manager
// follows one: see fileRoot), with its descriptor for statfs and its
// owner.
func openDest(root *fileRoot, p string) (*os.Root, *os.File, int, int, error) {
	droot, err := root.dir(p, true)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	fd, err := droot.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		_ = droot.Close()
		return nil, nil, 0, 0, err
	}
	info, err := fd.Stat()
	if err != nil {
		_ = droot.Close()
		_ = fd.Close()
		return nil, nil, 0, 0, err
	}
	uid, gid := fileOwner(info)
	return droot, fd, uid, gid, nil
}

func (f *Files) extractOp(ctx context.Context, _ helperapi.Caller, req helperapi.FileExtractRequest, log io.Writer) (any, error) {
	if protected(req.Root, req.Dest) && req.Dest != "" {
		return nil, protectedErr(req.Dest)
	}
	kind := archiveKind(req.Path)
	if kind == "" {
		return nil, fileErr(helperapi.CodeFileBadArchive, map[string]any{"path": req.Path})
	}
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	droot, dfd, uid, gid, err := openDest(root, req.Dest)
	if err != nil {
		return nil, err
	}
	defer func() { _ = droot.Close(); _ = dfd.Close() }()
	// The archive's directory is opened once, too: the archive (and its
	// other volumes) is read from it by name.
	adir, aname, adone, err := root.parent(req.Path)
	if err != nil {
		return nil, err
	}
	defer adone()
	x := &extractor{ctx: ctx, rootID: req.Root, src: adir, archive: req.Path, root: droot, dest: req.Dest, uid: uid, gid: gid, overwrite: req.Overwrite,
		b: &batch{log: log}, budget: spaceOf(dfd), files: map[string]bool{}}
	_, _ = fmt.Fprintf(log, "extracting %s into /%s (%s)\n", req.Path, req.Dest, kind)
	switch kind {
	case "unar":
		err = f.unarExtract(ctx, req, x, dfd, log)
	case "zip":
		err = x.zip(aname)
	case "gz", "zst", "xz", "bz2":
		err = x.single(aname, kind)
	default:
		err = x.tar(aname, kind)
	}
	if err != nil {
		var he *helperapi.Error
		if !errors.As(err, &he) && !errors.Is(err, context.Canceled) {
			err = fileErr(helperapi.CodeFileBadArchive, map[string]any{"path": req.Path, "detail": tailString(err.Error(), 200)})
		}
		return x.b.res, err
	}
	_, _ = fmt.Fprintf(log, "%d entries extracted, %d skipped\n", x.b.res.Done, len(x.b.res.Failed))
	return x.b.result()
}

// openArchive opens the archive, or another volume of it, by its name in
// the archive's directory.
func (x *extractor) openArchive(name string) (*os.File, fs.FileInfo, error) {
	p := join(parentOf(x.archive), name)
	file, fi, err := noFollow(x.src, name, p)
	if err == nil && !fi.Mode().IsRegular() {
		_ = file.Close()
		err = fileErr(helperapi.CodeFileBadArchive, map[string]any{"path": p})
	}
	return file, fi, err
}

func (x *extractor) zip(name string) error {
	file, fi, err := x.openArchive(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	zr, err := zip.NewReader(file, fi.Size())
	if err != nil {
		return err
	}
	for _, e := range zr.File {
		if err := x.count(); err != nil {
			return err
		}
		mode := e.Mode()
		switch {
		case mode.IsDir():
			err = x.dir(e.Name, mode)
		case mode&fs.ModeSymlink != 0:
			var r io.ReadCloser
			if r, err = e.Open(); err == nil {
				target, _ := io.ReadAll(io.LimitReader(r, 4096))
				_ = r.Close()
				err = x.symlink(e.Name, string(target))
			}
		case mode.IsRegular():
			var r io.ReadCloser
			if r, err = e.Open(); err == nil {
				err = x.file(e.Name, mode, r)
				_ = r.Close()
			}
		default:
			x.b.failed(e.Name, fileErr(helperapi.CodeFileProtected, map[string]any{"path": e.Name}))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (x *extractor) tar(name, kind string) error {
	file, _, err := x.openArchive(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	r, done, err := decompress(kind, file)
	if err != nil {
		return err
	}
	defer done()
	return x.tarFrom(r)
}

// tarFrom extracts a tar stream.
func (x *extractor) tarFrom(r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := x.count(); err != nil {
			return err
		}
		mode := fs.FileMode(h.Mode).Perm() //nolint:gosec // G115: tar modes fit.
		switch h.Typeflag {
		case tar.TypeDir:
			err = x.dir(h.Name, mode)
		case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // TypeRegA appears in old archives.
			err = x.file(h.Name, mode, tr)
		case tar.TypeSymlink:
			err = x.symlink(h.Name, h.Linkname)
		case tar.TypeLink:
			// A hard link becomes a copy of the entry it names, which must
			// be a regular file this archive wrote: anything else (a file
			// that was there, one reached through a link) could be someone
			// else's, read as root.
			in, serr := x.source(x.target(h.Linkname))
			if serr != nil {
				x.b.failed(h.Name, fileErr(helperapi.CodeFileNotFound, map[string]any{"path": h.Linkname}))
				continue
			}
			err = x.file(h.Name, mode, in)
			_ = in.Close()
		case tar.TypeXGlobalHeader:
			continue
		default:
			x.b.failed(h.Name, fileErr(helperapi.CodeFileProtected, map[string]any{"path": h.Name}))
		}
		if err != nil {
			return err
		}
	}
}

// single unpacks a compressed single file next to its archive's name.
func (x *extractor) single(base, kind string) error {
	file, _, err := x.openArchive(base)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	r, done, err := decompress(kind, file)
	if err != nil {
		return err
	}
	defer done()
	name := base[:len(base)-len(path.Ext(base))]
	if name == "" {
		name = "unpacked"
	}
	return x.file(name, defaultFileMod, r)
}

// unarSpaceCheck is how often the space unar leaves is looked at (tests).
var unarSpaceCheck = 500 * time.Millisecond

// spaceOf is space; tests replace it.
var spaceOf = space

// watchSpace returns a context that ends once the file system of the open
// directory d has less than floor bytes to spare, and a function that
// stops watching and reports whether that happened. d is opened once by
// the caller: nothing is looked up by path while it runs (a pipe put in
// the directory's place would block an open).
func watchSpace(ctx context.Context, d *os.File, floor int64) (context.Context, func() bool) {
	ctx, cancel := context.WithCancel(ctx)
	var hit atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(unarSpaceCheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if spaceOf(d) < floor {
					hit.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() bool {
		cancel()
		<-done
		return hit.Load()
	}
}

// unar runs in a transient unit as unpackUser, with only its own scratch
// directory (below unpackDir) visible and writable: nothing it starts
// outlives the run, and one run cannot see another's files. The account
// is made by the package.
const (
	unpackUser = "cairn-unpack"
	unpackDir  = "/var/lib/cairn-unpack"
)

func unpackErr(reason string, params map[string]any) error {
	if params == nil {
		params = map[string]any{}
	}
	params["reason"] = reason
	return fileErr(helperapi.CodeFileUnpackFailed, params)
}

// volumes lists the archive's name (base, in the open directory dir) and,
// for the first part of a multi-volume RAR (x.part1.rar), the other parts
// next to it.
func volumes(dir *os.Root, base string) []string {
	lower := strings.ToLower(base)
	i := strings.LastIndex(lower, ".part")
	if i < 0 || !strings.HasSuffix(lower, ".rar") || strings.Trim(lower[i+5:len(lower)-4], "0") != "1" {
		return []string{base}
	}
	prefix := lower[:i+5]
	names, _ := dirNames(dir)
	out := []string{base}
	for _, n := range names {
		l := strings.ToLower(n)
		if n == base || !strings.HasPrefix(l, prefix) || !strings.HasSuffix(l, ".rar") {
			continue
		}
		if num := l[len(prefix) : len(l)-4]; num != "" && strings.Trim(num, "0123456789") == "" {
			out = append(out, n)
		}
	}
	return out
}

// unarExtract has unar unpack in a scratch directory of its own, as
// unpackUser in a transient unit: the archive (and its other volumes) is
// copied there first, so unar reads nothing but the scratch directory and
// needs no access to where the archive is or where it goes. What it made
// then goes into the destination through the same checks as any other
// entry. The copy and unar's output count against the space: of the
// scratch file system, and of the destination (half of it when both are
// the same file system, so that the copy out still fits).
func (f *Files) unarExtract(ctx context.Context, req helperapi.FileExtractRequest, x *extractor, dfd *os.File, log io.Writer) error {
	if _, err := os.Stat(f.path(unarBinary)); err != nil {
		return fileErr(helperapi.CodeFileToolMissing, map[string]any{"tool": "unar"})
	}
	if !systemdRunning() {
		_, _ = fmt.Fprintln(log, "Extracting 7z and RAR archives needs systemd (unar runs in a transient unit), and this system does not run it.")
		return fileErr(helperapi.CodeFileSystemd, nil)
	}
	uuid, ugid, err := lookupUser(unpackUser)
	if err != nil || uuid <= 0 || ugid <= 0 {
		return unpackErr("account", map[string]any{"user": unpackUser})
	}
	if x.budget <= 0 {
		return errNoSpace
	}
	// The scratch directories: their parent traversable by unpackUser's
	// group only, not listable; each one unpackUser's own.
	base := f.path(unpackDir)
	if err := os.Mkdir(base, 0o710); err != nil && !errors.Is(err, fs.ErrExist) {
		return unpackErr("scratch", map[string]any{"detail": err.Error()})
	}
	if fi, err := os.Lstat(base); err != nil || !fi.IsDir() {
		return unpackErr("scratch", map[string]any{"path": unpackDir})
	}
	if err := os.Lchown(base, os.Geteuid(), ugid); err != nil {
		return unpackErr("scratch", map[string]any{"detail": err.Error()})
	}
	if err := os.Chmod(base, 0o710); err != nil { //nolint:gosec // G302: a directory, traversable by the unpacking group only.
		return unpackErr("scratch", map[string]any{"detail": err.Error()})
	}
	broot, err := os.OpenRoot(base)
	if err != nil {
		return unpackErr("scratch", map[string]any{"detail": err.Error()})
	}
	defer func() { _ = broot.Close() }()
	id := randomName("")
	if err := broot.Mkdir(id, 0o700); err != nil {
		return unpackErr("scratch", map[string]any{"detail": err.Error()})
	}
	defer func() { _ = broot.RemoveAll(id) }()
	info, err := broot.Lstat(id)
	if err != nil {
		return err
	}
	run, err := enterDir(broot, id, info)
	if err != nil {
		return unpackErr("scratch", nil)
	}
	defer func() { _ = run.Close() }()
	sfd, err := run.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = sfd.Close() }()
	if err := run.Mkdir("out", 0o700); err != nil {
		return err
	}

	// The archive and its other volumes, copied in.
	avail := spaceOf(sfd)
	names := volumes(x.src, path.Base(req.Path))
	for _, n := range names {
		in, fi, err := x.openArchive(n)
		if err != nil {
			if n == names[0] {
				return err
			}
			continue
		}
		if avail -= fi.Size(); avail < 0 {
			_ = in.Close()
			return errNoSpace
		}
		out, err := run.OpenFile(n, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o440)
		if err == nil {
			_, err = io.Copy(out, io.LimitReader(in, fi.Size()))
			if err == nil {
				err = out.Chown(os.Geteuid(), ugid)
			}
			if err == nil {
				err = out.Chmod(0o440)
			}
			err = errors.Join(err, out.Close())
		}
		_ = in.Close()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return unpackErr("copy", map[string]any{"path": req.Path, "detail": tailString(err.Error(), 200)})
		}
	}
	for _, n := range []string{".", "out"} {
		if err := run.Lchown(n, uuid, ugid); err != nil {
			return err
		}
	}

	// unar may use what both file systems have left, halved when they
	// are one (its output is copied out next to it).
	allowed := min(x.budget, spaceOf(sfd))
	if sameFS(sfd, dfd) {
		allowed /= 2
	}
	if allowed <= 0 {
		return errNoSpace
	}
	runPath := path.Join(base, id)
	uctx, stop := watchSpace(ctx, sfd, spaceOf(sfd)-allowed)
	_, err = runInUnit(uctx, unitRun{
		Cmd: execx.Cmd{Name: unarBinary, Args: []string{"-q", "-s", "-D", "-nr", "-o", path.Join(runPath, "out"), path.Join(runPath, names[0])},
			Output: log, Timeout: extractTimeout, MaxOutput: 64 << 10},
		User: unpackUser,
		// Of the scratch directories only this run's is there.
		Properties: []string{"PrivateNetwork=yes", "ProtectHome=yes", "TemporaryFileSystem=" + base, "BindPaths=" + runPath},
	})
	if stop() {
		return errNoSpace
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The unit's own output is in the log already; how the run ended
		// is not, and when systemd-run could not run at all nothing is.
		var ee *execx.ExitError
		if errors.As(err, &ee) {
			_, _ = fmt.Fprintf(log, "unar's unit ended with exit status %d.\n", ee.ExitCode)
		} else {
			_, _ = fmt.Fprintf(log, "unar's unit could not run: %s\n", tailString(err.Error(), 400))
		}
		return fileErr(helperapi.CodeFileBadArchive, map[string]any{"path": req.Path, "detail": errDetail(err)})
	}
	// The scratch directory is root's again before it is walked; what
	// unar made is walked one level at a time (see walkTree).
	if err := run.Lchown(".", os.Geteuid(), os.Getegid()); err != nil {
		return err
	}
	if err := run.Chmod(".", 0o700); err != nil {
		return err
	}
	x.budget = spaceOf(dfd)
	return walkTree(ctx, run, "out", func(e walkEntry) error {
		if e.Rel == "" {
			return nil
		}
		if err := x.count(); err != nil {
			return err
		}
		name := e.Rel
		switch m := e.Info.Mode(); {
		case m.IsDir():
			return x.dir(name, m)
		case m&fs.ModeSymlink != 0:
			target, err := e.Dir.Readlink(e.Name)
			if err != nil {
				return err
			}
			return x.symlink(name, target)
		case m.IsRegular():
			in, fi, err := e.open()
			if errors.Is(err, errSwapped) {
				x.b.failed(name, fileErr(helperapi.CodeFileProtected, map[string]any{"path": name}))
				return nil
			}
			if err != nil {
				return err
			}
			defer func() { _ = in.Close() }()
			// A file with another name that is not unar's was linked in,
			// not unpacked: it could be anyone's.
			if foreignLink(fi, uuid) {
				x.b.failed(name, fileErr(helperapi.CodeFileProtected, map[string]any{"path": name}))
				return nil
			}
			return x.file(name, fi.Mode(), in)
		}
		x.b.failed(name, fileErr(helperapi.CodeFileProtected, map[string]any{"path": name}))
		return nil
	})
}

// sameFS reports whether two open directories are on one file system.
func sameFS(a, b *os.File) bool {
	ai, aerr := a.Stat()
	bi, berr := b.Stat()
	if aerr != nil || berr != nil {
		return true
	}
	as, aok := ai.Sys().(*syscall.Stat_t)
	bs, bok := bi.Sys().(*syscall.Stat_t)
	return !aok || !bok || as.Dev == bs.Dev
}

// Compress ----------------------------------------------------------------

func (f *Files) compressOp(ctx context.Context, _ helperapi.Caller, req helperapi.FileCompressRequest, log io.Writer) (any, error) {
	out := join(req.Dir, req.Name)
	if protected(req.Root, out) {
		return nil, protectedErr(out)
	}
	root, err := f.openRoot(req.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	// The directory is opened once: its owner, every name archived, the
	// archive being written and its final name are all taken from that
	// open directory, whatever its path leads to meanwhile.
	dir, err := root.dir(req.Dir, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	uid, gid, err := dirOwner(dir)
	if err != nil {
		return nil, err
	}
	exists := fileErr(helperapi.CodeFileExists, map[string]any{"path": out})
	if _, err := dir.Lstat(req.Name); err == nil {
		return nil, exists
	}
	tmp := randomName(".cairn-archive-")
	file, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
			_ = dir.Remove(tmp)
		}
	}()
	var add func(name string, fi fs.FileInfo, target string, r io.Reader) error
	var finish func() error
	if req.Format == "zip" {
		zw := zip.NewWriter(file)
		finish = zw.Close
		add = func(name string, fi fs.FileInfo, target string, r io.Reader) error {
			h, err := zip.FileInfoHeader(fi)
			if err != nil {
				return err
			}
			h.Name = name
			if fi.IsDir() {
				h.Name += "/"
				h.Method = zip.Store
			} else {
				h.Method = zip.Deflate
			}
			w, err := zw.CreateHeader(h)
			if err != nil {
				return err
			}
			switch {
			case target != "":
				_, err = io.WriteString(w, target)
			case r != nil:
				_, err = io.Copy(w, r)
			}
			return err
		}
	} else {
		var cw io.WriteCloser
		switch req.Format {
		case "tar.gz":
			cw = gzip.NewWriter(file)
		case "tar.zst":
			if cw, err = zstd.NewWriter(file); err != nil {
				return nil, err
			}
		default:
			if cw, err = xz.NewWriter(file); err != nil {
				return nil, err
			}
		}
		tw := tar.NewWriter(cw)
		finish = func() error { return errors.Join(tw.Close(), cw.Close()) }
		add = func(name string, fi fs.FileInfo, target string, r io.Reader) error {
			h, err := tar.FileInfoHeader(fi, target)
			if err != nil {
				return err
			}
			h.Name, h.Format = name, tar.FormatPAX
			if fi.IsDir() {
				h.Name += "/"
			}
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			if r != nil {
				err = copyN(tw, r, h.Size)
			}
			return err
		}
	}
	count := 0
	// The archive is not world-readable, and only its owner reads it when
	// anything in it was not world-readable either.
	mode := fs.FileMode(0o640)
	for _, n := range req.Names {
		start := join(req.Dir, n)
		if n == req.Name || protected(req.Root, start) {
			return nil, protectedErr(start)
		}
		// A link named here is archived as the link, not the tree it
		// points to; the tree is walked one level at a time (see
		// walkTree).
		err := walkNamed(ctx, dir, n, start, false, func(e walkEntry) error {
			name := n
			if e.Rel != "" {
				name = join(n, e.Rel)
			}
			info := e.Info
			count++
			switch t := info.Mode(); {
			case t&fs.ModeSymlink != 0:
				target, err := e.Dir.Readlink(e.Name)
				if err != nil {
					return err
				}
				return add(name, info, target, nil)
			case t.IsDir():
				if info.Mode().Perm()&0o004 == 0 {
					mode = 0o600
				}
				return add(name, info, "", nil)
			case t.IsRegular():
				in, fi, err := e.open()
				if errors.Is(err, errSwapped) {
					_, _ = fmt.Fprintf(log, "  skipped %s (it changed meanwhile)\n", e.Path)
					return nil
				}
				if err != nil {
					return err
				}
				defer func() { _ = in.Close() }()
				if fi.Mode().Perm()&0o004 == 0 {
					mode = 0o600
				}
				// The header takes the open file's size.
				return add(name, fi, "", in)
			}
			_, _ = fmt.Fprintf(log, "  skipped %s (not a file, directory or link)\n", e.Path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if err := finish(); err != nil {
		return nil, err
	}
	if err := file.Chown(uid, gid); err != nil {
		return nil, err
	}
	if err := file.Chmod(mode); err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	// Never over an entry made there since the look.
	if err := renameAt(dir, tmp, dir, req.Name, true); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, exists
		}
		return nil, err
	}
	ok = true
	fi, err := dir.Lstat(req.Name)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "%s: %d entries, %d bytes\n", out, count, fi.Size())
	return entryOf(dir, req.Name, out, fi, newNames()), nil
}

// copyN copies exactly n bytes of r to w: a file that grew since its size
// was taken is cut at n, one that shrank is padded with zeros, so that a
// tar entry matches its header and the rest of the archive is not lost.
func copyN(w io.Writer, r io.Reader, n int64) error {
	m, err := io.Copy(w, io.LimitReader(r, n))
	if err == nil && m < n {
		_, err = io.CopyN(w, zeros{}, n-m)
	}
	return err
}

// zeros reads as endless zero bytes.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
