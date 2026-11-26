// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"archive/tar"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// siteUsers makes the users of sites shop and blog known; other site users
// are not (a directory of the sites root that is no site).
func siteUsers(t *testing.T) {
	inner := lookupUser
	t.Cleanup(func() { lookupUser = inner })
	users := map[string][2]int{"cw-shop": {1001, 1001}, "cw-shop-run": {1002, 1001}, "cw-blog": {1003, 1003}, "cw-blog-run": {1004, 1003}}
	lookupUser = func(name string) (int, int, error) {
		if u, ok := users[name]; ok {
			return u[0], u[1], nil
		}
		if strings.HasPrefix(name, "cw-") {
			return 0, 0, errors.New("no such user")
		}
		return inner(name)
	}
}

// A site's backup restored into a site's directory gives each file to that
// site's users: the runner's files to the runner, the rest to the owner;
// the site's settings stay root's.
func TestRestoreGivesFilesToTheSitesUsers(t *testing.T) {
	e, _, _ := newBackupEnv(t)
	siteUsers(t)
	e.put("/srv/www/shop/app/index.php", "<?php echo 1;")
	e.put("/srv/www/shop/app/data/upload.bin", "u")
	e.put("/srv/www/shop/app/.env", "SECRET=1")
	e.put("/var/lib/cairn-helper/sites/shop.json", `{"spec":{"code":"shop"}}`)
	base := filepath.Join(e.f.Root, "/srv/www/shop")
	const owner, runner, group = 1001, 1002, 1001
	_ = os.Chown(base, 0, group)
	_ = os.Chmod(base, 0o750) //nolint:gosec // G302: the mode under test
	for p, u := range map[string]int{"app": owner, "app/index.php": owner, "app/.env": owner, "app/data": runner, "app/data/upload.bin": runner} {
		if err := os.Lchown(filepath.Join(base, p), u, group); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Chmod(filepath.Join(base, "app/.env"), 0o600)
	if err := os.Chmod(filepath.Join(base, "app/data"), 0o770|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	// Written by the runner, shared with the owner through the group.
	if err := os.Chmod(filepath.Join(base, "app/data/upload.bin"), 0o660); err != nil { //nolint:gosec // G302: the mode under test
		t.Fatal(err)
	}
	plan := helperapi.BackupPlan{ID: "p9100000000000aa", Retention: helperapi.DefaultRetention,
		Targets: []helperapi.BackupTarget{{Kind: "site", Site: "shop"}}}
	if _, err := call[helperapi.Empty](t, e.r, helperapi.ActionBackupPlanApply, plan); err != nil {
		t.Fatal(err)
	}
	op, _, log := e.run(helperapi.ActionBackupRun, helperapi.PlanRef{Plan: plan.ID})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("run: %+v\n%s", op, log)
	}
	runs, _ := call[helperapi.BackupRuns](t, e.r, helperapi.ActionBackupRuns, helperapi.PlanRef{Plan: plan.ID})
	name := runs.Runs[0].Files[0]

	// In place.
	op, _, log = e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: name,
		Overwrite: true, Dest: helperapi.FileRef{Root: "sites", Path: "shop"}})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("restore: %+v\n%s", op, log)
	}
	for p, want := range map[string]int{"app/index.php": owner, "app/.env": owner, "app/data/upload.bin": runner, "app/data": runner, ".cairn/site.json": 0, ".cairn": 0} {
		if u, g := fileOwnerOf(t, filepath.Join(base, p)); u != want || want != 0 && g != group {
			t.Errorf("after in-place restore: %s owned by %d:%d, want %d", p, u, g, want)
		}
	}
	if u, _ := fileOwnerOf(t, base); u != 0 {
		t.Errorf("the site's directory now belongs to %d", u)
	}
	// A restore keeps the modes that were backed up, group write included.
	restoredModes(t, base)

	// Into another site's directory: that site's users.
	blog := filepath.Join(e.f.Root, "/srv/www/blog")
	if err := os.MkdirAll(blog, 0o750); err != nil {
		t.Fatal(err)
	}
	_ = os.Chown(blog, 0, 1003)
	op, _, log = e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: name,
		Dest: helperapi.FileRef{Root: "sites", Path: "blog"}})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("restore into blog: %+v\n%s", op, log)
	}
	for p, want := range map[string]int{"app": 1003, "app/index.php": 1003, "app/data": 1004, "app/data/upload.bin": 1004, ".cairn/site.json": 0} {
		if u, g := fileOwnerOf(t, filepath.Join(blog, p)); u != want || want != 0 && g != 1003 {
			t.Errorf("restored into blog: %s owned by %d:%d, want %d", p, u, g, want)
		}
	}
	restoredModes(t, blog)
	if fi, _ := os.Lstat(filepath.Join(blog, "app/.env")); fi.Mode().Perm() != 0o600 {
		t.Errorf(".env mode %v", fi.Mode())
	}

	// Into a directory that is no site's: the directory's owner, as before.
	copyDir := filepath.Join(e.f.Root, "/srv/www/copy")
	_ = os.MkdirAll(copyDir, 0o750)
	_ = os.Chown(copyDir, 1500, 1500)
	if op, _, log := e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: name,
		Dest: helperapi.FileRef{Root: "sites", Path: "copy"}}); op.State != helperapi.OpSucceeded {
		t.Fatalf("restore into copy: %+v\n%s", op, log)
	}
	for _, p := range []string{"app/index.php", "app/data/upload.bin", ".cairn/site.json"} {
		if u, g := fileOwnerOf(t, filepath.Join(copyDir, p)); u != 1500 || g != 1500 {
			t.Errorf("restored into copy: %s owned by %d:%d", p, u, g)
		}
	}
}

// restoredModes checks the modes of a site's backup restored into dir.
func restoredModes(t *testing.T, dir string) {
	t.Helper()
	if fi, _ := os.Lstat(filepath.Join(dir, "app/data")); fi.Mode()&os.ModeSetgid == 0 || fi.Mode().Perm() != 0o770 {
		t.Errorf("%s: a restored directory lost its mode: %v", dir, fi.Mode())
	}
	if fi, _ := os.Lstat(filepath.Join(dir, "app/data/upload.bin")); fi.Mode().Perm() != 0o660 {
		t.Errorf("%s: a restored file lost its mode: %v", dir, fi.Mode())
	}
}

// Extracting an archive in the file manager keeps giving everything to the
// destination's owner, whatever the archive says.
func TestExtractGivesFilesToTheDestinationsOwner(t *testing.T) {
	e := newFilesEnv(t)
	dest := filepath.Join(e.f.Root, "/srv/www/shop/app")
	_ = os.Chown(dest, 1001, 1001)
	e.put("/srv/www/shop/app/old.txt", "old")
	e.put("/srv/www/shop/app/a.tar", tarOf(t,
		tarEntry{&tar.Header{Name: "d/", Typeflag: tar.TypeDir, Mode: 0o2770, Uid: 1002, Uname: "cw-shop-run"}, ""},
		tarEntry{&tar.Header{Name: "d/f", Typeflag: tar.TypeReg, Uid: 1002, Uname: "cw-shop-run"}, "f"},
		tarEntry{&tar.Header{Name: "old.txt", Typeflag: tar.TypeReg, Uid: 1002}, "new"},
	))
	if op, _, log := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/a.tar", Dest: "shop/app"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("extract: %+v\n%s", op, log)
	}
	if e.read("/srv/www/shop/app/old.txt") != "old" {
		t.Error("an existing file was replaced without overwrite")
	}
	for _, p := range []string{"d", "d/f"} {
		if u, g := fileOwnerOf(t, filepath.Join(dest, p)); u != 1001 || g != 1001 {
			t.Errorf("%s owned by %d:%d", p, u, g)
		}
	}
	if fi, _ := os.Lstat(filepath.Join(dest, "d")); fi.Mode()&os.ModeSetgid != 0 {
		t.Errorf("an extracted directory got the setgid bit: %v", fi.Mode())
	}
	if op, _, _ := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/a.tar", Dest: "shop/app", Overwrite: true}); op.State != helperapi.OpSucceeded {
		t.Fatalf("extract with overwrite: %+v", op)
	}
	if e.read("/srv/www/shop/app/old.txt") != "new" {
		t.Error("overwrite did not replace the file")
	}
	if u, _ := fileOwnerOf(t, filepath.Join(dest, "old.txt")); u != 1001 {
		t.Errorf("a replaced file belongs to %d", u)
	}
	for _, n := range lsDir(dest) {
		if strings.HasPrefix(n, ".cairn-unpack-") {
			t.Errorf("a temporary file was left: %s", n)
		}
	}
}

// failAfter reads n bytes, then fails.
type failAfter struct{ n int }

func (f *failAfter) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("the archive breaks off")
	}
	k := min(len(p), f.n)
	clear(p[:k])
	f.n -= k
	return k, nil
}

// A file that fails to be written over leaves the one that was there.
func TestFailedOverwriteKeepsTheFile(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/shop/app/f.txt", "was there")
	root, err := os.OpenRoot(filepath.Join(e.f.Root, "/srv/www/shop/app"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	x := &extractor{ctx: t.Context(), rootID: "sites", root: root, dest: "shop/app", overwrite: true, b: &batch{log: io.Discard},
		budget: 1 << 30, files: map[string]bool{}}
	if err := x.file("f.txt", 0o644, &failAfter{n: 4096}); err == nil {
		t.Fatal("no error from a broken entry")
	}
	if e.read("/srv/www/shop/app/f.txt") != "was there" {
		t.Error("the file was lost or changed")
	}
	if names := lsDir(filepath.Join(e.f.Root, "/srv/www/shop/app")); len(names) != 1 {
		t.Errorf("left behind: %q", names)
	}
	if err := x.file("f.txt", 0o640, strings.NewReader("new")); err != nil || e.read("/srv/www/shop/app/f.txt") != "new" {
		t.Fatalf("overwrite: %v", err)
	}
}

// A restore from a damaged backup leaves the destination as it was (no
// file replaced, none lost); the same backup whole restores as usual.
func TestDamagedRestoreLeavesTheDestination(t *testing.T) {
	e, _, _ := newBackupEnv(t)
	big := func() string { b := make([]byte, 3<<20); _, _ = rand.Read(b); return string(b) }
	a, z := big(), big()
	e.put("/srv/www/shop/app/data/a.bin", a)
	e.put("/srv/www/shop/app/data/z.bin", z)
	plan := helperapi.BackupPlan{ID: "p9400000000000aa", Retention: helperapi.DefaultRetention,
		Targets: []helperapi.BackupTarget{{Kind: "directory", Root: "sites", Path: "shop/app/data"}}}
	if _, err := call[helperapi.Empty](t, e.r, helperapi.ActionBackupPlanApply, plan); err != nil {
		t.Fatal(err)
	}
	if op, _, log := e.run(helperapi.ActionBackupRun, helperapi.PlanRef{Plan: plan.ID}); op.State != helperapi.OpSucceeded {
		t.Fatalf("run: %+v\n%s", op, log)
	}
	runs, _ := call[helperapi.BackupRuns](t, e.r, helperapi.ActionBackupRuns, helperapi.PlanRef{Plan: plan.ID})
	name := runs.Runs[0].Files[0]
	bp := filepath.Join(planDirOf(e, plan.ID), name)
	whole, err := os.ReadFile(bp)
	if err != nil {
		t.Fatal(err)
	}
	// The live files changed since; then the backup gets damaged (cut at 75%).
	e.put("/srv/www/shop/app/data/a.bin", "CURRENT-A")
	e.put("/srv/www/shop/app/data/z.bin", "CURRENT-Z")
	if err := os.Truncate(bp, int64(len(whole))*3/4); err != nil {
		t.Fatal(err)
	}
	req := helperapi.BackupRestoreRequest{Plan: plan.ID, Name: name, Overwrite: true, Dest: helperapi.FileRef{Root: "sites", Path: "shop/app/data"}}
	op, _, log := e.run(helperapi.ActionBackupRestore, req)
	if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeFileBadArchive {
		t.Errorf("restore of the damaged backup: %+v\n%s", op, log)
	}
	for f, want := range map[string]string{"a.bin": "CURRENT-A", "z.bin": "CURRENT-Z"} {
		if got := e.read("/srv/www/shop/app/data/" + f); got != want {
			t.Errorf("%s after a failed restore: %d bytes, not what was there", f, len(got))
		}
	}
	if names := lsDir(filepath.Join(e.f.Root, "/srv/www/shop/app/data")); len(names) != 2 {
		t.Errorf("left behind: %q", names)
	}
	// Whole again: restored over the live files.
	if err := os.WriteFile(bp, whole, 0o600); err != nil {
		t.Fatal(err)
	}
	if op, _, log := e.run(helperapi.ActionBackupRestore, req); op.State != helperapi.OpSucceeded ||
		e.read("/srv/www/shop/app/data/a.bin") != a || e.read("/srv/www/shop/app/data/z.bin") != z {
		t.Fatalf("restore of the whole backup: %+v\n%s", op, log)
	}
	// Too big for the space there is: refused before anything is written.
	e.put("/srv/www/shop/app/data/a.bin", "CURRENT-A")
	old := spaceOf
	t.Cleanup(func() { spaceOf = old })
	spaceOf = func(*os.File) int64 { return 4 << 20 }
	if op, _, _ := e.run(helperapi.ActionBackupRestore, req); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeFileNoSpace ||
		e.read("/srv/www/shop/app/data/a.bin") != "CURRENT-A" {
		t.Errorf("restore without the space: %+v", op)
	}
}

// After the clock went back (backups named in the future are there), the
// backup a run just made is kept, and the future ones are left alone,
// here and in the bucket.
func TestRetentionAfterTheClockWentBack(t *testing.T) {
	e, b, store := newBackupEnv(t)
	now := time.Date(2026, 11, 23, 15, 7, 20, 0, time.UTC)
	b.clock = func() time.Time { return now }
	e.put("/srv/www/shop/app/index.php", "x")
	plan := helperapi.BackupPlan{ID: "p9200000000000aa", Storage: s3, KeepLocal: true, Retention: helperapi.Retention{Last: 2},
		Targets: []helperapi.BackupTarget{{Kind: "site", Site: "shop"}}}
	if _, err := call[helperapi.Empty](t, e.r, helperapi.ActionBackupPlanApply, plan); err != nil {
		t.Fatal(err)
	}
	// What two runs left while the clock was a day ahead, and an old one.
	_ = os.MkdirAll(planDirOf(e, plan.ID), 0o700)
	var future []string
	for _, d := range []int{1, 2} {
		n := backupName(plan.Targets[0], now.Add(time.Duration(d)*24*time.Hour), ".tar.zst", false)
		future = append(future, n)
		_ = os.WriteFile(filepath.Join(planDirOf(e, plan.ID), n), []byte("future"), 0o600)
		store.objects[plan.ID+"/"+n] = []byte("future")
	}
	past := func(d int) string {
		return backupName(plan.Targets[0], now.Add(-time.Duration(d)*24*time.Hour), ".tar.zst", false)
	}
	for _, d := range []int{1, 2} {
		_ = os.WriteFile(filepath.Join(planDirOf(e, plan.ID), past(d)), []byte("past"), 0o600)
		store.objects[plan.ID+"/"+past(d)] = []byte("past")
	}
	op, _, log := e.run(helperapi.ActionBackupRun, helperapi.PlanRef{Plan: plan.ID})
	runs, _ := call[helperapi.BackupRuns](t, e.r, helperapi.ActionBackupRuns, helperapi.PlanRef{Plan: plan.ID})
	r := runs.Runs[0]
	if op.State != helperapi.OpSucceeded || !r.OK || len(r.Files) != 1 || r.Started != now.Unix() {
		t.Fatalf("run: %+v %+v\n%s", op, r, log)
	}
	if _, err := os.Stat(filepath.Join(planDirOf(e, plan.ID), r.Files[0])); err != nil {
		t.Errorf("the backup just made (%s) was removed by the same run\n%s", r.Files[0], log)
	}
	if _, ok := store.objects[plan.ID+"/"+r.Files[0]]; !ok {
		t.Errorf("the bucket's copy of the backup just made was removed\n%s", log)
	}
	for _, n := range future {
		if !e.exists("/var/lib/cairn/backups/plans/"+plan.ID+"/"+n) || store.objects[plan.ID+"/"+n] == nil {
			t.Errorf("a backup dated in the future was removed: %s", n)
		}
		if !strings.Contains(log, n+" is dated after now") {
			t.Errorf("no warning about %s\n%s", n, log)
		}
	}
	// Last 2 of the rest: the new one and yesterday's.
	if !e.exists("/var/lib/cairn/backups/plans/"+plan.ID+"/"+past(1)) || e.exists("/var/lib/cairn/backups/plans/"+plan.ID+"/"+past(2)) {
		t.Errorf("local retention: %q", lsDir(planDirOf(e, plan.ID)))
	}
	if store.objects[plan.ID+"/"+past(1)] == nil || store.objects[plan.ID+"/"+past(2)] != nil {
		t.Errorf("bucket retention\n%s", log)
	}
	if r.Deleted != 2 {
		t.Errorf("deleted %d, want 2", r.Deleted)
	}
}

// The extractor keeps a directory's setgid bit only for restores.
func TestSetgidOnlyForRestores(t *testing.T) {
	e := newFilesEnv(t)
	root, err := os.OpenRoot(filepath.Join(e.f.Root, "/srv/www/shop/app"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, keep := range []bool{false, true} {
		x := &extractor{ctx: t.Context(), rootID: "sites", root: root, dest: "shop/app", b: &batch{log: io.Discard},
			budget: 1 << 30, files: map[string]bool{}, uid: os.Getuid(), gid: os.Getgid(), restore: keep}
		name := map[bool]string{false: "plain", true: "kept"}[keep]
		if err := x.dir(name, 0o770|fs.ModeSetgid); err != nil {
			t.Fatal(err)
		}
		fi, _ := root.Lstat(name)
		if got := fi.Mode()&fs.ModeSetgid != 0; got != keep {
			t.Errorf("%s: %v", name, fi.Mode())
		}
	}
}
