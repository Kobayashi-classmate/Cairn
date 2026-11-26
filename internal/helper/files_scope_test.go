// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// What keeps working while paths are resolved once and sites are kept
// apart: links inside a site, the system root, other file systems.

func mustLink(t *testing.T, target, at string) {
	t.Helper()
	if err := os.Symlink(target, at); err != nil {
		t.Fatal(err)
	}
}

func (e *filesEnv) ok(action string, req any) string {
	e.t.Helper()
	op, _, log := e.run(action, req)
	if op.State != helperapi.OpSucceeded {
		e.t.Fatalf("%s %+v: %+v\n%s", action, req, op, log)
	}
	return log
}

func (e *filesEnv) fails(action string, req any, code string) {
	e.t.Helper()
	op, _, log := e.run(action, req)
	if op.State != helperapi.OpFailed || op.Error.Code != code {
		e.t.Fatalf("%s %+v: want %s, got %+v\n%s", action, req, code, op, log)
	}
}

// crossDevice makes the renames that match fail as between two file
// systems.
func crossDevice(t *testing.T, match func(sname, dname string) bool) {
	t.Helper()
	old := renameEntry
	t.Cleanup(func() { renameEntry = old })
	renameEntry = func(sfd int, sname string, dfd int, dname string, flags uint) error {
		if match(sname, dname) {
			return unix.EXDEV
		}
		return old(sfd, sname, dfd, dname, flags)
	}
}

func TestLinksInsideASiteAreFollowed(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/shop/app/releases/1/index.php", "v1")
	e.put("/srv/www/shop/storage/note.txt", "a note")
	e.put("/srv/www/shop/storage/sub/in.txt", "inside")
	app := e.abs("/srv/www/shop/app")
	mustLink(t, "releases/1", app+"/current") // a release in use
	mustLink(t, "../storage", app+"/data")    // out of app/, inside the site
	mustLink(t, "data/note.txt", app+"/note") // a link to a file, through the other link
	site := func(p string) helperapi.FileRef { return helperapi.FileRef{Root: "sites", Path: p} }

	l, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "sites", Path: "shop/app/current"})
	if err != nil || len(l.Entries) != 1 || l.Entries[0].Path != "shop/app/current/index.php" || l.Dir.Name != "current" || l.Dir.Type != helperapi.FileTypeDir {
		t.Fatalf("listing a linked directory: %+v %v", l, err)
	}
	if c, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, site("shop/app/current/index.php")); err != nil || c.Content != "v1" || c.Entry.Name != "index.php" {
		t.Fatalf("reading in a linked directory: %+v %v", c, err)
	}
	if c, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, site("shop/app/note")); err != nil || c.Content != "a note" {
		t.Fatalf("reading a link to a file: %+v %v", c, err)
	}
	if st, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileStat, site("shop/app/data/note.txt")); err != nil || st.Type != helperapi.FileTypeFile || st.Name != "note.txt" {
		t.Fatalf("stat behind a link: %+v %v", st, err)
	}
	entry, file, err := e.f.open(t.Context(), helperapi.Caller{}, site("shop/app/data/note.txt"))
	if err != nil || entry.Size != 6 {
		t.Fatalf("opening behind a link: %+v %v", entry, err)
	}
	_ = file.Close()
	if s, err := call[helperapi.FileSearchResult](t, e.r, helperapi.ActionFileSearch, helperapi.FileSearchRequest{Root: "sites", Path: "shop/app/data", Query: "inside", Content: true}); err != nil ||
		len(s.Matches) != 1 || s.Matches[0].Path != "shop/app/data/sub/in.txt" || s.Matches[0].Line != 1 {
		t.Fatalf("searching a linked directory: %+v %v", s, err)
	}
	// A single file can be searched, too.
	if s, err := call[helperapi.FileSearchResult](t, e.r, helperapi.ActionFileSearch, helperapi.FileSearchRequest{Root: "sites", Path: "shop/storage/note.txt", Query: "note", Content: true}); err != nil || len(s.Matches) != 2 {
		t.Fatalf("searching a file: %+v %v", s, err)
	}

	// Changes behind the link land in the site's own directory.
	saved, err := call[helperapi.FileSaved](t, e.r, helperapi.ActionFileWrite, helperapi.FileWriteRequest{Root: "sites", Path: "shop/app/data/sub/new.txt", Content: "new"})
	if err != nil || e.read("/srv/www/shop/storage/sub/new.txt") != "new" || saved.Entry.Path != "shop/app/data/sub/new.txt" {
		t.Fatalf("saving behind a link: %+v %v", saved, err)
	}
	if d, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileMkdir, helperapi.FileNewRequest{Root: "sites", Dir: "shop/app/data/sub", Name: "d"}); err != nil || d.Type != helperapi.FileTypeDir || !e.exists("/srv/www/shop/storage/sub/d") {
		t.Fatalf("mkdir behind a link: %+v %v", d, err)
	}
	if r, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileRename, helperapi.FileRenameRequest{Root: "sites", Path: "shop/app/data/note.txt", Name: "note2.txt"}); err != nil || r.Path != "shop/app/data/note2.txt" || !e.exists("/srv/www/shop/storage/note2.txt") {
		t.Fatalf("rename behind a link: %+v %v", r, err)
	}
	e.ok(helperapi.ActionFileChmod, helperapi.FileChmodRequest{Root: "sites", Paths: []string{"shop/app/data/note2.txt"}, Mode: 0o640})
	if fi, _ := os.Stat(e.abs("/srv/www/shop/storage/note2.txt")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("chmod behind a link: %v", fi.Mode())
	}
	e.ok(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/data/note2.txt", "shop/app/current"}, Dest: "shop/app/data/sub"})
	if e.read("/srv/www/shop/storage/sub/note2.txt") != "a note" {
		t.Fatal("copy behind a link")
	}
	if target, _ := os.Readlink(e.abs("/srv/www/shop/storage/sub/current")); target != "releases/1" {
		t.Fatalf("a link is copied as the link: %q", target)
	}
	e.put("/srv/www/shop/app/releases/1/assets/a.css", "a{}")
	e.ok(helperapi.ActionFileMove, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/data/sub/note2.txt"}, Dest: "shop/app/current/assets"})
	if e.read("/srv/www/shop/app/releases/1/assets/note2.txt") != "a note" || e.exists("/srv/www/shop/storage/sub/note2.txt") {
		t.Fatal("move between linked directories")
	}
	e.ok(helperapi.ActionFileCompress, helperapi.FileCompressRequest{Root: "sites", Dir: "shop/app/data/sub", Names: []string{"in.txt", "new.txt"}, Format: "tar.gz", Name: "both.tar.gz"})
	if got := tarGzContents(t, e.abs("/srv/www/shop/storage/sub/both.tar.gz")); got["in.txt"] != "inside" || got["new.txt"] != "new" {
		t.Fatalf("compress behind a link: %v", got)
	}
	e.ok(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/data/sub/both.tar.gz", Dest: "shop/app/data/sub/d"})
	if e.read("/srv/www/shop/storage/sub/d/in.txt") != "inside" {
		t.Fatal("extract behind a link")
	}
	// An archive is unpacked into a linked directory as it always was.
	e.ok(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/data/sub/both.tar.gz", Dest: "shop/app/current"})
	if e.read("/srv/www/shop/app/releases/1/in.txt") != "inside" {
		t.Fatal("extract into a linked directory")
	}
	// The archive itself is a file, not a link to one.
	mustLink(t, "both.tar.gz", e.abs("/srv/www/shop/storage/sub/link.tar.gz"))
	e.fails(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/data/sub/link.tar.gz", Dest: "shop/app/data/sub/d", Overwrite: true}, helperapi.CodeFileIsLink)
	e.ok(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: putUpload(e, "uploaded"), Root: "sites", Dir: "shop/app/data/sub", Name: "u.txt"})
	if e.read("/srv/www/shop/storage/sub/u.txt") != "uploaded" {
		t.Fatal("upload behind a link")
	}
	e.ok(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "sites", Paths: []string{"shop/app/data/sub/u.txt"}})
	e.ok(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "sites", Paths: []string{"shop/app/data/sub/d"}, Permanent: true})
	if e.exists("/srv/www/shop/storage/sub/u.txt") || e.exists("/srv/www/shop/storage/sub/d") {
		t.Fatal("delete behind a link")
	}
	// Whatever takes entries is named by the directory itself, as before:
	// a link in its place is refused, not followed.
	if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileTouch, helperapi.FileNewRequest{Root: "sites", Dir: "shop/app/data", Name: "x"}); errCode(err) != helperapi.CodeFileNotDir {
		t.Fatalf("a new file in a link's place: %v", err)
	}
	e.fails(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/note"}, Dest: "shop/app/data"}, helperapi.CodeFileNotDir)
}

// An entry replaced by itself (reached by another path) or by nothing (an
// entry that is skipped) is not lost.
func TestReplacingLosesNothingWhenNothingTakesThePlace(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/shop/storage/f.txt", "the file")
	e.put("/srv/www/shop/storage/d/in.txt", "the directory")
	mustLink(t, "../storage", e.abs("/srv/www/shop/app/data"))
	for _, action := range []string{helperapi.ActionFileCopy, helperapi.ActionFileMove} {
		e.fails(action, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/data/f.txt", "shop/app/data/d"}, Dest: "shop/storage", Overwrite: true}, helperapi.CodeFileNotFound)
		if e.read("/srv/www/shop/storage/f.txt") != "the file" || e.read("/srv/www/shop/storage/d/in.txt") != "the directory" {
			t.Fatalf("%s of an entry over itself lost it", action)
		}
		if left := mustGlob(t, e.abs("/srv/www/shop/storage/.cairn-old-*")); len(left) != 0 || len(trashNames(e)) != 0 {
			t.Fatalf("%s of an entry over itself left %v, trash %v", action, left, trashNames(e))
		}
	}
	// A pipe is not copied: what it was to replace stays.
	e.put("/srv/www/shop/app/in/p", "kept")
	if err := os.Mkdir(e.abs("/srv/www/shop/app/out"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(e.abs("/srv/www/shop/app/out/p"), 0o600); err != nil {
		t.Skip("no fifo:", err)
	}
	log := e.ok(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/out/p"}, Dest: "shop/app/in", Overwrite: true})
	if e.read("/srv/www/shop/app/in/p") != "kept" || !strings.Contains(log, "skipped") {
		t.Fatalf("a skipped entry replaced a file:\n%s", log)
	}
	// Nor is it "moved" to another file system by being removed.
	crossDevice(t, func(sname, _ string) bool { return sname == "p" })
	e.fails(helperapi.ActionFileMove, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/out/p"}, Dest: "shop/app/in", Overwrite: true}, helperapi.CodeOpFailed)
	if e.read("/srv/www/shop/app/in/p") != "kept" || !e.exists("/srv/www/shop/app/out/p") {
		t.Fatal("a skipped entry was removed, or replaced a file")
	}
}

func TestEntriesOfTheSitesRootAreNotFollowed(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/shop/app/index.php", "shop")
	e.put("/srv/www/readme.txt", "top")
	mustLink(t, "shop", e.abs("/srv/www/alias"))
	mustLink(t, "shop/app/index.php", e.abs("/srv/www/page"))
	site := func(p string) helperapi.FileRef { return helperapi.FileRef{Root: "sites", Path: p} }
	// The entries themselves are there to see, links included.
	l, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "sites"})
	if err != nil || len(l.Entries) != 4 || l.Dir.Name != "" {
		t.Fatalf("listing the root: %+v %v", l, err)
	}
	if st, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileStat, site("alias")); err != nil || st.Type != helperapi.FileTypeLink || st.Target != "shop" {
		t.Fatalf("a link in the root: %+v %v", st, err)
	}
	if c, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, site("readme.txt")); err != nil || c.Content != "top" {
		t.Fatalf("a file in the root: %+v %v", c, err)
	}
	if st, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileStat, site("")); err != nil || st.Type != helperapi.FileTypeDir || st.Name != "" {
		t.Fatalf("the root itself: %+v %v", st, err)
	}
	// Nothing goes through them.
	if _, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "sites", Path: "alias"}); errCode(err) != helperapi.CodeFileNotDir {
		t.Fatalf("listing a link in the root: %v", err)
	}
	if _, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, site("alias/app/index.php")); errCode(err) != helperapi.CodeFileNotFound {
		t.Fatalf("reading through a link in the root: %v", err)
	}
	if _, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, site("page")); errCode(err) != helperapi.CodeFileIsLink {
		t.Fatalf("reading a link in the root: %v", err)
	}
	if _, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "sites", Path: "readme.txt"}); errCode(err) != helperapi.CodeFileNotDir {
		t.Fatalf("listing a file: %v", err)
	}
	if _, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "sites", Path: "shop/app/index.php"}); errCode(err) != helperapi.CodeFileNotDir {
		t.Fatalf("listing a file in a site: %v", err)
	}
	if _, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "sites", Path: "shop/none/deeper"}); errCode(err) != helperapi.CodeFileNotFound {
		t.Fatalf("listing what is not there: %v", err)
	}
}

func TestSystemRootWorksAndKeepsSitesApart(t *testing.T) {
	needRoot(t)
	e := newFilesEnv(t)
	otherSite(e)
	if _, err := call[helperapi.AccessSettings](t, e.r, helperapi.ActionAccessSet, helperapi.AccessSettings{FullDisk: true}); err != nil {
		t.Fatal(err)
	}
	e.put("/etc/app/app.conf", "a = 1")
	e.put("/opt/tool/readme", "tool")
	e.put("/srv/www/shop/storage/note.txt", "a note")
	mustLink(t, "../etc/app", e.abs("/opt/conf"))                  // the administrator's own link
	mustLink(t, "../storage", e.abs("/srv/www/shop/app/data"))     // inside the site
	mustLink(t, "../../blog/app", e.abs("/srv/www/shop/app/x"))    // to another site
	mustLink(t, "../../../../etc", e.abs("/srv/www/shop/app/etc")) // to the system
	sys := func(p string) helperapi.FileRef { return helperapi.FileRef{Root: "system", Path: p} }

	// System directories, through the administrator's links too.
	if l, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "system", Path: "opt/conf"}); err != nil || len(l.Entries) != 1 {
		t.Fatalf("listing through a link: %+v %v", l, err)
	}
	c, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, sys("opt/conf/app.conf"))
	if err != nil || c.Content != "a = 1" {
		t.Fatalf("reading through a link: %+v %v", c, err)
	}
	if _, err := call[helperapi.FileSaved](t, e.r, helperapi.ActionFileWrite, helperapi.FileWriteRequest{Root: "system", Path: "etc/app/app.conf", Content: "a = 2", ETag: c.ETag}); err != nil || e.read("/etc/app/app.conf") != "a = 2" {
		t.Fatalf("saving a system file: %v", err)
	}
	if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileMkdir, helperapi.FileNewRequest{Root: "system", Dir: "etc/app", Name: "conf.d"}); err != nil {
		t.Fatalf("mkdir in a system directory: %v", err)
	}
	if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileRename, helperapi.FileRenameRequest{Root: "system", Path: "opt/conf/app.conf", Name: "main.conf"}); err != nil || !e.exists("/etc/app/main.conf") {
		t.Fatalf("rename through a link: %v", err)
	}
	e.ok(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "system", Paths: []string{"etc/app/main.conf", "opt/tool"}, Dest: "etc/app/conf.d"})
	e.ok(helperapi.ActionFileMove, helperapi.FileTransferRequest{Root: "system", Paths: []string{"etc/app/conf.d/tool"}, Dest: "opt/tool"})
	if e.read("/etc/app/conf.d/main.conf") != "a = 2" || e.read("/opt/tool/tool/readme") != "tool" {
		t.Fatal("copy and move in system directories")
	}
	e.ok(helperapi.ActionFileChmod, helperapi.FileChmodRequest{Root: "system", Paths: []string{"etc/app"}, Mode: 0o640, DirMode: 0o750, Recursive: true})
	e.ok(helperapi.ActionFileCompress, helperapi.FileCompressRequest{Root: "system", Dir: "etc/app", Names: []string{"conf.d"}, Format: "zip", Name: "conf.zip"})
	e.ok(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "system", Path: "etc/app/conf.zip", Dest: "opt/tool"})
	if e.read("/opt/tool/conf.d/main.conf") != "a = 2" {
		t.Fatal("compress and extract in system directories")
	}
	e.ok(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: putUpload(e, "uploaded"), Root: "system", Dir: "etc/app", Name: "u.conf"})
	// The system's trash, and back.
	e.ok(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "system", Paths: []string{"etc/app/u.conf", "etc/app/conf.d"}})
	trash, _ := call[helperapi.TrashList](t, e.r, helperapi.ActionTrashList, helperapi.FileRootRef{Root: "system"})
	if len(trash.Entries) != 2 || e.exists("/etc/app/conf.d") || len(mustGlob(t, e.abs("/"+systemTrash+"/*"))) != 4 {
		t.Fatalf("the system's trash: %+v", trash.Entries)
	}
	e.ok(helperapi.ActionTrashRestore, helperapi.TrashRequest{Root: "system", IDs: []string{trash.Entries[0].ID, trash.Entries[1].ID}})
	if e.read("/etc/app/u.conf") != "uploaded" || e.read("/etc/app/conf.d/main.conf") != "a = 2" {
		t.Fatal("restore from the system's trash")
	}
	e.ok(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "system", Paths: []string{"etc/app/u.conf"}, Permanent: true})
	// The top of the system stays as it is.
	e.fails(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "system", Paths: []string{"etc"}, Permanent: true}, helperapi.CodeFileProtected)
	e.fails(helperapi.ActionFileMove, helperapi.FileTransferRequest{Root: "system", Paths: []string{"opt/tool/readme"}, Dest: ""}, helperapi.CodeFileProtected)
	e.fails(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "system", Paths: []string{"opt/tool"}, Dest: "", Overwrite: true}, helperapi.CodeFileProtected)
	if !e.exists("/opt/tool/readme") || e.exists("/readme") || e.exists("/tool") {
		t.Fatal("the top of the system changed")
	}

	// A site, seen from the system root, is as closed as from the sites
	// root: its own links work, the others lead nowhere.
	if c, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, sys("srv/www/shop/app/data/note.txt")); err != nil || c.Content != "a note" {
		t.Fatalf("a link inside the site: %+v %v", c, err)
	}
	for _, p := range []string{"srv/www/shop/app/x/secret", "srv/www/shop/app/etc/app/main.conf"} {
		if c, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, sys(p)); errCode(err) != helperapi.CodeFileNotFound {
			t.Fatalf("reading %s: %+v %v", p, c, err)
		}
	}
	if l, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "system", Path: "srv/www/shop/app/x"}); err == nil {
		t.Fatalf("listing another site through a link: %+v", l.Entries)
	}
	e.fails(helperapi.ActionFileChmod, helperapi.FileChmodRequest{Root: "system", Paths: []string{"srv/www/shop/app/x/secret"}, Mode: 0o644}, helperapi.CodeFileNotFound)
	e.fails(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "system", Paths: []string{"srv/www/shop/app/etc/app/main.conf"}, Permanent: true}, helperapi.CodeFileNotFound)
	e.fails(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "system", Paths: []string{"opt/tool/readme"}, Dest: "srv/www/shop/app/x"}, helperapi.CodeFileNotDir)
	otherSiteIntact(e, "the system root")
	if e.read("/etc/app/main.conf") != "a = 2" {
		t.Fatal("a system file was changed through a site's link")
	}
	// Into a site and out of it, by its own paths.
	e.ok(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "system", Paths: []string{"opt/tool/readme"}, Dest: "srv/www/shop/app"})
	e.ok(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "system", Paths: []string{"srv/www/shop/app/data/note.txt"}, Dest: "opt/tool"})
	if e.read("/srv/www/shop/app/readme") != "tool" || e.read("/opt/tool/note.txt") != "a note" {
		t.Fatal("copies between a site and the system")
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestUploadAcrossFileSystems(t *testing.T) {
	needRoot(t)
	e := newFilesEnv(t)
	chownAll(t, shopUID, e.abs("/srv/www/shop/app"))
	crossDevice(t, func(sname, _ string) bool { return strings.HasSuffix(sname, ".part") })
	id := putUpload(e, "uploaded")
	part := e.abs("/var/lib/cairn/uploads/" + id + ".part")
	log := e.ok(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: id, Root: "sites", Dir: "shop/app", Name: "u.txt"})
	fi, err := os.Lstat(e.abs("/srv/www/shop/app/u.txt"))
	if err != nil || !strings.Contains(log, "copying") || e.read("/srv/www/shop/app/u.txt") != "uploaded" || fi.Mode() != 0o644 || uidOf(fi) != shopUID {
		t.Fatalf("copied upload: %v %v\n%s", fi, err, log)
	}
	if left := mustGlob(t, e.abs("/srv/www/shop/app/.cairn-upload-*")); len(left) != 0 || e.exists("/var/lib/cairn/uploads/"+id+".part") {
		t.Fatalf("left behind: %v", left)
	}
	// An upload that is not placed stays the panel's, as it was.
	putUpload(e, "again")
	e.fails(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: id, Root: "sites", Dir: "shop/app", Name: "u.txt"}, helperapi.CodeFileExists)
	if err := os.Mkdir(e.abs("/srv/www/shop/app/dir"), 0o750); err != nil {
		t.Fatal(err)
	}
	e.fails(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: id, Root: "sites", Dir: "shop/app", Name: "dir", Overwrite: true}, helperapi.CodeFileIsDir)
	if fi, err := os.Lstat(part); err != nil || fi.Mode() != 0o600 || uidOf(fi) != 0 || e.read("/srv/www/shop/app/u.txt") != "uploaded" {
		t.Fatalf("the upload after a refusal: %v %v", fi, err)
	}
	e.ok(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: id, Root: "sites", Dir: "shop/app", Name: "u.txt", Overwrite: true})
	if e.read("/srv/www/shop/app/u.txt") != "again" || len(mustGlob(t, e.abs("/srv/www/shop/app/.cairn-upload-*"))) != 0 {
		t.Fatal("copied over a file")
	}
	// A hard link the panel put where an upload should be is not taken.
	e.put("/etc/victim", "keep")
	if err := os.Link(e.abs("/etc/victim"), part); err != nil {
		t.Fatal(err)
	}
	e.fails(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: id, Root: "sites", Dir: "shop/app", Name: "v.txt"}, helperapi.CodeFileNotFound)
	if fi, _ := os.Lstat(e.abs("/etc/victim")); fi.Mode() != 0o600 || uidOf(fi) != 0 || e.exists("/srv/www/shop/app/v.txt") {
		t.Fatalf("a hard link in the uploads was placed: %v", fi)
	}
}

func TestMoveAndTrashAcrossFileSystems(t *testing.T) {
	needRoot(t)
	e := newFilesEnv(t)
	e.put("/srv/www/shop/app/tree/sub/f.txt", "f")
	mustLink(t, "sub/f.txt", e.abs("/srv/www/shop/app/tree/l"))
	other := e.abs("/srv/www/shop/app/other")
	if err := os.Mkdir(other, 0o750); err != nil {
		t.Fatal(err)
	}
	chownAll(t, shopUID, other)
	// The destination is on another file system: copied, then removed.
	crossDevice(t, func(sname, _ string) bool { return sname == "tree" })
	e.ok(helperapi.ActionFileMove, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/tree"}, Dest: "shop/app/other"})
	if e.read("/srv/www/shop/app/other/tree/sub/f.txt") != "f" || e.exists("/srv/www/shop/app/tree") || ownerAt(t, other+"/tree/sub/f.txt") != shopUID {
		t.Fatal("move to another file system")
	}
	if target, _ := os.Readlink(other + "/tree/l"); target != "sub/f.txt" {
		t.Fatalf("a moved link: %q", target)
	}
	// The trash is on another file system: nothing is moved there, the
	// entry stays, and no half-made trash entry is left.
	crossDevice(t, func(sname, _ string) bool { return sname == "tree" || sname == "gone.txt" })
	e.put("/srv/www/shop/app/gone.txt", "still here")
	e.fails(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "sites", Paths: []string{"shop/app/gone.txt"}}, helperapi.CodeFileCrossDevice)
	if e.read("/srv/www/shop/app/gone.txt") != "still here" || len(trashNames(e)) != 0 {
		t.Fatalf("a delete across file systems: trash %v", trashNames(e))
	}
	// So a directory there is not replaced either: it would be lost.
	e.put("/srv/www/shop/app/new/tree/n.txt", "n")
	e.fails(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/new/tree"}, Dest: "shop/app/other", Overwrite: true}, helperapi.CodeFileCrossDevice)
	if e.read("/srv/www/shop/app/other/tree/sub/f.txt") != "f" || e.exists("/srv/www/shop/app/other/tree/n.txt") || len(trashNames(e)) != 0 {
		t.Fatalf("a directory replaced across file systems: trash %v", trashNames(e))
	}
}

// A move to another file system removes from where it was only what it
// copied: an entry the copy skipped keeps the source whole, and what
// appeared or changed while it copied stays where it was. Either way the
// move is not reported as done.
func TestMoveAcrossFileSystemsKeepsWhatItDidNotCopy(t *testing.T) {
	e := newFilesEnv(t)
	if err := os.Mkdir(e.abs("/srv/www/shop/app/other"), 0o750); err != nil {
		t.Fatal(err)
	}
	crossDevice(t, func(sname, _ string) bool { return strings.HasPrefix(sname, "src") })
	move := func(name string) helperapi.FileTransferRequest {
		return helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/" + name}, Dest: "shop/app/other"}
	}
	hooked := ""
	onEnter(t, func(n string) {
		if n != hooked {
			return
		}
		hooked = ""
		switch n {
		case "d":
			// The application replaces d by a fresh directory, its old
			// contents now at d.old, and writes late.txt: the copy skips d.
			src := e.abs("/srv/www/shop/app/src")
			if err := os.Rename(src+"/d", src+"/d.old"); err != nil {
				t.Error(err)
			}
			if err := os.Mkdir(src+"/d", 0o750); err != nil {
				t.Error(err)
			}
			if err := os.WriteFile(src+"/late.txt", []byte("late"), 0o600); err != nil {
				t.Error(err)
			}
		case "b":
			// a.txt was copied already and is written to again; late.txt
			// is new. Nothing is skipped.
			src := e.abs("/srv/www/shop/app/src2")
			f, err := os.OpenFile(src+"/a.txt", os.O_WRONLY|os.O_APPEND, 0)
			if err == nil {
				_, err = f.WriteString(" and more")
				err = errors.Join(err, f.Close())
			}
			if err != nil {
				t.Error(err)
			}
			if err := os.WriteFile(src+"/late.txt", []byte("late"), 0o600); err != nil {
				t.Error(err)
			}
		}
	})

	e.put("/srv/www/shop/app/src/d/data.txt", "precious")
	e.put("/srv/www/shop/app/src/keep.txt", "keep")
	hooked = "d"
	e.fails(helperapi.ActionFileMove, move("src"), helperapi.CodeFileMoveIncomplete)
	if hooked != "" {
		t.Fatal("the directory was not replaced during the copy")
	}
	for p, want := range map[string]string{"src/d.old/data.txt": "precious", "src/late.txt": "late", "src/keep.txt": "keep", "other/src/keep.txt": "keep"} {
		if got := e.read("/srv/www/shop/app/" + p); got != want {
			t.Errorf("after a move that skipped an entry: %s holds %q, want %q", p, got, want)
		}
	}

	e.put("/srv/www/shop/app/src2/a.txt", "a")
	e.put("/srv/www/shop/app/src2/b/c.txt", "c")
	e.put("/srv/www/shop/app/src2/z.txt", "z")
	hooked = "b"
	e.fails(helperapi.ActionFileMove, move("src2"), helperapi.CodeFileMoveIncomplete)
	if hooked != "" {
		t.Fatal("nothing changed during the copy")
	}
	for p, want := range map[string]string{"src2/a.txt": "a and more", "src2/late.txt": "late", "other/src2/a.txt": "a", "other/src2/b/c.txt": "c", "other/src2/z.txt": "z"} {
		if got := e.read("/srv/www/shop/app/" + p); got != want {
			t.Errorf("after a move during which entries changed: %s holds %q, want %q", p, got, want)
		}
	}
	// What was copied unchanged is gone from where it was.
	for _, p := range []string{"src2/b", "src2/z.txt"} {
		if e.exists("/srv/www/shop/app/" + p) {
			t.Errorf("%s was copied but not removed", p)
		}
	}

	// Nothing changes meanwhile: moved, nothing left behind.
	e.put("/srv/www/shop/app/src3/x/y.txt", "y")
	mustLink(t, "x/y.txt", e.abs("/srv/www/shop/app/src3/l"))
	e.ok(helperapi.ActionFileMove, move("src3"))
	if e.exists("/srv/www/shop/app/src3") || e.read("/srv/www/shop/app/other/src3/x/y.txt") != "y" {
		t.Fatal("an unchanged tree was not moved")
	}
}

func TestFailedCopyLeavesWhatTookItsPlace(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/shop/app/src/app/f.txt", "own")
	dst := e.abs("/srv/www/shop/app/dst")
	if err := os.Mkdir(dst, 0o750); err != nil {
		t.Fatal(err)
	}
	seen := 0
	onEnter(t, func(name string) {
		if name != "app" {
			return
		}
		if seen++; seen != 2 { // 1: the source, 2: the copy just made
			return
		}
		// Someone moves the copy's new directory away and puts a tree of
		// theirs in its place: the copy stops there.
		if err := os.Rename(filepath.Join(dst, "app"), filepath.Join(dst, "moved")); err != nil {
			t.Error(err)
		}
		e.put("/srv/www/shop/app/dst/app/keep.txt", "theirs")
	})
	e.fails(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/src/app"}, Dest: "shop/app/dst"}, helperapi.CodeFileIsLink)
	if e.read("/srv/www/shop/app/dst/app/keep.txt") != "theirs" || e.exists("/srv/www/shop/app/dst/app/f.txt") {
		t.Fatal("the failed copy touched a tree that was not its own")
	}
}

func TestRenameAtNeverReplacesWhenAsked(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	read := func(n string) string {
		data, _ := os.ReadFile(filepath.Join(dir, n))
		return string(data)
	}
	check := func(what string) {
		t.Helper()
		if err := renameAt(root, "a", root, "b", true); !errors.Is(err, os.ErrExist) || read("a") != "a" || read("b") != "b" {
			t.Fatalf("%s: over an entry: %v", what, err)
		}
		if err := renameAt(root, "a", root, "c", true); err != nil || read("c") != "a" {
			t.Fatalf("%s: to a free name: %v", what, err)
		}
		if err := renameAt(root, "c", root, "a", false); err != nil || read("a") != "a" {
			t.Fatalf("%s: back: %v", what, err)
		}
	}
	check("renameat2")
	// A file system that cannot refuse to replace.
	old := renameEntry
	t.Cleanup(func() { renameEntry = old })
	renameEntry = func(sfd int, sname string, dfd int, dname string, flags uint) error {
		if flags != 0 {
			return unix.EINVAL
		}
		return old(sfd, sname, dfd, dname, flags)
	}
	check("without flags")
	if err := renameAt(root, "a", root, "b", false); err != nil || read("b") != "a" {
		t.Fatalf("replacing: %v", err)
	}
}
