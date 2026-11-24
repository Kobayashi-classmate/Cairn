// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

func TestDatabaseOfBackup(t *testing.T) {
	for name, want := range map[string]string{
		"db-mysql-my_shop_20261116-030000.sql.zst.age":  "mysql/my_shop",
		"db-postgresql-17-my_shop_20261116-030000.dump": "postgresql-17/my_shop",
		"db-mysql-my_shop":                  "mysql/my_shop",
		"site-shop_20261116-030000.tar.zst": "",
	} {
		engine, db, ok := helperapi.DatabaseOfBackup(name)
		if got := engine + "/" + db; ok && got != want || !ok && want != "" {
			t.Errorf("%s: %s %v", name, got, ok)
		}
	}
}

// planDirOf is a plan's local directory in the test's file system.
func planDirOf(e *filesEnv, plan string) string {
	return filepath.Join(e.f.Root, "/var/lib/cairn/backups/plans", plan)
}

func lsDir(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, x := range ents {
		out = append(out, x.Name())
	}
	return out
}

// Every path a directory target may have gives a key that makes valid
// backup names, and no two paths share one.
func TestBackupKeysOfAnyPath(t *testing.T) {
	long := strings.Repeat("L", 255)
	paths := []string{"", "a", "a/b", "a-b", "a.b", "a_b", "a b", "a~b", "a/.b", "a-2eb", "a.2eb",
		"shop/app/my files", "shop/app/\u4e0a\u4f20", "shop/app/\u4e0a\u4f21",
		"shop/app/" + long, "shop/app/" + long[1:] + "x", "shop/app/" + long + "/" + long, strings.Repeat("\u4e0a/", 300) + "x",
		strings.Repeat("a", 133) + ".b", strings.Repeat("a", 134) + ".b", strings.Repeat("a", 135) + ".b"}
	seen := map[string]string{}
	for _, root := range []string{helperapi.RootSites, helperapi.RootSystem} {
		for _, p := range paths {
			tgt := helperapi.BackupTarget{Kind: helperapi.BackupDirectory, Root: root, Path: p}
			key := tgt.Key()
			for _, suffix := range []string{".tar.zst", ".tar.zst.age"} {
				name := key + "_20261123-150720" + suffix
				if !helperapi.ValidBackupName(name) {
					t.Errorf("%s:%q: %q is no valid backup name", root, p, name)
				}
				if k, _, ok := parseBackupName(name); !ok || k != key {
					t.Errorf("%s:%q: %q reads as key %q", root, p, name, k)
				}
			}
			if prev, ok := seen[key]; ok {
				t.Errorf("%s:%q and %s share the key %q", root, p, prev, key)
			}
			seen[key] = root + ":" + p
			plan := helperapi.BackupPlan{ID: "p0123456789abcde", Targets: []helperapi.BackupTarget{tgt}, Retention: helperapi.DefaultRetention}
			if err := plan.Validate(); err != nil {
				t.Errorf("%s:%q: plan refused: %v", root, p, err)
			}
		}
	}
	// The keys of plain paths read as before.
	for p, want := range map[string]string{"shop/app/data": "dir-sites-shop-app-data", "": "dir-sites", "a_b/c": "dir-sites-a_b-c"} {
		if got := (helperapi.BackupTarget{Kind: helperapi.BackupDirectory, Root: helperapi.RootSites, Path: p}).Key(); got != want {
			t.Errorf("%q: key %q, want %q", p, got, want)
		}
	}
}

// A directory target whose path holds a space, non-ASCII or a long name
// produces a backup that can be listed, pruned and restored.
func TestUnusualDirectoryPaths(t *testing.T) {
	long := strings.Repeat("L", 190)
	for i, dir := range []string{"shop/app/my files", "shop/app/\u4e0a\u4f20", "shop/app/" + long} {
		e, _, _ := newBackupEnv(t)
		e.put("/srv/www/"+dir+"/f.txt", "payload")
		id := "p9" + strings.Repeat("0", 13) + string(rune('a'+i))
		tgt := helperapi.BackupTarget{Kind: "directory", Root: "sites", Path: dir}
		plan := helperapi.BackupPlan{ID: id, Retention: helperapi.Retention{Last: 1},
			Targets: []helperapi.BackupTarget{tgt}}
		if _, err := call[helperapi.Empty](t, e.r, helperapi.ActionBackupPlanApply, plan); err != nil {
			t.Fatalf("%q: apply: %v", dir, err)
		}
		// An older backup of the same target: Last=1 must prune it.
		old := tgt.Key() + "_20200101-000000.tar.zst"
		_ = os.MkdirAll(planDirOf(e, id), 0o700)
		_ = os.WriteFile(filepath.Join(planDirOf(e, id), old), []byte("old"), 0o600)
		op, _, log := e.run(helperapi.ActionBackupRun, helperapi.PlanRef{Plan: id})
		t.Logf("%q: key=%q (%d bytes) run=%s files on disk=%q", dir, tgt.Key(), len(tgt.Key()), op.State, lsDir(planDirOf(e, id)))
		if op.State != helperapi.OpSucceeded {
			t.Errorf("%q: run failed: %+v\n%s", dir, op, log)
			continue
		}
		runs, _ := call[helperapi.BackupRuns](t, e.r, helperapi.ActionBackupRuns, helperapi.PlanRef{Plan: id})
		l, _ := call[helperapi.BackupList](t, e.r, helperapi.ActionBackupList, helperapi.PlanRef{Plan: id})
		if len(l.Files) != 1 {
			t.Errorf("%q: backup.list shows %d files, want 1", dir, len(l.Files))
		}
		if _, err := os.Stat(filepath.Join(planDirOf(e, id), old)); err == nil {
			t.Errorf("%q: retention (last 1) left the older backup %s", dir, old)
		}
		name := runs.Runs[0].Files[0]
		req := helperapi.BackupRestoreRequest{Plan: id, Name: name, Dest: helperapi.FileRef{Root: "sites", Path: "shop"}}
		if err := req.Validate(); err != nil {
			t.Errorf("%q: backup.restore of %q refused by validation: %v", dir, name, err)
			continue
		}
		if err := os.MkdirAll(filepath.Join(e.f.Root, "/srv/www/check"), 0o750); err != nil {
			t.Fatal(err)
		}
		req.Dest.Path = "check"
		if op, _, log := e.run(helperapi.ActionBackupRestore, req); op.State != helperapi.OpSucceeded || e.read("/srv/www/check/f.txt") != "payload" {
			t.Errorf("%q: restore: %+v\n%s", dir, op, log)
		}
	}
}

// Backups named by the keys of before (a "-" or "." in the path read as
// "/") are still listed, restored and pruned: the local ones are renamed,
// the bucket's are found by their old names.
func TestBackupsWithOldKeys(t *testing.T) {
	e, _, store := newBackupEnv(t)
	e.put("/srv/www/shop/app/my-site.d/f.txt", "now")
	e.put("/srv/www/shop/app/my files/g.txt", "spaced")
	dotted := helperapi.BackupTarget{Kind: "directory", Root: "sites", Path: "shop/app/my-site.d"}
	spaced := helperapi.BackupTarget{Kind: "directory", Root: "sites", Path: "shop/app/my files"}
	plan := helperapi.BackupPlan{ID: "p8123456789abcde", Storage: s3, Retention: helperapi.Retention{Last: 2},
		Targets: []helperapi.BackupTarget{dotted, spaced}}
	if _, err := call[helperapi.Empty](t, e.r, helperapi.ActionBackupPlanApply, plan); err != nil {
		t.Fatal(err)
	}
	if legacyKey(dotted) != "dir-sites-shop-app-my-site-d" || dotted.Key() == legacyKey(dotted) {
		t.Fatalf("keys %q %q", legacyKey(dotted), dotted.Key())
	}
	// What an older version made: tar.zst archives of a single file each.
	archive := func(body string) []byte {
		return []byte(zstdOf(t, tarOf(t, tarEntry{&tar.Header{Name: "old.txt", Typeflag: tar.TypeReg}, body})))
	}
	dir := planDirOf(e, plan.ID)
	_ = os.MkdirAll(dir, 0o700)
	oldLocal := legacyKey(dotted) + "_20200101-000000.tar.zst"
	oldSpaced := legacyKey(spaced) + "_20200102-000000.tar.zst"
	for n, body := range map[string]string{oldLocal: "local dotted", oldSpaced: "local spaced"} {
		if err := os.WriteFile(filepath.Join(dir, n), archive(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldRemote := legacyKey(dotted) + "_20200103-000000.tar.zst"
	store.objects[plan.ID+"/"+oldRemote] = archive("remote dotted")
	store.objects[plan.ID+"/"+legacyKey(dotted)+"_20190101-000000.tar.zst"] = archive("remote oldest")

	l, err := call[helperapi.BackupList](t, e.r, helperapi.ActionBackupList, helperapi.PlanRef{Plan: plan.ID})
	if err != nil || len(l.Files) != 4 {
		t.Fatalf("list: %+v %v (on disk %q)", l.Files, err, lsDir(dir))
	}
	names := map[string]helperapi.BackupFile{}
	for _, f := range l.Files {
		names[f.Name] = f
		if f.Target != dotted.Key() && f.Target != spaced.Key() {
			t.Errorf("%s is listed for target %q", f.Name, f.Target)
		}
		if !helperapi.ValidBackupName(f.Name) {
			t.Errorf("listed under a name no action takes: %q", f.Name)
		}
	}
	newLocal := currentName(renamedKeys(plan), oldLocal)
	newSpaced := currentName(renamedKeys(plan), oldSpaced)
	newRemote := currentName(renamedKeys(plan), oldRemote)
	if !names[newLocal].Local || !names[newSpaced].Local || !names[newRemote].Remote || names[newRemote].Local {
		t.Fatalf("listed %+v", l.Files)
	}
	// Restored by the names listed: locally, and from the bucket by its
	// old name.
	for name, want := range map[string]string{newSpaced: "local spaced", newRemote: "remote dotted"} {
		if err := os.RemoveAll(filepath.Join(e.f.Root, "/srv/www/check")); err != nil {
			t.Fatal(err)
		}
		_ = os.MkdirAll(filepath.Join(e.f.Root, "/srv/www/check"), 0o750)
		if op, _, log := e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: name,
			Dest: helperapi.FileRef{Root: "sites", Path: "check"}}); op.State != helperapi.OpSucceeded || e.read("/srv/www/check/old.txt") != want {
			t.Fatalf("restore of %s: %+v\n%s", name, op, log)
		}
	}
	if p, err := call[helperapi.PresignResponse](t, e.r, helperapi.ActionBackupPresign, helperapi.BackupFilesRequest{Plan: plan.ID, Names: []string{newRemote}}); err != nil || !strings.Contains(p.URL, oldRemote) {
		t.Fatalf("presign of a copy under its old name: %+v %v", p, err)
	}
	// Retention counts them with the target's new backups (Last 2).
	if op, _, log := e.run(helperapi.ActionBackupRun, helperapi.PlanRef{Plan: plan.ID}); op.State != helperapi.OpSucceeded {
		t.Fatalf("run: %+v\n%s", op, log)
	}
	for _, n := range lsDir(dir) {
		if strings.HasPrefix(n, legacyKey(dotted)) || strings.HasPrefix(n, legacyKey(spaced)) {
			t.Errorf("a local backup kept its old name: %s", n)
		}
	}
	if !e.exists("/var/lib/cairn/backups/plans/" + plan.ID + "/" + newLocal) {
		t.Errorf("the newest old backup of the dotted target was pruned: %q", lsDir(dir))
	}
	if _, ok := store.objects[plan.ID+"/"+legacyKey(dotted)+"_20190101-000000.tar.zst"]; ok {
		t.Error("the bucket's oldest backup under the old key was not pruned")
	}
	if _, ok := store.objects[plan.ID+"/"+oldRemote]; !ok {
		t.Error("the bucket's newer old backup was pruned too")
	}
	// Deleting by the name listed reaches the old name in the bucket.
	if op, _, _ := e.run(helperapi.ActionBackupDelete, helperapi.BackupFilesRequest{Plan: plan.ID, Names: []string{newRemote}, Remote: true}); op.State != helperapi.OpSucceeded {
		t.Fatalf("delete: %+v", op)
	}
	if _, ok := store.objects[plan.ID+"/"+oldRemote]; ok {
		t.Error("delete left the old name in the bucket")
	}
}
