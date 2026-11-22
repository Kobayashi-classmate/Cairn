// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

func zstdOf(t *testing.T, data string) string {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(zw, data)
	_ = zw.Close()
	return buf.String()
}

func TestRestoreOfDatabaseNames(t *testing.T) {
	e, b, _ := newBackupEnv(t)
	plan := helperapi.BackupPlan{ID: "p2123456789abcde", Retention: helperapi.DefaultRetention,
		Targets: []helperapi.BackupTarget{{Kind: "directory", Root: "sites", Path: "shop/app"}}}
	if _, err := call[helperapi.Empty](t, e.r, helperapi.ActionBackupPlanApply, plan); err != nil {
		t.Fatal(err)
	}
	// Only a name that names a database goes without a destination.
	bogus := "db-foo_20261116-030000.tar.zst"
	if err := (helperapi.BackupRestoreRequest{Plan: plan.ID, Name: bogus}).Validate(); err == nil {
		t.Fatal("a db- name naming no database needs a destination")
	}
	if err := (helperapi.BackupRestoreRequest{Plan: plan.ID, Name: "db-mysql-shop_20261116-030000.sql.zst"}).Validate(); err != nil {
		t.Fatalf("a database backup: %v", err)
	}
	// And the helper never unpacks such a name as files (into "/" for an
	// empty destination, before).
	e.put("/var/lib/cairn/backups/plans/"+plan.ID+"/"+bogus, zstdOf(t, tarOf(t, tarEntry{&tar.Header{Name: "planted.txt", Typeflag: tar.TypeReg}, "x"})))
	_, err := b.restoreOp(context.Background(), helperapi.Caller{}, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: bogus}, io.Discard)
	if errCode(err) != helperapi.CodeBackupNotFound || e.exists("/planted.txt") {
		t.Fatalf("restore of %s: %v", bogus, err)
	}
	// A backup that is there, restored into a root that is not: refused
	// by the root check itself.
	site := "site-x_20261116-030000.tar.zst"
	e.put("/var/lib/cairn/backups/plans/"+plan.ID+"/"+site, zstdOf(t, tarOf(t, tarEntry{&tar.Header{Name: "planted.txt", Typeflag: tar.TypeReg}, "x"})))
	if _, err := b.restoreOp(context.Background(), helperapi.Caller{}, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: site,
		Dest: helperapi.FileRef{Root: "nowhere"}}, io.Discard); errCode(err) != helperapi.CodeFileRootUnavailable || e.exists("/planted.txt") {
		t.Fatalf("an unknown destination root: %v", err)
	}

	// A database backup becomes a dump, within the space there is.
	name := "db-mysql-shop_20261116-030000.sql.zst"
	e.put("/var/lib/cairn/backups/plans/"+plan.ID+"/"+name, zstdOf(t, "CREATE TABLE t (id int);\n"))
	oldSpace := spaceOf
	t.Cleanup(func() { spaceOf = oldSpace })
	spaceOf = func(*os.File) int64 { return 10 }
	if op, _, log := e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: name}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeFileNoSpace {
		t.Fatalf("restore beyond the free space: %+v\n%s", op, log)
	}
	spaceOf = oldSpace
	if op, _, log := e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: name}); op.State != helperapi.OpSucceeded {
		t.Fatalf("restore: %+v\n%s", op, log)
	}
}

func TestFetchIgnoresPlantedFiles(t *testing.T) {
	e, _, store := newBackupEnv(t)
	plan := helperapi.BackupPlan{ID: "p3123456789abcde", Storage: s3, Retention: helperapi.DefaultRetention,
		Targets: []helperapi.BackupTarget{{Kind: "directory", Root: "sites", Path: "shop/app"}}}
	if _, err := call[helperapi.Empty](t, e.r, helperapi.ActionBackupPlanApply, plan); err != nil {
		t.Fatal(err)
	}
	name := "dir-x_20261116-030000.tar.zst"
	store.objects[plan.ID+"/"+name] = []byte("remote copy")
	e.put("/etc/victim", "keep")
	dir := filepath.Join(e.f.Root, "/var/lib/cairn/backups/plans", plan.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The panel owns the directory: what it put at the temporary name is
	// not written into.
	if err := os.Link(filepath.Join(e.f.Root, "/etc/victim"), filepath.Join(dir, ".partial-"+name)); err != nil {
		t.Fatal(err)
	}
	if op, _, log := e.run(helperapi.ActionBackupFetch, helperapi.BackupFilesRequest{Plan: plan.ID, Names: []string{name}}); op.State != helperapi.OpSucceeded {
		t.Fatalf("fetch: %+v\n%s", op, log)
	}
	if e.read("/etc/victim") != "keep" || e.read("/var/lib/cairn/backups/plans/"+plan.ID+"/"+name) != "remote copy" {
		t.Fatalf("fetch wrote through a planted link: %q", e.read("/etc/victim"))
	}
	// Nor is a local copy with another name elsewhere read.
	other := "dir-y_20261116-030000.tar.zst"
	if err := os.Link(filepath.Join(e.f.Root, "/etc/victim"), filepath.Join(dir, other)); err != nil {
		t.Fatal(err)
	}
	if op, _, _ := e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: other, Dest: helperapi.FileRef{Root: "sites", Path: "shop"}}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeBackupNotFound {
		t.Fatalf("restore of a hard link: %+v", op)
	}
}
