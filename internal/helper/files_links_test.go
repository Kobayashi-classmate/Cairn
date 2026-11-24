// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// A site's owner can swap any directory of theirs for a link at any time,
// and leave links to other sites lying around. The tests below have the
// site "shop" (its app/ belongs to shopUID) do that while the panel works
// in it, and check that the other site, "blog", is never read or changed.

const (
	shopUID = 4321 // the owner of the directories that are swapped
	blogUID = 5555 // the owner of the other site's files
)

func (e *filesEnv) abs(p string) string { return filepath.Join(e.f.Root, p) }

// otherSite lays out blog/app: secret, index.php and config.php, all
// blogUID's and 0600.
func otherSite(e *filesEnv) {
	e.t.Helper()
	for name, content := range map[string]string{"secret": "blog secret", "index.php": "blog index", "config.php": "blog config"} {
		p := "/srv/www/blog/app/" + name
		e.put(p, content)
		if err := os.Chown(e.abs(p), blogUID, blogUID); err != nil {
			e.t.Fatal(err)
		}
	}
}

// otherSiteIntact fails the test if anything of blog/app changed; extra
// names the entries a test put there besides.
func otherSiteIntact(e *filesEnv, what string, extra ...string) {
	e.t.Helper()
	files := map[string]string{"secret": "blog secret", "index.php": "blog index", "config.php": "blog config"}
	for name, want := range files {
		p := "/srv/www/blog/app/" + name
		fi, err := os.Lstat(e.abs(p))
		if err != nil {
			e.t.Errorf("%s: blog/app/%s is gone: %v", what, name, err)
			continue
		}
		if got := e.read(p); got != want {
			e.t.Errorf("%s: blog/app/%s now holds %q", what, name, got)
		}
		if uid, _ := fileOwner(fi); uid != blogUID {
			e.t.Errorf("%s: blog/app/%s now belongs to %d", what, name, uid)
		}
		if fi.Mode().Perm() != 0o600 {
			e.t.Errorf("%s: blog/app/%s now has mode %v", what, name, fi.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(e.abs("/srv/www/blog/app"))
	if err != nil {
		e.t.Errorf("%s: blog/app: %v", what, err)
	}
	for _, en := range entries {
		if _, ok := files[en.Name()]; !ok && !slices.Contains(extra, en.Name()) {
			e.t.Errorf("%s: blog/app/%s was put into the other site", what, en.Name())
		}
	}
}

// swapForLink moves the directory at (an absolute path) aside to at.real
// and puts a link to target in its place.
func swapForLink(t *testing.T, at, target string) {
	t.Helper()
	if err := os.Rename(at, at+".real"); err != nil {
		t.Error(err)
		return
	}
	if err := os.Symlink(target, at); err != nil {
		t.Error(err)
	}
}

// onStep runs fn at the steps of the operations under test.
func onStep(t *testing.T, fn func(stage, p string)) {
	t.Helper()
	old := stepHook
	t.Cleanup(func() { stepHook = old })
	stepHook = fn
}

// onEnter runs fn before a walk enters a directory.
func onEnter(t *testing.T, fn func(name string)) {
	t.Helper()
	old := enterHook
	t.Cleanup(func() { enterHook = old })
	enterHook = func(_ *os.Root, name string) { fn(name) }
}

func chownAll(t *testing.T, uid int, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.Lchown(p, uid, uid); err != nil {
			t.Fatal(err)
		}
	}
}

func ownerAt(t *testing.T, p string) int {
	t.Helper()
	uid, _ := fileOwnerOf(t, p)
	return uid
}

// tarGzContents reads a .tar.gz file into name -> content.
func tarGzContents(t *testing.T, p string) map[string]string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	out := map[string]string{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err != nil {
			return out
		}
		data, _ := io.ReadAll(tr)
		out[h.Name] = string(data)
	}
}

func needRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("changes owners")
	}
}

// Compressing: every name comes from the directory opened at the start,
// whatever its path leads to by the time the second name is read.
func TestCompressReadsOnlyTheDirectoryItOpened(t *testing.T) {
	needRoot(t)
	e := newFilesEnv(t)
	otherSite(e)
	e.put("/srv/www/shop/app/up/aaa/f.txt", "own f")
	e.put("/srv/www/shop/app/up/secret", "own secret")
	up := e.abs("/srv/www/shop/app/up")
	chownAll(t, shopUID, up, up+"/aaa", up+"/aaa/f.txt", up+"/secret")
	swapped := false
	onEnter(t, func(name string) {
		if name == "aaa" && !swapped { // the first name is being archived
			swapped = true
			swapForLink(t, up, "../../blog/app")
		}
	})
	op, _, log := e.run(helperapi.ActionFileCompress, helperapi.FileCompressRequest{Root: "sites", Dir: "shop/app/up", Names: []string{"aaa", "secret"},
		Format: "tar.gz", Name: "out.tar.gz"})
	if !swapped || op.State != helperapi.OpSucceeded {
		t.Errorf("compress: swapped=%v %+v\n%s", swapped, op, log)
	}
	var archives []string
	for _, glob := range []string{up + ".real/.cairn-archive-*", up + ".real/*.tar.gz", e.abs("/srv/www/blog/app/.cairn-archive-*"), e.abs("/srv/www/blog/app/*.tar.gz")} {
		m, _ := filepath.Glob(glob)
		archives = append(archives, m...)
	}
	if len(archives) != 1 || archives[0] != up+".real/out.tar.gz" {
		t.Errorf("the archive belongs in the directory that was opened, and nothing else is left: %v", archives)
	}
	for _, a := range archives {
		got := tarGzContents(t, a)
		if got["secret"] != "own secret" || got["aaa/f.txt"] != "own f" {
			t.Errorf("the archive %s (owner %d) holds %v", a, ownerAt(t, a), got)
		}
		if uid := ownerAt(t, a); uid != shopUID {
			t.Errorf("the archive %s belongs to %d", a, uid)
		}
	}
	otherSiteIntact(e, "compress")
}

// transferEnv lays out shop/app/src (a directory and a file) and the
// destination shop/app/dst, shopUID's.
func transferEnv(t *testing.T) (*filesEnv, string) {
	t.Helper()
	needRoot(t)
	e := newFilesEnv(t)
	otherSite(e)
	e.put("/srv/www/shop/app/src/a/f.txt", "a")
	e.put("/srv/www/shop/app/src/shell.php", "<?php evil();")
	dst := e.abs("/srv/www/shop/app/dst")
	if err := os.Mkdir(dst, 0o750); err != nil {
		t.Fatal(err)
	}
	chownAll(t, shopUID, dst)
	return e, dst
}

// Copying and moving: every entry goes into the destination opened at the
// start, whatever its path leads to by the time the second entry is placed.
func TestTransfersStayInTheDestinationTheyOpened(t *testing.T) {
	for _, action := range []string{helperapi.ActionFileCopy, helperapi.ActionFileMove} {
		e, dst := transferEnv(t)
		swapped := false
		// While the first entry is copied (or, moved, given to the
		// destination's owner), the destination becomes a link.
		onEnter(t, func(name string) {
			if name == "a" && !swapped {
				swapped = true
				swapForLink(t, dst, "../../blog/app")
			}
		})
		op, res, log := e.run(action, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/src/a", "shop/app/src/shell.php"}, Dest: "shop/app/dst"})
		if !swapped || op.State != helperapi.OpSucceeded || res.Done != 2 {
			t.Errorf("%s: swapped=%v %+v %+v\n%s", action, swapped, op, res, log)
		}
		if e.read("/srv/www/shop/app/dst.real/shell.php") != "<?php evil();" || e.read("/srv/www/shop/app/dst.real/a/f.txt") != "a" {
			t.Errorf("%s: the entries belong in the directory that was opened", action)
		} else if uid := ownerAt(t, dst+".real/shell.php"); uid != shopUID {
			t.Errorf("%s: the placed file belongs to %d", action, uid)
		}
		otherSiteIntact(e, action)
	}
}

// A copy that fails removes what it made, in the destination it opened,
// and nothing else.
func TestFailedCopyRemovesOnlyWhatItMade(t *testing.T) {
	needRoot(t)
	e := newFilesEnv(t)
	otherSite(e)
	e.put("/srv/www/shop/app/src/app/f.txt", "own")
	dst := e.abs("/srv/www/shop/app/dst")
	if err := os.Mkdir(dst, 0o750); err != nil {
		t.Fatal(err)
	}
	chownAll(t, shopUID, dst)
	seen := 0
	onEnter(t, func(name string) {
		if name != "app" {
			return
		}
		if seen++; seen != 2 { // 1: the source, 2: the copy just made
			return
		}
		// A name taken in the new directory makes the copy fail; the
		// destination is a link to the other site by then.
		if err := os.WriteFile(filepath.Join(dst, "app", "f.txt"), []byte("x"), 0o600); err != nil {
			t.Error(err)
		}
		swapForLink(t, dst, "../../blog")
	})
	op, _, log := e.run(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/src/app"}, Dest: "shop/app/dst"})
	if seen < 2 || op.State != helperapi.OpFailed {
		t.Errorf("copy: entered %d times, %+v\n%s", seen, op, log)
	}
	if e.exists("/srv/www/shop/app/dst.real/app") {
		t.Error("the failed copy's own directory is left behind")
	}
	otherSiteIntact(e, "failed copy")
}

func putUpload(e *filesEnv, content string) string {
	id := strings.Repeat("ab", 16)
	e.put("/var/lib/cairn/uploads/"+id+".part", content)
	return id
}

// An upload's owner and mode are set on the uploaded file itself, never on
// what a name in the destination leads to.
func TestPlacedUploadIsChangedItself(t *testing.T) {
	needRoot(t)
	e := newFilesEnv(t)
	otherSite(e)
	chownAll(t, shopUID, e.abs("/srv/www/shop/app"))
	id := putUpload(e, "uploaded")
	hit := false
	onStep(t, func(stage, p string) {
		if stage != "placed" {
			return
		}
		hit = true
		// The directory's owner replaces the entry with a link.
		full := e.abs("/srv/www/" + p)
		if err := os.Remove(full); err != nil {
			t.Error(err)
		}
		if err := os.Symlink("../../blog/app/secret", full); err != nil {
			t.Error(err)
		}
	})
	op, _, log := e.run(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: id, Root: "sites", Dir: "shop/app", Name: "u.txt"})
	if !hit {
		t.Errorf("place: %+v\n%s", op, log)
	}
	otherSiteIntact(e, "place")
}

// An upload lands in the directory whose owner and entries were checked.
func TestUploadLandsInTheDirectoryItOpened(t *testing.T) {
	needRoot(t)
	e := newFilesEnv(t)
	otherSite(e)
	up := e.abs("/srv/www/shop/app/up")
	if err := os.Mkdir(up, 0o750); err != nil {
		t.Fatal(err)
	}
	chownAll(t, shopUID, up)
	id := putUpload(e, "uploaded index")
	onStep(t, func(stage, _ string) {
		if stage == "place" {
			swapForLink(t, up, "../../blog/app")
		}
	})
	op, _, log := e.run(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: id, Root: "sites", Dir: "shop/app/up", Name: "index.php"})
	if op.State != helperapi.OpSucceeded || e.read("/srv/www/shop/app/up.real/index.php") != "uploaded index" {
		t.Errorf("place: %+v\n%s", op, log)
	} else if uid := ownerAt(t, up+".real/index.php"); uid != shopUID {
		t.Errorf("the upload belongs to %d", uid)
	}
	otherSiteIntact(e, "place")
}

// entryEnv lays out shop/app/a (index.php, config.php; shopUID's), which
// becomes a link to blog/app at the given step of an operation.
func entryEnv(t *testing.T, stage string) (*filesEnv, *bool) {
	t.Helper()
	needRoot(t)
	e := newFilesEnv(t)
	otherSite(e)
	a := e.abs("/srv/www/shop/app/a")
	e.put("/srv/www/shop/app/a/index.php", "own index")
	e.put("/srv/www/shop/app/a/config.php", "own config")
	chownAll(t, shopUID, a, a+"/index.php", a+"/config.php")
	hit := false
	onStep(t, func(s, _ string) {
		if s == stage && !hit {
			hit = true
			swapForLink(t, a, "../../blog/app")
		}
	})
	return e, &hit
}

// Deleting removes (or moves to the trash) the entry that was looked at.
func TestDeleteActsOnTheEntryItLookedAt(t *testing.T) {
	for _, permanent := range []bool{true, false} {
		e, hit := entryEnv(t, "delete")
		op, _, log := e.run(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "sites", Paths: []string{"shop/app/a/index.php"}, Permanent: permanent})
		if !*hit || op.State != helperapi.OpSucceeded {
			t.Errorf("delete (permanent=%v): hit=%v %+v\n%s", permanent, *hit, op, log)
		}
		if e.exists("/srv/www/shop/app/a.real/index.php") {
			t.Errorf("delete (permanent=%v): the entry looked at is still there", permanent)
		}
		otherSiteIntact(e, "delete")
	}
}

// A new entry is given to its directory's owner itself, not whatever its
// path names by then.
func TestNewEntryIsAdoptedItself(t *testing.T) {
	for _, action := range []string{helperapi.ActionFileTouch, helperapi.ActionFileMkdir} {
		e, hit := entryEnv(t, "create")
		if err := os.Remove(e.abs("/srv/www/shop/app/a/index.php")); err != nil {
			t.Fatal(err)
		}
		_, err := call[helperapi.FileEntry](t, e.r, action, helperapi.FileNewRequest{Root: "sites", Dir: "shop/app/a", Name: "index.php"})
		if !*hit || err != nil {
			t.Errorf("%s: hit=%v %v", action, *hit, err)
		}
		if fi, err := os.Lstat(e.abs("/srv/www/shop/app/a.real/index.php")); err != nil || uidOf(fi) != shopUID {
			t.Errorf("%s: the new entry is not its directory's owner's: %v", action, err)
		}
		otherSiteIntact(e, action)
	}
}

// A rename moves the entry that was checked, to the name checked to be
// free, in one directory.
func TestRenameStaysInItsDirectory(t *testing.T) {
	e, hit := entryEnv(t, "rename")
	if err := os.Remove(e.abs("/srv/www/shop/app/a/index.php")); err != nil {
		t.Fatal(err)
	}
	_, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileRename, helperapi.FileRenameRequest{Root: "sites", Path: "shop/app/a/config.php", Name: "index.php"})
	if !*hit || err != nil {
		t.Errorf("rename: hit=%v %v", *hit, err)
	}
	if e.read("/srv/www/shop/app/a.real/index.php") != "own config" || e.exists("/srv/www/shop/app/a.real/config.php") {
		t.Error("the entry was not renamed in the directory that was opened")
	}
	otherSiteIntact(e, "rename")
}

// A save replaces the file in the directory that was checked.
func TestSaveStaysInItsDirectory(t *testing.T) {
	e, hit := entryEnv(t, "write")
	c, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, helperapi.FileRef{Root: "sites", Path: "shop/app/a/index.php"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = call[helperapi.FileSaved](t, e.r, helperapi.ActionFileWrite, helperapi.FileWriteRequest{Root: "sites", Path: "shop/app/a/index.php", Content: "edited", ETag: c.ETag})
	if !*hit || err != nil {
		t.Errorf("save: hit=%v %v", *hit, err)
	}
	if e.read("/srv/www/shop/app/a.real/index.php") != "edited" {
		t.Error("the save did not land in the directory that was opened")
	}
	if left, _ := filepath.Glob(e.abs("/srv/www/shop/app/a.real/.cairn-save-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
	otherSiteIntact(e, "save")
}

// A path under one site never reaches another site's files through a
// link its owner left there.
func TestLinksBetweenSitesAreNotFollowed(t *testing.T) {
	needRoot(t)
	e := newFilesEnv(t)
	otherSite(e)
	e.put("/srv/www/blog/app/pack.tar", tarOf(t, tarEntry{&tar.Header{Name: "packed.txt", Typeflag: tar.TypeReg}, "blog packed"}))
	e.put("/srv/www/blog/app/sub/inner", "blog inner")
	if err := os.Symlink("../../blog/app", e.abs("/srv/www/shop/app/x")); err != nil {
		t.Fatal(err)
	}
	intact := func(what string) {
		t.Helper()
		otherSiteIntact(e, what, "pack.tar", "sub")
		if !e.exists("/srv/www/blog/app/pack.tar") || e.read("/srv/www/blog/app/sub/inner") != "blog inner" {
			t.Errorf("%s: the other site's files changed", what)
		}
		if entries, _ := os.ReadDir(e.abs("/srv/www/blog/app/sub")); len(entries) != 1 {
			t.Errorf("%s: blog/app/sub now holds %v", what, entries)
		}
	}
	ref := func(p string) helperapi.FileRef { return helperapi.FileRef{Root: "sites", Path: p} }

	// Reading.
	if l, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "sites", Path: "shop/app/x"}); err == nil {
		t.Errorf("listing through the link: %+v", l.Entries)
	}
	if st, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileStat, ref("shop/app/x/secret")); err == nil {
		t.Errorf("stat through the link: %+v", st)
	}
	if c, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, ref("shop/app/x/secret")); err == nil {
		t.Errorf("reading through the link: %q", c.Content)
	}
	if _, f, err := e.f.open(t.Context(), helperapi.Caller{}, ref("shop/app/x/secret")); err == nil {
		_ = f.Close()
		t.Error("opening through the link")
	}
	if s, err := call[helperapi.FileSearchResult](t, e.r, helperapi.ActionFileSearch, helperapi.FileSearchRequest{Root: "sites", Path: "shop/app/x", Query: "blog", Content: true}); err == nil && len(s.Matches) != 0 {
		t.Errorf("searching through the link: %+v", s.Matches)
	}
	if s, err := call[helperapi.FileSearchResult](t, e.r, helperapi.ActionFileSearch, helperapi.FileSearchRequest{Root: "sites", Path: "shop", Query: "blog", Content: true}); err != nil || len(s.Matches) != 0 {
		t.Errorf("searching the site: %+v %v", s.Matches, err)
	}

	// Changing.
	if _, err := call[helperapi.FileSaved](t, e.r, helperapi.ActionFileWrite, helperapi.FileWriteRequest{Root: "sites", Path: "shop/app/x/sub/new.txt", Content: "x"}); err == nil {
		t.Error("saving through the link")
	}
	if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileMkdir, helperapi.FileNewRequest{Root: "sites", Dir: "shop/app/x/sub", Name: "new"}); err == nil {
		t.Error("making a directory through the link")
	}
	if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileRename, helperapi.FileRenameRequest{Root: "sites", Path: "shop/app/x/config.php", Name: "c.php"}); err == nil {
		t.Error("renaming through the link")
	}
	intact("synchronous changes")
	e.put("/srv/www/shop/app/own.txt", "own")
	e.put("/srv/www/shop/app/own.tar", tarOf(t, tarEntry{&tar.Header{Name: "unpacked.txt", Typeflag: tar.TypeReg}, "own"}))
	id := putUpload(e, "uploaded")
	for _, c := range []struct {
		what, action string
		req          any
	}{
		{"chmod through the link", helperapi.ActionFileChmod, helperapi.FileChmodRequest{Root: "sites", Paths: []string{"shop/app/x/secret"}, Mode: 0o644}},
		{"chown through the link", helperapi.ActionFileChown, helperapi.FileChownRequest{Root: "sites", Paths: []string{"shop/app/x/index.php"}, User: "nobody"}},
		{"delete through the link", helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "sites", Paths: []string{"shop/app/x/index.php"}}},
		{"copy from behind the link", helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/x/secret"}, Dest: "shop/app"}},
		{"move from behind the link", helperapi.ActionFileMove, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/x/config.php"}, Dest: "shop/app"}},
		{"extract from behind the link", helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/x/pack.tar", Dest: "shop/app"}},
		{"compress behind the link", helperapi.ActionFileCompress, helperapi.FileCompressRequest{Root: "sites", Dir: "shop/app/x/sub", Names: []string{"inner"}, Format: "zip", Name: "s.zip"}},
		{"copy to behind the link", helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/own.txt"}, Dest: "shop/app/x/sub"}},
		{"move to behind the link", helperapi.ActionFileMove, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/own.txt"}, Dest: "shop/app/x/sub"}},
		{"extract to behind the link", helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/own.tar", Dest: "shop/app/x/sub"}},
		{"upload to behind the link", helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: id, Root: "sites", Dir: "shop/app/x/sub", Name: "u.txt"}},
	} {
		if op, _, log := e.run(c.action, c.req); op.State != helperapi.OpFailed {
			t.Errorf("%s: %+v\n%s", c.what, op, log)
		}
		intact(c.what)
	}
	for _, p := range []string{"/srv/www/shop/app/secret", "/srv/www/shop/app/config.php", "/srv/www/shop/app/packed.txt"} {
		if e.exists(p) {
			t.Errorf("%s came from the other site", p)
		}
	}
	if e.read("/srv/www/shop/app/own.txt") != "own" {
		t.Error("a move that failed lost its source")
	}

	// The link itself is an entry like any other: shown, and removed
	// without touching what it points to.
	if st, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileStat, ref("shop/app/x")); err != nil || st.Type != helperapi.FileTypeLink || st.Target != "../../blog/app" {
		t.Errorf("the link itself: %+v %v", st, err)
	}
	if op, _, log := e.run(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "sites", Paths: []string{"shop/app/x"}, Permanent: true}); op.State != helperapi.OpSucceeded || e.exists("/srv/www/shop/app/x") {
		t.Errorf("deleting the link: %+v\n%s", op, log)
	}
	intact("deleting the link")
}
