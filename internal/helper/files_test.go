// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

type filesEnv struct {
	t *testing.T
	f *Files
	o *Ops
	r *Registry
}

func newFilesEnv(t *testing.T) *filesEnv {
	t.Helper()
	root := t.TempDir()
	f := &Files{Root: root, DataDir: "/var/lib/cairn", PanelUser: "cairn", StateDir: filepath.Join(root, "/var/lib/cairn-helper"),
		RootAccess: "panel", Log: slog.New(slog.DiscardHandler)}
	for _, d := range []string{"/srv/www/shop/app", "/var/lib/cairn/uploads", "/etc"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	o, r := newTestOps(t)
	RegisterFiles(r, o, f)
	uid, gid := os.Getuid(), os.Getgid()
	old := lookupUser
	t.Cleanup(func() { lookupUser = old })
	lookupUser = func(string) (int, int, error) { return uid, gid, nil }
	return &filesEnv{t: t, f: f, o: o, r: r}
}

func (e *filesEnv) put(p, content string) {
	e.t.Helper()
	writeSystemFile(e.t, e.f.Root, p, content)
}

func (e *filesEnv) read(p string) string {
	data, _ := os.ReadFile(filepath.Join(e.f.Root, p))
	return string(data)
}

func (e *filesEnv) exists(p string) bool {
	_, err := os.Lstat(filepath.Join(e.f.Root, p))
	return err == nil
}

func (e *filesEnv) run(action string, req any) (helperapi.Op, helperapi.FileOpResult, string) {
	e.t.Helper()
	ref, err := call[helperapi.OpRef](e.t, e.r, action, req)
	if err != nil {
		e.t.Fatalf("%s: %v", action, err)
	}
	op := waitOp(e.t, e.o, ref.ID, helperapi.OpSucceeded, helperapi.OpFailed)
	got, _ := e.o.Get(ref.ID, 0)
	var res helperapi.FileOpResult
	if len(op.Result) > 0 {
		decode(e.t, op.Result, &res)
	}
	return op, res, got.Log
}

func errCode(err error) string {
	if he, ok := helperapi.AsError(err); ok {
		return he.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestFileRootsAccessAndListing(t *testing.T) {
	e := newFilesEnv(t)
	roots, _ := call[helperapi.FileRoots](t, e.r, helperapi.ActionFileRoots, helperapi.Empty{})
	if !roots.Roots[0].Available || roots.Roots[2].Available || roots.Settings.FullDisk {
		t.Fatalf("roots %+v", roots)
	}
	if _, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "system"}); errCode(err) != helperapi.CodeFileRootUnavailable {
		t.Fatalf("system root without full-disk access: %v", err)
	}
	if s, err := call[helperapi.AccessSettings](t, e.r, helperapi.ActionAccessSet, helperapi.AccessSettings{FullDisk: true}); err != nil || !s.FullDisk {
		t.Fatalf("access %+v %v", s, err)
	}
	if l, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "system", Path: "srv/www"}); err != nil || len(l.Entries) != 1 {
		t.Fatalf("system root: %+v %v", l, err)
	}
	// The server's configuration can forbid it outright.
	e.f.RootAccess = "never"
	if _, err := call[helperapi.AccessSettings](t, e.r, helperapi.ActionAccessSet, helperapi.AccessSettings{RootTerminal: true}); errCode(err) != helperapi.CodeAccessLocked {
		t.Fatalf("locked: %v", err)
	}
	if _, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "system"}); errCode(err) != helperapi.CodeFileRootUnavailable {
		t.Fatalf("system root when locked: %v", err)
	}

	e.put("/srv/www/shop/app/index.php", "<?php echo 1;")
	e.put("/srv/www/shop/app/.env", "SECRET=1")
	e.put("/srv/www/.cairn-trash/x.json", "{}")
	if err := os.Mkdir(filepath.Join(e.f.Root, "/srv/www/shop/app/assets"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(e.f.Root, "/srv/www/shop/app/etc")); err != nil {
		t.Fatal(err)
	}
	l, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "sites", Path: "shop/app"})
	if err != nil || len(l.Entries) != 3 || l.Entries[0].Name != "assets" || l.Entries[1].Type != helperapi.FileTypeLink || l.Entries[1].Target != "/etc" {
		t.Fatalf("listing %+v %v", l, err)
	}
	if l, _ := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "sites", Path: "shop/app", Hidden: true}); len(l.Entries) != 4 {
		t.Fatalf("hidden files: %+v", l.Entries)
	}
	if l, _ := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "sites", Hidden: true}); len(l.Entries) != 1 {
		t.Fatalf("the trash is never listed: %+v", l.Entries)
	}
	// A link out of the root leads nowhere.
	if _, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "sites", Path: "shop/app/etc"}); errCode(err) != helperapi.CodeFileNotFound {
		t.Fatalf("listing through a link out of the root: %v", err)
	}
	if _, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, helperapi.FileRef{Root: "sites", Path: "shop/app/etc/passwd"}); errCode(err) != helperapi.CodeFileNotFound {
		t.Fatalf("reading through a link out of the root: %v", err)
	}
	if _, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "sites", Path: "../etc"}); err == nil {
		t.Fatal("dot-dot paths must be refused")
	}
}

func TestFileReadWriteAndOwners(t *testing.T) {
	e := newFilesEnv(t)
	asRoot := os.Getuid() == 0
	if asRoot {
		if err := os.Chown(filepath.Join(e.f.Root, "/srv/www/shop/app"), 1234, 1235); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := call[helperapi.FileSaved](t, e.r, helperapi.ActionFileWrite, helperapi.FileWriteRequest{Root: "sites", Path: "shop/app/a.txt", Content: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if w := saved.Entry; asRoot && (w.Owner != "1234" || w.Group != "1235") {
		t.Fatalf("a new file belongs to the directory's owner: %+v", w)
	}
	// The answer carries the new version: saving again with it works.
	if _, err := call[helperapi.FileSaved](t, e.r, helperapi.ActionFileWrite, helperapi.FileWriteRequest{Root: "sites", Path: "shop/app/a.txt", Content: "one", ETag: saved.ETag}); err != nil {
		t.Fatalf("saving with the returned etag: %v", err)
	}
	if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileWrite, helperapi.FileWriteRequest{Root: "sites", Path: "shop/app/a.txt", Content: "x"}); errCode(err) != helperapi.CodeFileExists {
		t.Fatalf("creating over a file: %v", err)
	}
	c, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, helperapi.FileRef{Root: "sites", Path: "shop/app/a.txt"})
	if err != nil || c.Content != "one" || c.ETag == "" {
		t.Fatalf("read %+v %v", c, err)
	}
	if err := os.Chmod(filepath.Join(e.f.Root, "/srv/www/shop/app/a.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileWrite, helperapi.FileWriteRequest{Root: "sites", Path: "shop/app/a.txt", Content: "two", ETag: c.ETag}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(e.f.Root, "/srv/www/shop/app/a.txt")); e.read("/srv/www/shop/app/a.txt") != "two" || fi.Mode().Perm() != 0o600 {
		t.Fatalf("save: %q %v", e.read("/srv/www/shop/app/a.txt"), fi.Mode())
	}
	// Saving over a version changed meanwhile fails.
	if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileWrite, helperapi.FileWriteRequest{Root: "sites", Path: "shop/app/a.txt", Content: "three", ETag: c.ETag}); errCode(err) != helperapi.CodeFileChanged {
		t.Fatalf("stale save: %v", err)
	}
	e.put("/srv/www/shop/app/bin.dat", "\x00\x01\x02")
	if _, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, helperapi.FileRef{Root: "sites", Path: "shop/app/bin.dat"}); errCode(err) != helperapi.CodeFileBinary {
		t.Fatalf("binary: %v", err)
	}
	if err := os.Symlink("a.txt", filepath.Join(e.f.Root, "/srv/www/shop/app/link")); err != nil {
		t.Fatal(err)
	}
	c, _ = call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, helperapi.FileRef{Root: "sites", Path: "shop/app/link"})
	if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileWrite, helperapi.FileWriteRequest{Root: "sites", Path: "shop/app/link", Content: "x", ETag: c.ETag}); errCode(err) != helperapi.CodeFileIsLink {
		t.Fatalf("saving through a link: %v", err)
	}
	// The file itself, for downloads.
	entry, file, err := e.f.open(context.Background(), helperapi.Caller{}, helperapi.FileRef{Root: "sites", Path: "shop/app/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(file)
	_ = file.Close()
	if string(data) != "two" || entry.Name != "a.txt" {
		t.Fatalf("open: %q %+v", data, entry)
	}
	if _, _, err := e.f.open(context.Background(), helperapi.Caller{}, helperapi.FileRef{Root: "sites", Path: "shop/app"}); errCode(err) != helperapi.CodeFileIsDir {
		t.Fatalf("opening a directory: %v", err)
	}

	d, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileMkdir, helperapi.FileNewRequest{Root: "sites", Dir: "shop/app", Name: "uploads"})
	if err != nil || d.Type != helperapi.FileTypeDir || (asRoot && d.Owner != "1234") {
		t.Fatalf("mkdir %+v %v", d, err)
	}
	if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileTouch, helperapi.FileNewRequest{Root: "sites", Dir: "", Name: ".cairn-trash"}); errCode(err) != helperapi.CodeFileProtected {
		t.Fatalf("making the trash by hand: %v", err)
	}
	if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileRename, helperapi.FileRenameRequest{Root: "sites", Path: "shop/app/a.txt", Name: "uploads"}); errCode(err) != helperapi.CodeFileExists {
		t.Fatalf("rename over an entry: %v", err)
	}
	if r, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileRename, helperapi.FileRenameRequest{Root: "sites", Path: "shop/app/a.txt", Name: "b.txt"}); err != nil || r.Path != "shop/app/b.txt" {
		t.Fatalf("rename %+v %v", r, err)
	}

	s, err := call[helperapi.FileSearchResult](t, e.r, helperapi.ActionFileSearch, helperapi.FileSearchRequest{Root: "sites", Query: "TWO", Content: true})
	if err != nil || len(s.Matches) != 1 || s.Matches[0].Path != "shop/app/b.txt" || s.Matches[0].Line != 1 {
		t.Fatalf("search %+v %v", s, err)
	}
	if s, _ := call[helperapi.FileSearchResult](t, e.r, helperapi.ActionFileSearch, helperapi.FileSearchRequest{Root: "sites", Query: "UPLOAD", CaseSensitive: true}); len(s.Matches) != 0 {
		t.Fatalf("case-sensitive search %+v", s)
	}
}

func TestFileCopyMoveTrashAndModes(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/shop/app/src/a.txt", "a")
	e.put("/srv/www/shop/app/src/sub/b.txt", "b")
	if err := os.Symlink("a.txt", filepath.Join(e.f.Root, "/srv/www/shop/app/src/l")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.f.Root, "/srv/www/blog/app"), 0o750); err != nil {
		t.Fatal(err)
	}
	if op, _, _ := e.run(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/src"}, Dest: "shop/app/src/sub"}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeFileIntoItself {
		t.Fatalf("copy into itself: %+v", op)
	}
	if op, res, log := e.run(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/src"}, Dest: "blog/app"}); op.State != helperapi.OpSucceeded || res.Done != 1 {
		t.Fatalf("copy: %+v %+v\n%s", op, res, log)
	}
	if e.read("/srv/www/blog/app/src/sub/b.txt") != "b" {
		t.Fatal("copied file")
	}
	if target, _ := os.Readlink(filepath.Join(e.f.Root, "/srv/www/blog/app/src/l")); target != "a.txt" {
		t.Fatalf("copied link %q", target)
	}
	if op, _, _ := e.run(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/src"}, Dest: "blog/app"}); op.State != helperapi.OpFailed {
		t.Fatalf("copy over without overwrite: %+v", op)
	}
	if op, _, _ := e.run(helperapi.ActionFileMove, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"blog/app/src/a.txt"}, Dest: "blog/app"}); op.State != helperapi.OpSucceeded || !e.exists("/srv/www/blog/app/a.txt") {
		t.Fatalf("move: %+v", op)
	}

	// Deleting moves to the trash; restore puts it back; purge removes it.
	if op, _, log := e.run(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "sites", Paths: []string{"blog/app/src", "blog/app/a.txt"}}); op.State != helperapi.OpSucceeded {
		t.Fatalf("delete: %+v\n%s", op, log)
	}
	if e.exists("/srv/www/blog/app/src") {
		t.Fatal("deleted directory still there")
	}
	trash, _ := call[helperapi.TrashList](t, e.r, helperapi.ActionTrashList, helperapi.FileRootRef{Root: "sites"})
	if len(trash.Entries) != 2 || trash.Entries[0].User != "admin" {
		t.Fatalf("trash %+v", trash)
	}
	var dirID, fileID string
	for _, te := range trash.Entries {
		if te.Path == "blog/app/src" {
			dirID = te.ID
		} else {
			fileID = te.ID
		}
	}
	if op, _, _ := e.run(helperapi.ActionTrashRestore, helperapi.TrashRequest{Root: "sites", IDs: []string{dirID}}); op.State != helperapi.OpSucceeded || e.read("/srv/www/blog/app/src/sub/b.txt") != "b" {
		t.Fatalf("restore: %+v", op)
	}
	if op, _, _ := e.run(helperapi.ActionTrashPurge, helperapi.TrashPurgeRequest{Root: "sites", IDs: []string{fileID}}); op.State != helperapi.OpSucceeded {
		t.Fatalf("purge: %+v", op)
	}
	if trash, _ := call[helperapi.TrashList](t, e.r, helperapi.ActionTrashList, helperapi.FileRootRef{Root: "sites"}); len(trash.Entries) != 0 {
		t.Fatalf("trash after purge %+v", trash)
	}
	if op, _, _ := e.run(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "sites", Paths: []string{".cairn-trash"}}); op.State != helperapi.OpFailed {
		t.Fatalf("deleting the trash: %+v", op)
	}
	if op, _, _ := e.run(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "sites", Paths: []string{"blog/app/src"}, Permanent: true}); op.State != helperapi.OpSucceeded || e.exists("/srv/www/blog/app/src") {
		t.Fatalf("permanent delete: %+v", op)
	}

	// Permissions: setuid is refused, setgid only on directories.
	if _, err := call[helperapi.OpRef](t, e.r, helperapi.ActionFileChmod, helperapi.FileChmodRequest{Root: "sites", Paths: []string{"shop/app/src"}, Mode: 0o4755}); err == nil {
		t.Fatal("setuid must be refused")
	}
	if op, _, _ := e.run(helperapi.ActionFileChmod, helperapi.FileChmodRequest{Root: "sites", Paths: []string{"shop/app/src"}, Mode: 0o2640, DirMode: 0o2750, Recursive: true}); op.State != helperapi.OpSucceeded {
		t.Fatalf("chmod: %+v", op)
	}
	fi, _ := os.Stat(filepath.Join(e.f.Root, "/srv/www/shop/app/src/sub/b.txt"))
	di, _ := os.Stat(filepath.Join(e.f.Root, "/srv/www/shop/app/src/sub"))
	if fi.Mode() != 0o640 || di.Mode()&os.ModeSetgid == 0 || di.Mode().Perm() != 0o750 {
		t.Fatalf("modes: file %v, dir %v", fi.Mode(), di.Mode())
	}
	if op, _, _ := e.run(helperapi.ActionFileChown, helperapi.FileChownRequest{Root: "sites", Paths: []string{"shop/app/src"}, User: "no-such-user-x"}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeFileUserUnknown {
		t.Fatalf("chown to an unknown user: %+v", op)
	}
}

func TestArchivesRoundTrip(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/shop/app/site/index.html", "<h1>hi</h1>")
	e.put("/srv/www/shop/app/site/css/a.css", strings.Repeat("body{}", 1000))
	if err := os.Symlink("index.html", filepath.Join(e.f.Root, "/srv/www/shop/app/site/home")); err != nil {
		t.Fatal(err)
	}
	for _, format := range helperapi.ArchiveFormats {
		name := "site." + format
		if op, _, log := e.run(helperapi.ActionFileCompress, helperapi.FileCompressRequest{Root: "sites", Dir: "shop/app", Names: []string{"site"}, Format: format, Name: name}); op.State != helperapi.OpSucceeded {
			t.Fatalf("compress %s: %+v\n%s", format, op, log)
		}
		dest := "out-" + strings.ReplaceAll(format, ".", "-")
		if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileMkdir, helperapi.FileNewRequest{Root: "sites", Dir: "shop/app", Name: dest}); err != nil {
			t.Fatal(err)
		}
		if op, _, log := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/" + name, Dest: "shop/app/" + dest}); op.State != helperapi.OpSucceeded {
			t.Fatalf("extract %s: %+v\n%s", format, op, log)
		}
		base := "/srv/www/shop/app/" + dest + "/site/"
		if e.read(base+"index.html") != "<h1>hi</h1>" || len(e.read(base+"css/a.css")) != 6000 {
			t.Fatalf("%s: extracted content", format)
		}
		if target, _ := os.Readlink(filepath.Join(e.f.Root, base+"home")); target != "index.html" {
			t.Fatalf("%s: extracted link %q", format, target)
		}
	}
	if _, err := call[helperapi.OpRef](t, e.r, helperapi.ActionFileCompress, helperapi.FileCompressRequest{Root: "sites", Dir: "shop/app", Names: []string{"site"}, Format: "rar", Name: "x.rar"}); err == nil {
		t.Fatal("rar cannot be written")
	}
	if op, _, _ := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/site/index.html", Dest: "shop/app"}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeFileBadArchive {
		t.Fatalf("not an archive: %+v", op)
	}
}

func TestHostileArchives(t *testing.T) {
	e := newFilesEnv(t)
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	add := func(h *tar.Header, body string) {
		h.Size = int64(len(body))
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(tw, body)
	}
	add(&tar.Header{Name: "ok.txt", Typeflag: tar.TypeReg, Mode: 0o4755}, "fine")
	add(&tar.Header{Name: "../../escape.txt", Typeflag: tar.TypeReg}, "bad")
	add(&tar.Header{Name: "/abs.txt", Typeflag: tar.TypeReg}, "rooted")
	add(&tar.Header{Name: "up", Typeflag: tar.TypeSymlink, Linkname: "../../.."}, "")
	add(&tar.Header{Name: "etc", Typeflag: tar.TypeSymlink, Linkname: "/etc"}, "")
	add(&tar.Header{Name: "up/through.txt", Typeflag: tar.TypeReg}, "through a link")
	add(&tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "/etc/hostname"}, "")
	add(&tar.Header{Name: "dev", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}, "")
	add(&tar.Header{Name: "inner", Typeflag: tar.TypeSymlink, Linkname: "ok.txt"}, "")
	add(&tar.Header{Name: ".cairn-trash/x", Typeflag: tar.TypeReg}, "into the trash")
	_ = tw.Close()
	e.put("/srv/www/shop/app/evil.tar", tb.String())
	e.put("/srv/www/escape-marker", "")
	op, res, log := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/evil.tar", Dest: "shop/app"})
	// ok, abs (inside), up/through.txt (into a real directory: the link
	// "up" was refused), inner, and .cairn-trash/x (only the root's own
	// trash is protected).
	if op.State != helperapi.OpSucceeded || res.Done != 5 {
		t.Fatalf("extract: %+v %+v\n%s", op, res, log)
	}
	if fi, _ := os.Stat(filepath.Join(e.f.Root, "/srv/www/shop/app/ok.txt")); fi.Mode() != 0o755 {
		t.Fatalf("setuid kept: %v", fi.Mode())
	}
	if fi, err := os.Lstat(filepath.Join(e.f.Root, "/srv/www/shop/app/up")); err != nil || !fi.IsDir() {
		t.Errorf("up should be a real directory: %v", err)
	}
	for _, p := range []string{"/srv/escape.txt", "/srv/www/escape.txt", "/srv/www/shop/escape.txt", "/srv/www/shop/app/etc",
		"/srv/www/shop/app/hard", "/srv/www/shop/app/dev", "/through.txt"} {
		if e.exists(p) {
			t.Errorf("%s was extracted", p)
		}
	}
	if e.read("/srv/www/shop/app/abs.txt") != "rooted" {
		t.Error("an absolute name lands inside the destination")
	}
	if target, _ := os.Readlink(filepath.Join(e.f.Root, "/srv/www/shop/app/inner")); target != "ok.txt" {
		t.Errorf("a link inside the destination: %q", target)
	}

	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create("../../../zip-slip.txt")
	_, _ = io.WriteString(w, "slip")
	w, _ = zw.Create("fine/z.txt")
	_, _ = io.WriteString(w, "zip")
	_ = zw.Close()
	e.put("/srv/www/shop/app/evil.zip", zb.String())
	if op, res, _ := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/evil.zip", Dest: "shop/app"}); op.State != helperapi.OpSucceeded || res.Done != 1 || len(res.Failed) != 1 {
		t.Fatalf("zip: %+v %+v", op, res)
	}
	if e.exists("/zip-slip.txt") || e.exists("/srv/zip-slip.txt") || e.read("/srv/www/shop/app/fine/z.txt") != "zip" {
		t.Fatal("zip slip")
	}
	// The root's own trash cannot be extracted into.
	tb.Reset()
	tw = tar.NewWriter(&tb)
	add(&tar.Header{Name: trashName + "/planted", Typeflag: tar.TypeReg}, "x")
	_ = tw.Close()
	e.put("/srv/www/shop/app/trash.tar", tb.String())
	if op, _, _ := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/trash.tar", Dest: ""}); op.State != helperapi.OpFailed || e.exists("/srv/www/"+trashName+"/planted") {
		t.Fatalf("into the trash: %+v", op)
	}
	// A link already in the destination is not followed either.
	if err := os.Symlink("/etc", filepath.Join(e.f.Root, "/srv/www/shop/app/sneaky")); err != nil {
		t.Fatal(err)
	}
	tb.Reset()
	tw = tar.NewWriter(&tb)
	add(&tar.Header{Name: "sneaky/planted", Typeflag: tar.TypeReg}, "x")
	_ = tw.Close()
	e.put("/srv/www/shop/app/sneaky.tar", tb.String())
	if op, _, log := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/sneaky.tar", Dest: "shop/app"}); op.State != helperapi.OpFailed || !strings.Contains(log, "sneaky/planted: file.not_dir") {
		t.Fatalf("through an existing link: %+v\n%s", op, log)
	}
	if e.exists("/etc/planted") {
		t.Fatal("written through an existing link")
	}
}

func TestPlaceUpload(t *testing.T) {
	e := newFilesEnv(t)
	id := strings.Repeat("ab", 16)
	e.put("/var/lib/cairn/uploads/"+id+".part", "uploaded")
	op, _, log := e.run(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: id, Root: "sites", Dir: "shop/app", Name: "u.txt"})
	if op.State != helperapi.OpSucceeded || e.read("/srv/www/shop/app/u.txt") != "uploaded" || e.exists("/var/lib/cairn/uploads/"+id+".part") {
		t.Fatalf("place: %+v\n%s", op, log)
	}
	if fi, _ := os.Stat(filepath.Join(e.f.Root, "/srv/www/shop/app/u.txt")); fi.Mode().Perm() != 0o644 {
		t.Fatalf("placed mode %v", fi.Mode())
	}
	e.put("/var/lib/cairn/uploads/"+id+".part", "again")
	if op, _, _ := e.run(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: id, Root: "sites", Dir: "shop/app", Name: "u.txt"}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeFileExists {
		t.Fatalf("place over a file: %+v", op)
	}
	if op, _, _ := e.run(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: id, Root: "sites", Dir: "shop/app", Name: "u.txt", Overwrite: true}); op.State != helperapi.OpSucceeded || e.read("/srv/www/shop/app/u.txt") != "again" {
		t.Fatalf("place with overwrite: %+v", op)
	}
}

func TestTerminalUsers(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/etc/passwd", strings.Join([]string{
		"root:x:0:0:root:/root:/bin/bash",
		"www-data:x:33:33:www-data:/var/www:/usr/sbin/nologin",
		"alice:x:1000:1000:Alice:/home/alice:/bin/zsh",
		"locked:x:1001:1001::/home/locked:/usr/sbin/nologin",
		"cw-shop:x:997:997::/srv/www/shop:/usr/sbin/nologin",
		"nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin",
	}, "\n"))
	e.put("/bin/bash", "#!binary")
	u, _ := call[helperapi.TermUsers](t, e.r, helperapi.ActionTermUsers, helperapi.Empty{})
	if len(u.Users) != 2 || u.Users[0] != (helperapi.TermUser{Name: "cw-shop", Kind: "site", Home: "/srv/www/shop", Site: "shop"}) || u.Users[1].Name != "alice" || u.RootTerminal {
		t.Fatalf("users %+v", u)
	}
	var started execx.Terminal
	old := startTerminal
	t.Cleanup(func() { startTerminal = old })
	startTerminal = func(term execx.Terminal) (*os.File, *exec.Cmd, error) {
		started = term
		cmd := exec.Command("/bin/true") //nolint:forbidigo // a stand-in for the shell
		if err := cmd.Start(); err != nil {
			return nil, nil, err
		}
		r, _, _ := os.Pipe()
		return r, cmd, nil
	}
	open := func(user string) error {
		_, f, err := e.f.termOpen(context.Background(), helperapi.Caller{Username: "admin"}, helperapi.TermOpenRequest{User: user, Cols: 80, Rows: 24})
		if f != nil {
			_ = f.Close()
		}
		return err
	}
	if err := open("root"); errCode(err) != helperapi.CodeTermRootDisabled {
		t.Fatalf("root while switched off: %v", err)
	}
	for _, u := range []string{"locked", "www-data", "nobody"} {
		if err := open(u); errCode(err) != helperapi.CodeTermUserNotAllowed {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if err := open("cw-shop"); err != nil {
		t.Fatal(err)
	}
	// Site owners have no login shell: they get bash in their site.
	if started.Shell != "/bin/bash" || started.Uid != 997 || started.Home != "/srv/www/shop" || started.Cols != 80 {
		t.Fatalf("started %+v", started)
	}
	// The profile script's switch; without systemd execx sets the locale.
	if env := strings.Join(started.Env, " "); !strings.Contains(env, "CAIRN_TERM=1") || !strings.Contains(env, "TERM=xterm-256color") || strings.Contains(env, "LANG=") {
		t.Fatalf("environment %q", started.Env)
	}
	if _, err := call[helperapi.AccessSettings](t, e.r, helperapi.ActionAccessSet, helperapi.AccessSettings{RootTerminal: true}); err != nil {
		t.Fatal(err)
	}
	if err := open("root"); err != nil || started.Uid != 0 {
		t.Fatalf("root when switched on: %v %+v", err, started)
	}
}

func TestTerminalUnitsUnderSystemd(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/etc/passwd", "alice:x:1000:1000:Alice:/home/alice:/bin/sh\n")
	e.put("/bin/sh", "#!binary")
	oldSystemd, oldUnit, oldQuery := systemdRunning, startTerminalUnit, queryCommand
	t.Cleanup(func() { systemdRunning, startTerminalUnit, queryCommand = oldSystemd, oldUnit, oldQuery })
	systemdRunning = func() bool { return true }
	running := ""
	queryCommand = func(_ context.Context, name string, _ ...string) (string, error) {
		if name != "systemctl" {
			t.Errorf("query %s", name)
		}
		return running, nil
	}
	var unit string
	var env []string
	startTerminalUnit = func(_ context.Context, term execx.Terminal, u string) (*os.File, error) {
		unit, env = u, term.Env
		if term.Uid != 1000 || term.Shell != "/bin/sh" {
			t.Errorf("terminal %+v", term)
		}
		r, _, err := os.Pipe()
		return r, err
	}
	// A system without a locale: the unit gets C.UTF-8, as execx gives a
	// shell without systemd.
	t.Setenv("LANG", "")
	s, f, err := e.f.termOpen(context.Background(), helperapi.Caller{}, helperapi.TermOpenRequest{User: "alice", Cols: 80, Rows: 24})
	if err != nil || !strings.HasPrefix(unit, "cairn-term-") || s.Unit != unit {
		t.Fatalf("open %+v %v", s, err)
	}
	_ = f.Close()
	if got := strings.Join(env, " "); !strings.Contains(got, "CAIRN_TERM=1") || !strings.Contains(got, "LANG=C.UTF-8") {
		t.Fatalf("environment %q", env)
	}
	// The system's own UTF-8 locale is kept.
	t.Setenv("LANG", "zh_CN.UTF-8")
	if _, f, err := e.f.termOpen(context.Background(), helperapi.Caller{}, helperapi.TermOpenRequest{User: "alice", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}
	if got := strings.Join(env, " "); !strings.Contains(got, "LANG=zh_CN.UTF-8") || strings.Contains(got, "C.UTF-8") {
		t.Fatalf("environment %q", env)
	}
	running = strings.Repeat("cairn-term-x.service loaded active running Cairn terminal\n", maxTerminals)
	if _, _, err := e.f.termOpen(context.Background(), helperapi.Caller{}, helperapi.TermOpenRequest{User: "alice", Cols: 80, Rows: 24}); errCode(err) != helperapi.CodeTermTooMany {
		t.Fatalf("limit: %v", err)
	}
}

func TestTerminalLocale(t *testing.T) {
	for system, want := range map[string]string{
		"":                    "C.UTF-8",
		"C":                   "C.UTF-8",
		"POSIX":               "C.UTF-8",
		"en_US":               "C.UTF-8", // ISO-8859-1
		"en_US.UTF-8":         "en_US.UTF-8",
		"zh_CN.utf8":          "zh_CN.utf8",
		"C.utf8":              "C.utf8",
		"sr_RS.UTF-8@latin":   "sr_RS.UTF-8@latin",
		"en_US.UTF-8\nX=1":    "C.UTF-8",
		"en_US.UTF-8 LC_ALL=": "C.UTF-8",
	} {
		if got := terminalLocale(system); got != want {
			t.Errorf("terminalLocale(%q) = %q, want %q", system, got, want)
		}
	}
}
