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

// extractor writes entries below dest, for its owner.
type extractor struct {
	ctx       context.Context
	rootID    string
	root      *os.Root
	dest      string
	uid, gid  int
	overwrite bool
	b         *batch
	budget    int64
	entries   int
	dirs      map[string]bool
}

var errNoSpace = fileErr(helperapi.CodeFileNoSpace, nil)

// target maps an entry name to its path, or "" when it would leave dest.
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
	p := join(x.dest, name)
	if protected(x.rootID, p) {
		return ""
	}
	return p
}

// mkdirs makes sure dir (below dest) is a chain of real directories:
// links in the way are refused, never followed.
func (x *extractor) mkdirs(dir string) error {
	if dir == x.dest || x.dirs[dir] {
		return nil
	}
	if err := x.mkdirs(parentOf(dir)); err != nil {
		return err
	}
	fi, err := x.root.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := x.root.Mkdir(dir, defaultDirMod); err != nil {
			return err
		}
		if err := x.root.Lchown(dir, x.uid, x.gid); err != nil {
			return err
		}
	case err != nil:
		return err
	case !fi.IsDir():
		return fileErr(helperapi.CodeFileNotDir, map[string]any{"path": dir})
	}
	x.dirs[dir] = true
	return nil
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

// free makes room for an entry; false when it is skipped.
func (x *extractor) free(p string) (bool, error) {
	fi, err := x.root.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !x.overwrite {
		x.b.failed(p, fileErr(helperapi.CodeFileExists, map[string]any{"path": p}))
		return false, nil
	}
	if fi.IsDir() {
		x.b.failed(p, fileErr(helperapi.CodeFileIsDir, map[string]any{"path": p}))
		return false, nil
	}
	return true, x.root.Remove(p)
}

func (x *extractor) dir(name string, mode fs.FileMode) error {
	p := x.target(name)
	if p == "" {
		x.b.failed(name, fileErr(helperapi.CodeFileProtected, map[string]any{"path": name}))
		return nil
	}
	if err := x.mkdirs(p); err != nil {
		x.b.failed(name, err)
		return nil
	}
	_ = x.root.Chmod(p, mode.Perm()|0o700)
	return nil
}

func (x *extractor) file(name string, mode fs.FileMode, r io.Reader) error {
	p := x.target(name)
	if p == "" {
		x.b.failed(name, fileErr(helperapi.CodeFileProtected, map[string]any{"path": name}))
		return nil
	}
	if err := x.mkdirs(parentOf(p)); err != nil {
		x.b.failed(name, err)
		return nil
	}
	ok, err := x.free(p)
	if err != nil || !ok {
		return err
	}
	out, err := x.root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
		_ = x.root.Remove(p)
		return err
	}
	x.b.res.Done++
	return nil
}

func (x *extractor) symlink(name, target string) error {
	p := x.target(name)
	if p == "" {
		x.b.failed(name, fileErr(helperapi.CodeFileProtected, map[string]any{"path": name}))
		return nil
	}
	resolved := path.Clean(path.Join(parentOf(p), target))
	if path.IsAbs(target) || target == "" || resolved == ".." || strings.HasPrefix(resolved, "../") || !within(resolved, x.dest) {
		x.b.failed(name, fileErr(helperapi.CodeFileIsLink, map[string]any{"path": name, "target": target}))
		return nil
	}
	if err := x.mkdirs(parentOf(p)); err != nil {
		x.b.failed(name, err)
		return nil
	}
	ok, err := x.free(p)
	if err != nil || !ok {
		return err
	}
	if err := x.root.Symlink(target, p); err != nil {
		x.b.failed(name, err)
		return nil
	}
	x.b.res.Done++
	return x.root.Lchown(p, x.uid, x.gid)
}

// space is what an extraction may write: the free space of the file
// system minus a reserve.
func space(root *os.Root, dir string) int64 {
	d, err := root.Open(rel(dir))
	if err != nil {
		return 0
	}
	defer func() { _ = d.Close() }()
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(d.Fd()), &st); err != nil {
		return 0
	}
	free := int64(st.Bavail) * st.Bsize                    //nolint:gosec // G115: block counts fit.
	reserve := max(int64(st.Blocks)*st.Bsize/100, 256<<20) //nolint:gosec // G115: block counts fit.
	return free - reserve
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
	uid, gid, err := ownerOfDir(root, req.Dest)
	if err != nil {
		return nil, err
	}
	x := &extractor{ctx: ctx, rootID: req.Root, root: root, dest: req.Dest, uid: uid, gid: gid, overwrite: req.Overwrite, b: &batch{log: log},
		budget: space(root, req.Dest), dirs: map[string]bool{}}
	_, _ = fmt.Fprintf(log, "extracting %s into /%s (%s)\n", req.Path, req.Dest, kind)
	switch kind {
	case "unar":
		err = f.unarExtract(ctx, root, req, x, log)
	case "zip":
		err = x.zip(req.Path)
	case "gz", "zst", "xz", "bz2":
		err = x.single(req.Path, kind)
	default:
		err = x.tar(req.Path, kind)
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

func (x *extractor) openArchive(p string) (*os.File, fs.FileInfo, error) {
	file, fi, err := noFollow(x.root, p)
	if err == nil && !fi.Mode().IsRegular() {
		_ = file.Close()
		err = fileErr(helperapi.CodeFileBadArchive, map[string]any{"path": p})
	}
	return file, fi, err
}

func (x *extractor) zip(p string) error {
	file, fi, err := x.openArchive(p)
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

func (x *extractor) tar(p, kind string) error {
	file, _, err := x.openArchive(p)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	r, done, err := decompress(kind, file)
	if err != nil {
		return err
	}
	defer done()
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
			// A hard link becomes a copy of the entry it names.
			src := x.target(h.Linkname)
			var in *os.File
			if src != "" {
				in, _, err = noFollow(x.root, src)
			}
			if src == "" || err != nil {
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
func (x *extractor) single(p, kind string) error {
	file, _, err := x.openArchive(p)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	r, done, err := decompress(kind, file)
	if err != nil {
		return err
	}
	defer done()
	base := path.Base(p)
	name := base[:len(base)-len(path.Ext(base))]
	if name == "" {
		name = "unpacked"
	}
	return x.file(name, defaultFileMod, r)
}

// unarExtract has unar unpack into a scratch directory, as the
// destination's owner, then moves what it made through the same checks.
func (f *Files) unarExtract(ctx context.Context, root *os.Root, req helperapi.FileExtractRequest, x *extractor, log io.Writer) error {
	if _, err := os.Stat(f.path(unarBinary)); err != nil {
		return fileErr(helperapi.CodeFileToolMissing, map[string]any{"tool": "unar"})
	}
	if _, _, err := x.openArchive(req.Path); err != nil {
		return err
	}
	scratch := join(req.Dest, randomName(".cairn-extract-"))
	if err := root.Mkdir(scratch, 0o700); err != nil {
		return err
	}
	defer func() { _ = root.RemoveAll(scratch) }()
	if err := root.Lchown(scratch, x.uid, x.gid); err != nil {
		return err
	}
	base := f.path(f.rootPath(req.Root))
	var runAs *execx.RunAs
	if x.uid != 0 {
		runAs = &execx.RunAs{Uid: uint32(x.uid), Gid: uint32(x.gid)} //nolint:gosec // G115: ids fit.
	}
	_, err := dbRun(ctx, execx.Cmd{Name: unarBinary, Args: []string{"-q", "-s", "-D", "-nr", "-o", path.Join(base, scratch), path.Join(base, req.Path)},
		RunAs: runAs, Output: log, Timeout: extractTimeout, MaxOutput: 64 << 10})
	if err != nil {
		return fileErr(helperapi.CodeFileBadArchive, map[string]any{"path": req.Path, "detail": errDetail(err)})
	}
	return fs.WalkDir(root.FS(), scratch, func(q string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if q == scratch {
			return nil
		}
		if err := x.count(); err != nil {
			return err
		}
		name := strings.TrimPrefix(q, scratch+"/")
		switch t := d.Type(); {
		case t.IsDir():
			info, ierr := d.Info()
			if ierr != nil {
				return ierr
			}
			return x.dir(name, info.Mode())
		case t&fs.ModeSymlink != 0:
			target, rerr := root.Readlink(q)
			if rerr != nil {
				return rerr
			}
			return x.symlink(name, target)
		case t.IsRegular():
			in, fi, oerr := noFollow(root, q)
			if oerr != nil {
				return oerr
			}
			defer func() { _ = in.Close() }()
			return x.file(name, fi.Mode(), in)
		}
		x.b.failed(name, fileErr(helperapi.CodeFileProtected, map[string]any{"path": name}))
		return nil
	})
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
	uid, gid, err := ownerOfDir(root, req.Dir)
	if err != nil {
		return nil, err
	}
	if _, err := root.Lstat(out); err == nil {
		return nil, fileErr(helperapi.CodeFileExists, map[string]any{"path": out})
	}
	tmp := join(req.Dir, randomName(".cairn-archive-"))
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
			_ = root.Remove(tmp)
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
				_, err = io.Copy(tw, r)
			}
			return err
		}
	}
	count := 0
	for _, n := range req.Names {
		start := join(req.Dir, n)
		if n == req.Name || protected(req.Root, start) {
			return nil, protectedErr(start)
		}
		err := fs.WalkDir(root.FS(), start, func(q string, d fs.DirEntry, err error) error {
			if err != nil {
				return notFound(err, q)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			name := strings.TrimPrefix(q, req.Dir+"/")
			if req.Dir == "" {
				name = q
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			count++
			switch t := d.Type(); {
			case t&fs.ModeSymlink != 0:
				target, err := root.Readlink(q)
				if err != nil {
					return err
				}
				return add(name, info, target, nil)
			case t.IsDir():
				return add(name, info, "", nil)
			case t.IsRegular():
				in, _, err := noFollow(root, q)
				if err != nil {
					return err
				}
				defer func() { _ = in.Close() }()
				return add(name, info, "", in)
			}
			_, _ = fmt.Fprintf(log, "  skipped %s (not a file, directory or link)\n", q)
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
	if err := file.Chmod(defaultFileMod); err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := root.Rename(tmp, out); err != nil {
		return nil, err
	}
	ok = true
	fi, err := root.Lstat(out)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "%s: %d entries, %d bytes\n", out, count, fi.Size())
	return entryOf(root, out, fi, newNames()), nil
}
