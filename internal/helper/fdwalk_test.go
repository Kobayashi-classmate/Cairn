// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// swapOnEnter replaces the directory name, just before a walk enters it
// (the n-th time), with a link to target: what a site's owner can do to
// their own tree between two steps of a walk. undo puts it back (at, the
// directory's real path).
func swapOnEnter(t *testing.T, name, at, target string, nth int) (swapped func() bool, undo func()) {
	t.Helper()
	old := enterHook
	t.Cleanup(func() { enterHook = old })
	seen, done := 0, false
	enterHook = func(dir *os.Root, n string) {
		if n != name || done {
			return
		}
		if seen++; seen < nth {
			return
		}
		done = true
		if err := dir.Rename(n, n+".real"); err != nil {
			t.Error(err)
		}
		if err := dir.Symlink(target, n); err != nil {
			t.Error(err)
		}
	}
	return func() bool { return done }, func() {
		if done {
			_ = os.Remove(at)
			_ = os.Rename(at+".real", at)
		}
		seen, done = 0, false
	}
}

func tarNames(t *testing.T, r io.Reader) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err != nil {
			return names
		}
		names = append(names, h.Name)
	}
}

func TestWalksDoNotFollowSwappedDirs(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/shop/app/tree/sub/f.txt", "own")
	e.put("/srv/www/blog/app/secret", "blog secret")
	secret := filepath.Join(e.f.Root, "/srv/www/blog/app/secret")
	if err := os.Chmod(secret, 0o640); err != nil { //nolint:gosec // G302: the mode under test
		t.Fatal(err)
	}
	root := os.Getuid() == 0
	if root {
		if err := os.Chown(secret, 4321, 4321); err != nil {
			t.Fatal(err)
		}
	}
	untouched := func(what string) {
		t.Helper()
		fi, err := os.Lstat(secret)
		if err != nil || fi.Mode().Perm() != 0o640 {
			t.Fatalf("%s: the other site's file changed: %v %v", what, fi, err)
		}
		if uid, _ := fileOwner(fi); root && uid != 4321 {
			t.Fatalf("%s: the other site's file changed owner: %d", what, uid)
		}
		if e.exists("/srv/www/blog/app/b.txt") {
			t.Fatalf("%s: written into the other site", what)
		}
	}
	// tree/sub becomes a link to the other site once the walk is in tree.
	swapped, undo := swapOnEnter(t, "sub", filepath.Join(e.f.Root, "/srv/www/shop/app/tree/sub"), "../../../blog/app", 1)

	if op, _, log := e.run(helperapi.ActionFileChmod, helperapi.FileChmodRequest{Root: "sites", Paths: []string{"shop/app/tree"}, Mode: 0o600, DirMode: 0o700, Recursive: true}); op.State != helperapi.OpSucceeded || !swapped() {
		t.Fatalf("chmod: %+v %v\n%s", op, swapped(), log)
	}
	untouched("chmod")
	undo()

	sites, err := e.f.openRoot("sites")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sites.Close() }()
	app, err := sites.dir("shop/app", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	if root {
		if op, _, log := e.run(helperapi.ActionFileChown, helperapi.FileChownRequest{Root: "sites", Paths: []string{"shop/app/tree"}, User: "nobody", Recursive: true}); op.State != helperapi.OpSucceeded || !swapped() {
			t.Fatalf("chown: %+v\n%s", op, log)
		}
		untouched("chown")
		undo()
		if err := chownTree(context.Background(), app, "tree", "shop/app/tree", 1234, 1234, io.Discard); err != nil || !swapped() {
			t.Fatalf("chownTree: %v", err)
		}
		untouched("chownTree")
		undo()
	}

	if err := os.Mkdir(filepath.Join(e.f.Root, "/srv/www/shop/app/cp"), 0o750); err != nil {
		t.Fatal(err)
	}
	if op, _, log := e.run(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/tree"}, Dest: "shop/app/cp"}); op.State != helperapi.OpSucceeded || !swapped() {
		t.Fatalf("copy: %+v\n%s", op, log)
	}
	if e.exists("/srv/www/shop/app/cp/tree/sub/secret") || e.exists("/srv/www/shop/app/cp/tree/sub.real/secret") {
		t.Fatal("copy: the other site's file was copied")
	}
	undo()

	if op, _, log := e.run(helperapi.ActionFileCompress, helperapi.FileCompressRequest{Root: "sites", Dir: "shop/app", Names: []string{"tree"}, Format: "tar.gz", Name: "t.tar.gz"}); op.State != helperapi.OpSucceeded || !swapped() {
		t.Fatalf("compress: %+v\n%s", op, log)
	}
	data, _ := os.ReadFile(filepath.Join(e.f.Root, "/srv/www/shop/app/t.tar.gz"))
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(tarNames(t, zr), " "); strings.Contains(names, "secret") {
		t.Fatalf("compress: the other site's file was archived: %s", names)
	}
	undo()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tarTree(context.Background(), app, "tree", tw, nil, io.Discard); err != nil || !swapped() {
		t.Fatalf("tarTree: %v", err)
	}
	_ = tw.Close()
	if names := strings.Join(tarNames(t, &buf), " "); strings.Contains(names, "secret") || !strings.Contains(names, "sub/") {
		t.Fatalf("tarTree: %s", names)
	}
	undo()

	// Extraction: d is made by the first entry, then swapped for a link
	// before the third one goes into it.
	e.put("/srv/www/shop/app/x.tar", tarOf(t,
		tarEntry{&tar.Header{Name: "d/", Typeflag: tar.TypeDir, Mode: 0o750}, ""},
		tarEntry{&tar.Header{Name: "d/a.txt", Typeflag: tar.TypeReg}, "a"},
		tarEntry{&tar.Header{Name: "d/b.txt", Typeflag: tar.TypeReg}, "b"},
	))
	if err := os.Mkdir(filepath.Join(e.f.Root, "/srv/www/shop/app/out"), 0o750); err != nil {
		t.Fatal(err)
	}
	_, undo2 := swapOnEnter(t, "d", filepath.Join(e.f.Root, "/srv/www/shop/app/out/d"), "../../../blog/app", 3)
	op, res, log := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/x.tar", Dest: "shop/app/out"})
	if op.State != helperapi.OpSucceeded || res.Done != 1 || len(res.Failed) != 1 {
		t.Fatalf("extract: %+v %+v\n%s", op, res, log)
	}
	untouched("extract")
	if e.read("/srv/www/shop/app/out/d.real/a.txt") != "a" {
		t.Fatal("extract: the entry before the swap")
	}
	undo2()
}

// renameAt takes plain names only: the system call resolves anything more
// from the open directory on, past the roots' guard.
func TestRenameAtTakesPlainNamesOnly(t *testing.T) {
	base := t.TempDir()
	for _, d := range []string{"site/in", "other"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"site/a", "other/secret"} {
		if err := os.WriteFile(filepath.Join(base, f), []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	site, err := os.OpenRoot(filepath.Join(base, "site"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = site.Close() }()
	for _, c := range [][2]string{
		{"../other/secret", "stolen"}, {"a", "../other/planted"}, {"a", "in/b"}, {"in/../a", "b"},
		{"a", ""}, {"", "b"}, {"a", "."}, {"..", "b"},
	} {
		if err := renameAt(site, c[0], site, c[1], true); err == nil {
			t.Errorf("renameAt(%q, %q) went through", c[0], c[1])
		}
	}
	if _, err := os.Stat(filepath.Join(base, "other/secret")); err != nil {
		t.Errorf("the file outside the directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "other/planted")); err == nil {
		t.Error("a file was moved outside the directory")
	}
	// Plain names still move, and an entry in the way is kept.
	if err := renameAt(site, "a", site, "b", true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "site/c"), []byte("c"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renameAt(site, "b", site, "c", true); err == nil {
		t.Error("renameAt with keep replaced an entry")
	}
}
