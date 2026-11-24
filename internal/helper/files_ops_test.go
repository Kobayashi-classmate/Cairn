// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

func TestBackupsRootLinkAndUnknownRoot(t *testing.T) {
	e := newFilesEnv(t)
	// The data directory is the panel's: a link in place of backups/ must
	// not make "/" (here: the test root's /etc) the backups root.
	if err := os.Symlink(filepath.Join(e.f.Root, "/etc"), filepath.Join(e.f.Root, "/var/lib/cairn/backups")); err != nil {
		t.Fatal(err)
	}
	if _, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "backups"}); errCode(err) != helperapi.CodeFileRootUnavailable {
		t.Fatalf("backups root through a link: %v", err)
	}
	for _, id := range []string{"", "other"} {
		if r, err := e.f.openRoot(id); errCode(err) != helperapi.CodeFileRootUnavailable {
			if r != nil {
				_ = r.Close()
			}
			t.Fatalf("root %q: %v", id, err)
		}
	}
	if err := os.Remove(filepath.Join(e.f.Root, "/var/lib/cairn/backups")); err != nil {
		t.Fatal(err)
	}
	if l, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "backups"}); err != nil || len(l.Entries) != 0 {
		t.Fatalf("backups root made on first use: %+v %v", l, err)
	}
}

// A purge keeps the backups; the panel account of a new installation
// may have another uid. The helper takes the directory over and lists
// what was kept (the package hands the files over, postinstall.sh).
func TestBackupsRootTakesOverKeptDirectory(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root to give the directory to another uid")
	}
	e := newFilesEnv(t)
	e.put("/var/lib/cairn/backups/db/mysql/shop.sql.gz", "dump")
	dir := filepath.Join(e.f.Root, "/var/lib/cairn/backups")
	for _, p := range []string{dir, dir + "/db", dir + "/db/mysql", dir + "/db/mysql/shop.sql.gz"} {
		if err := os.Lchown(p, 4242, 4242); err != nil {
			t.Fatal(err)
		}
	}
	l, err := call[helperapi.FileList](t, e.r, helperapi.ActionFileList, helperapi.FileListRequest{Root: "backups", Path: "db/mysql"})
	if err != nil || len(l.Entries) != 1 || l.Entries[0].Name != "shop.sql.gz" {
		t.Fatalf("kept backups: %+v %v", l, err)
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(dir, &st); err != nil || int(st.Uid) != os.Getuid() {
		t.Fatalf("backups root owner: %d %v", st.Uid, err)
	}
}

func TestRecursiveChangesSkipLinksAndHardLinks(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/blog/app/secret", "blog secret") // 0600
	secret := filepath.Join(e.f.Root, "/srv/www/blog/app/secret")
	if err := os.Symlink("../../blog/app", filepath.Join(e.f.Root, "/srv/www/shop/app/peek")); err != nil {
		t.Fatal(err)
	}
	e.put("/srv/www/shop/app/dir/own.txt", "own")
	if err := os.Link(secret, filepath.Join(e.f.Root, "/srv/www/shop/app/dir/planted")); err != nil {
		t.Fatal(err)
	}
	// The tree's own hard links (pnpm's store) are the owner's files.
	if err := os.Link(filepath.Join(e.f.Root, "/srv/www/shop/app/dir/own.txt"), filepath.Join(e.f.Root, "/srv/www/shop/app/dir/own-link")); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() == 0 {
		// The other site's file is the other site's.
		if err := os.Chown(secret, 4321, 4321); err != nil {
			t.Fatal(err)
		}
	}
	// A link given as the start is not walked.
	if op, _, log := e.run(helperapi.ActionFileChmod, helperapi.FileChmodRequest{Root: "sites", Paths: []string{"shop/app/peek"}, Mode: 0o666, DirMode: 0o777, Recursive: true}); op.State != helperapi.OpSucceeded {
		t.Fatalf("chmod: %+v\n%s", op, log)
	}
	if fi, _ := os.Stat(secret); fi.Mode().Perm() != 0o600 {
		t.Fatalf("changed through a link: %v", fi.Mode())
	}
	if os.Getuid() != 0 {
		return // without root, every file here is the tester's
	}
	// A file with another name that belongs to someone else is left
	// alone; the tree's own hard links change with it.
	op, _, log := e.run(helperapi.ActionFileChmod, helperapi.FileChmodRequest{Root: "sites", Paths: []string{"shop/app/dir"}, Mode: 0o644, DirMode: 0o755, Recursive: true})
	if op.State != helperapi.OpSucceeded || !strings.Contains(log, "planted (a hard link") || strings.Contains(log, "own-link") {
		t.Fatalf("chmod: %+v\n%s", op, log)
	}
	if fi, _ := os.Stat(secret); fi.Mode().Perm() != 0o600 {
		t.Fatalf("changed through a hard link: %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(e.f.Root, "/srv/www/shop/app/dir/own.txt")); fi.Mode().Perm() != 0o644 {
		t.Fatalf("own file: %v", fi.Mode())
	}
	// Moving a link into another owner's directory gives them the link,
	// not the tree it points to.
	if err := os.MkdirAll(filepath.Join(e.f.Root, "/srv/www/shop/app/other"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(filepath.Join(e.f.Root, "/srv/www/shop/app/other"), 1234, 1235); err != nil {
		t.Fatal(err)
	}
	if op, _, log := e.run(helperapi.ActionFileMove, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/peek", "shop/app/dir"}, Dest: "shop/app/other"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("move: %+v\n%s", op, log)
	}
	if uid, _ := fileOwnerOf(t, secret); uid != 4321 {
		t.Fatalf("moved link: its target changed owner to %d", uid)
	}
	if uid, _ := fileOwnerOf(t, filepath.Join(e.f.Root, "/srv/www/shop/app/other/peek")); uid != 1234 {
		t.Fatalf("the link itself is the new owner's: %d", uid)
	}
	if uid, _ := fileOwnerOf(t, filepath.Join(e.f.Root, "/srv/www/shop/app/other/dir/own.txt")); uid != 1234 {
		t.Fatalf("moved tree: %d", uid)
	}
}

func fileOwnerOf(t *testing.T, p string) (int, int) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fileOwner(fi)
}

func TestReadsDoNotHangOnPipes(t *testing.T) {
	e := newFilesEnv(t)
	if err := syscall.Mkfifo(filepath.Join(e.f.Root, "/srv/www/shop/app/pipe"), 0o644); err != nil {
		t.Skip("no fifo:", err)
	}
	if err := os.MkdirAll(filepath.Join(e.f.Root, "/srv/www/.cairn-trash"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(e.f.Root, "/srv/www/.cairn-trash/0123456789abcdef.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	read, list := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := call[helperapi.FileContent](t, e.r, helperapi.ActionFileRead, helperapi.FileRef{Root: "sites", Path: "shop/app/pipe"})
		read <- err
	}()
	go func() {
		l, err := call[helperapi.TrashList](t, e.r, helperapi.ActionTrashList, helperapi.FileRootRef{Root: "sites"})
		if err == nil && len(l.Entries) != 0 {
			err = os.ErrInvalid
		}
		list <- err
	}()
	for _, c := range []struct {
		ch   chan error
		what string
		code string
	}{{read, "reading a pipe", helperapi.CodeFileBinary}, {list, "trash list with a pipe", ""}} {
		select {
		case err := <-c.ch:
			if errCode(err) != c.code {
				t.Fatalf("%s: %v", c.what, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s hung", c.what)
		}
	}
	if op, _, _ := e.run(helperapi.ActionTrashRestore, helperapi.TrashRequest{Root: "sites", IDs: []string{"0123456789abcdef"}}); op.State != helperapi.OpFailed {
		t.Fatalf("restoring from a pipe: %+v", op)
	}
}

func TestTrashRestoreParentsAndPurge(t *testing.T) {
	e := newFilesEnv(t)
	app := filepath.Join(e.f.Root, "/srv/www/shop/app")
	asRoot := os.Getuid() == 0
	if asRoot {
		if err := os.Chown(app, 1234, 1235); err != nil {
			t.Fatal(err)
		}
	}
	e.put("/srv/www/shop/app/deep/sub/f.txt", "f")
	if op, _, _ := e.run(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "sites", Paths: []string{"shop/app/deep/sub/f.txt"}}); op.State != helperapi.OpSucceeded {
		t.Fatalf("delete: %+v", op)
	}
	if err := os.RemoveAll(filepath.Join(app, "deep")); err != nil {
		t.Fatal(err)
	}
	trash, _ := call[helperapi.TrashList](t, e.r, helperapi.ActionTrashList, helperapi.FileRootRef{Root: "sites"})
	if len(trash.Entries) != 1 {
		t.Fatalf("trash %+v", trash)
	}
	if op, _, log := e.run(helperapi.ActionTrashRestore, helperapi.TrashRequest{Root: "sites", IDs: []string{trash.Entries[0].ID}}); op.State != helperapi.OpSucceeded {
		t.Fatalf("restore: %+v\n%s", op, log)
	}
	if e.read("/srv/www/shop/app/deep/sub/f.txt") != "f" {
		t.Fatal("restored content")
	}
	if asRoot {
		for _, d := range []string{"deep", "deep/sub"} {
			if uid, gid := fileOwnerOf(t, filepath.Join(app, d)); uid != 1234 || gid != 1235 {
				t.Fatalf("a parent made again belongs to the directory's owner: %s %d:%d", d, uid, gid)
			}
		}
	}
	// Purging an entry that is not there is a failure, not a success.
	if op, _, _ := e.run(helperapi.ActionTrashPurge, helperapi.TrashPurgeRequest{Root: "sites", IDs: []string{"0123456789abcdef"}}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeFileNotFound {
		t.Fatalf("purging a missing entry: %+v", op)
	}
}

func trashNames(e *filesEnv) []string {
	names, _ := filepath.Glob(filepath.Join(e.f.Root, "/srv/www/.cairn-trash/*"))
	return names
}

func TestTransfersCannotReplaceTheTrash(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/shop/app/junk.txt", "junk")
	if op, _, log := e.run(helperapi.ActionFileDelete, helperapi.FileDeleteRequest{Root: "sites", Paths: []string{"shop/app/junk.txt"}}); op.State != helperapi.OpSucceeded {
		t.Fatalf("delete: %+v\n%s", op, log)
	}
	before := trashNames(e)
	// A directory named like the trash, with an entry's description made
	// up in it, copied or moved to the top of the root.
	e.put("/srv/www/shop/app/fake/.cairn-trash/0123456789abcdef.json", `{"path":"shop/app/x","type":"file"}`)
	for _, action := range []string{helperapi.ActionFileCopy, helperapi.ActionFileMove} {
		op, _, log := e.run(action, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/fake/.cairn-trash"}, Dest: "", Overwrite: true})
		if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeFileProtected {
			t.Errorf("%s over the trash: %+v\n%s", action, op, log)
		}
		if after := trashNames(e); strings.Join(after, " ") != strings.Join(before, " ") {
			t.Errorf("%s over the trash: it held %v, now %v", action, before, after)
		}
	}
	// Nor does an entry's description put anything into the trash (or, in
	// the system root, over a top-level directory) by being restored.
	e.put("/srv/www/.cairn-trash/fedcba9876543210/1111111111111111.json", `{"path":"etc/passwd","type":"file"}`)
	e.put("/srv/www/.cairn-trash/fedcba9876543210.json", `{"path":".cairn-trash/1111111111111111.json","type":"file"}`)
	if op, _, log := e.run(helperapi.ActionTrashRestore, helperapi.TrashRequest{Root: "sites", IDs: []string{"fedcba9876543210"}}); op.State != helperapi.OpFailed ||
		op.Error.Code != helperapi.CodeFileProtected || e.exists("/srv/www/.cairn-trash/1111111111111111.json") {
		t.Errorf("restoring into the trash: %+v\n%s", op, log)
	}
}

func TestReplacedDirectoryGoesToTheTrash(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/shop/app/site/keep.txt", "old only")
	e.put("/srv/www/shop/app/new/site/index.php", "new")
	e.put("/srv/www/shop/app/new/a.txt", "new a")
	e.put("/srv/www/shop/app/a.txt", "old a")
	op, res, log := e.run(helperapi.ActionFileCopy, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/new/site", "shop/app/new/a.txt"},
		Dest: "shop/app", Overwrite: true})
	if op.State != helperapi.OpSucceeded || res.Done != 2 || e.read("/srv/www/shop/app/site/index.php") != "new" || e.exists("/srv/www/shop/app/site/keep.txt") {
		t.Fatalf("copy over a directory: %+v %+v\n%s", op, res, log)
	}
	// What the directory held is not lost for good; a file is replaced as
	// ever, and nothing is left lying around.
	trash, _ := call[helperapi.TrashList](t, e.r, helperapi.ActionTrashList, helperapi.FileRootRef{Root: "sites"})
	if len(trash.Entries) != 1 || trash.Entries[0].Path != "shop/app/site" || trash.Entries[0].Type != helperapi.FileTypeDir || trash.Entries[0].User != "admin" {
		t.Fatalf("the replaced directory belongs in the trash: %+v", trash.Entries)
	}
	if left, _ := filepath.Glob(filepath.Join(e.f.Root, "/srv/www/shop/app/.cairn-old-*")); e.read("/srv/www/shop/app/a.txt") != "new a" || len(left) != 0 {
		t.Fatalf("a replaced file: %q, left %v", e.read("/srv/www/shop/app/a.txt"), left)
	}
	// From there it comes back, replacing in turn.
	if op, _, log := e.run(helperapi.ActionTrashRestore, helperapi.TrashRequest{Root: "sites", IDs: []string{trash.Entries[0].ID}}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeFileExists {
		t.Fatalf("restore over an entry without overwrite: %+v\n%s", op, log)
	}
	if op, _, log := e.run(helperapi.ActionTrashRestore, helperapi.TrashRequest{Root: "sites", IDs: []string{trash.Entries[0].ID}, Overwrite: true}); op.State != helperapi.OpSucceeded ||
		e.read("/srv/www/shop/app/site/keep.txt") != "old only" || e.exists("/srv/www/shop/app/site/index.php") {
		t.Fatalf("restore with overwrite: %+v\n%s", op, log)
	}
	trash, _ = call[helperapi.TrashList](t, e.r, helperapi.ActionTrashList, helperapi.FileRootRef{Root: "sites"})
	if len(trash.Entries) != 1 || trash.Entries[0].Path != "shop/app/site" {
		t.Fatalf("the directory replaced by the restore: %+v", trash.Entries)
	}
	// Moving over a directory keeps it the same way.
	e.put("/srv/www/shop/app/new/site/moved.txt", "moved")
	if op, _, log := e.run(helperapi.ActionFileMove, helperapi.FileTransferRequest{Root: "sites", Paths: []string{"shop/app/new/site"}, Dest: "shop/app", Overwrite: true}); op.State != helperapi.OpSucceeded ||
		e.read("/srv/www/shop/app/site/moved.txt") != "moved" || e.exists("/srv/www/shop/app/new/site") {
		t.Fatalf("move over a directory: %+v\n%s", op, log)
	}
	if trash, _ = call[helperapi.TrashList](t, e.r, helperapi.ActionTrashList, helperapi.FileRootRef{Root: "sites"}); len(trash.Entries) != 2 {
		t.Fatalf("the directory replaced by the move: %+v", trash.Entries)
	}
}

func TestMakeRoomPutsBackOnFailure(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/shop/app/a.txt", "old")
	root, err := e.f.openRoot(helperapi.RootSites)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	app, err := root.dir("shop/app", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	bin := &trashBin{root: root, user: "admin"}
	defer bin.close()
	var log strings.Builder
	if _, err := makeRoom(app, "a.txt", "shop/app/a.txt", false, bin, &log); errCode(err) != helperapi.CodeFileExists {
		t.Fatalf("without overwrite: %v", err)
	}
	settle, err := makeRoom(app, "a.txt", "shop/app/a.txt", true, bin, &log)
	if err != nil || e.exists("/srv/www/shop/app/a.txt") {
		t.Fatalf("make room: %v", err)
	}
	settle(false)
	if e.read("/srv/www/shop/app/a.txt") != "old" {
		t.Fatal("a failed copy or move loses the entry it was to replace")
	}
	settle, _ = makeRoom(app, "a.txt", "shop/app/a.txt", true, bin, &log)
	e.put("/srv/www/shop/app/a.txt", "new")
	settle(true)
	if left, _ := filepath.Glob(filepath.Join(e.f.Root, "/srv/www/shop/app/.cairn-old-*")); e.read("/srv/www/shop/app/a.txt") != "new" || len(left) != 0 {
		t.Fatalf("settled: %v", left)
	}
	// A directory waits in the trash, and comes back from there.
	e.put("/srv/www/shop/app/d/keep.txt", "kept")
	settle, err = makeRoom(app, "d", "shop/app/d", true, bin, &log)
	if err != nil || e.exists("/srv/www/shop/app/d") || len(trashNames(e)) != 2 {
		t.Fatalf("make room for a directory: %v, trash %v", err, trashNames(e))
	}
	settle(false)
	if e.read("/srv/www/shop/app/d/keep.txt") != "kept" || len(trashNames(e)) != 0 {
		t.Fatalf("a failed copy or move loses the directory it was to replace: trash %v", trashNames(e))
	}
	if log.Len() != 0 {
		t.Fatalf("nothing went wrong: %s", log.String())
	}
}
