// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// A directory copied into itself through a link inside the site ends with
// an error, and with nothing left of the attempt: the names give no hint
// that the destination lies inside the source.
func TestCopyIntoItselfThroughALink(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/shop/app/a/index.php", "index")
	if err := os.MkdirAll(e.abs("/srv/www/shop/app/a/sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", e.abs("/srv/www/shop/app/l")); err != nil {
		t.Fatal(err)
	}
	op, res, log := e.run(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/a"}, Dest: "shop/app/l/sub"})
	code := ""
	if op.Error != nil {
		code = op.Error.Code
	}
	if op.State != helperapi.OpFailed || code != helperapi.CodeFileIntoItself {
		t.Fatalf("copy into itself: %+v %+v\n%s", op, res, log)
	}
	if left, err := os.ReadDir(e.abs("/srv/www/shop/app/a/sub")); err != nil || len(left) != 0 {
		t.Fatalf("left in the destination: %v %v", left, err)
	}
	if e.read("/srv/www/shop/app/a/index.php") != "index" {
		t.Fatal("the source changed")
	}
	// Beside itself it copies as ever.
	if err := os.Mkdir(e.abs("/srv/www/shop/app/b"), 0o750); err != nil {
		t.Fatal(err)
	}
	if op, res, log := e.run(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/a"}, Dest: "shop/app/b"}); op.State != helperapi.OpSucceeded ||
		e.read("/srv/www/shop/app/b/a/index.php") != "index" {
		t.Fatalf("copy beside itself: %+v %+v\n%s", op, res, log)
	}
}

// A tree nested deeper than any path reaches is not followed to its end: a
// walk holds a descriptor and a path for every level it is in.
func TestWalkAndCopyStopAtDepth(t *testing.T) {
	base := t.TempDir()
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	cur := root
	for range maxTreeDepth + 5 {
		if err := cur.Mkdir("d", 0o750); err != nil {
			t.Fatal(err)
		}
		next, err := cur.OpenRoot("d")
		if err != nil {
			t.Fatal(err)
		}
		if cur != root {
			_ = cur.Close()
		}
		cur = next
	}
	_ = cur.Close()

	seen := 0
	err = walkNamed(context.Background(), root, "d", "d", false, func(walkEntry) error { seen++; return nil })
	if !errors.Is(err, errTooDeep) || seen > maxTreeDepth+2 {
		t.Fatalf("walk: %d entries, %v", seen, err)
	}
	// A lenient walk (a search) leaves the rest out.
	seen = 0
	if err := walkNamed(context.Background(), root, "d", "d", true, func(walkEntry) error { seen++; return nil }); err != nil || seen > maxTreeDepth+2 {
		t.Fatalf("lenient walk: %d entries, %v", seen, err)
	}
	if err := os.Mkdir(filepath.Join(base, "out"), 0o750); err != nil {
		t.Fatal(err)
	}
	out, err := root.OpenRoot("out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	info, err := root.Lstat("d")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := copyAt(context.Background(), root, "d", info, out, "d", os.Getuid(), os.Getgid(), "d", nil, io.Discard); !errors.Is(err, errTooDeep) {
		t.Fatalf("copy: %v", err)
	}
}

// Saving a new file does not replace one that appeared after the look.
func TestSaveOfANewFileKeepsOneMadeMeanwhile(t *testing.T) {
	e := newFilesEnv(t)
	onStep(t, func(stage, p string) {
		if stage == "write" {
			e.put("/srv/www/"+p, "theirs")
		}
	})
	_, err := call[helperapi.FileSaved](t, e.r, helperapi.ActionFileWrite, helperapi.FileWriteRequest{Root: "sites", Path: "shop/app/new.txt", Content: "mine"})
	if errCode(err) != helperapi.CodeFileExists {
		t.Fatalf("save over a file made meanwhile: %v", err)
	}
	if got := e.read("/srv/www/shop/app/new.txt"); got != "theirs" {
		t.Fatalf("the file made meanwhile: %q", got)
	}
	left, err := filepath.Glob(e.abs("/srv/www/shop/app/.cairn-save-*"))
	if err != nil || len(left) != 0 {
		t.Fatalf("temporary files left: %v %v", left, err)
	}
	// With nothing in the way a new file is saved, and saved again by its
	// ETag.
	stepHook = nil
	saved, err := call[helperapi.FileSaved](t, e.r, helperapi.ActionFileWrite, helperapi.FileWriteRequest{Root: "sites", Path: "shop/app/other.txt", Content: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call[helperapi.FileSaved](t, e.r, helperapi.ActionFileWrite, helperapi.FileWriteRequest{Root: "sites", Path: "shop/app/other.txt", Content: "two", ETag: saved.ETag}); err != nil ||
		e.read("/srv/www/shop/app/other.txt") != "two" {
		t.Fatalf("save again: %v", err)
	}
}

// What takes the upload's name after it was opened and checked is not
// what gets placed.
func TestUploadSwappedAfterTheLookIsNotPlaced(t *testing.T) {
	needRoot(t)
	e := newFilesEnv(t)
	chownAll(t, shopUID, e.abs("/srv/www/shop/app"))
	id := putUpload(e, "uploaded")
	part := e.abs("/var/lib/cairn/uploads/" + id + ".part")
	onStep(t, func(stage, _ string) {
		if stage != "place" {
			return
		}
		if err := os.Rename(part, part+".aside"); err != nil {
			t.Error(err)
		}
		if err := os.Symlink("/etc/passwd", part); err != nil {
			t.Error(err)
		}
	})
	op, _, log := e.run(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: id, Root: "sites", Dir: "shop/app", Name: "u.txt"})
	if op.State != helperapi.OpFailed {
		t.Fatalf("a swapped upload was placed: %+v\n%s", op, log)
	}
	if _, err := os.Lstat(e.abs("/srv/www/shop/app/u.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("something was left in the site: %v", err)
	}
}
