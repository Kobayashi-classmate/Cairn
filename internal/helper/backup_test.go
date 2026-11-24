// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/netx"
)

// memStore is a bucket in memory.
type memStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	failPut bool
}

func (m *memStore) put(_ context.Context, key string, r io.Reader, _ int64) error {
	if m.failPut {
		return errors.New("access denied")
	}
	data, err := io.ReadAll(r)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
	return err
}

func (m *memStore) stat(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return 0, errors.New("no such key")
	}
	return int64(len(data)), nil
}

func (m *memStore) list(_ context.Context, dir string) ([]objectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []objectInfo
	for k, v := range m.objects {
		if name, ok := strings.CutPrefix(k, dir+"/"); ok && !strings.Contains(name, "/") {
			out = append(out, objectInfo{name: name, size: int64(len(v))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func (m *memStore) remove(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *memStore) get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, errors.New("no such key")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memStore) presign(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://bucket.example/" + key + "?X-Amz-Signature=x", nil
}

func newBackupEnv(t *testing.T) (*filesEnv, *Backups, *memStore) {
	e := newFilesEnv(t)
	b := &Backups{Files: e.f, DBs: &DBs{Root: e.f.Root, DataDir: e.f.DataDir, PanelUser: "cairn"}, Log: e.f.Log}
	RegisterBackups(e.r, e.o, b)
	store := &memStore{objects: map[string][]byte{}}
	old := newStore
	t.Cleanup(func() { newStore = old })
	newStore = func(helperapi.S3Storage) (objectStore, error) { return store, nil }
	return e, b, store
}

var s3 = &helperapi.S3Storage{Endpoint: "s3.example.com", Bucket: "backups", AccessKey: "AK", SecretKey: "SK"}

func TestBackupRunUploadAndRestore(t *testing.T) {
	e, b, store := newBackupEnv(t)
	e.put("/srv/www/shop/app/index.php", "<?php echo 1;")
	e.put("/srv/www/shop/tmp/sess_1", "session")
	e.put("/var/lib/cairn-helper/sites/shop.json", `{"spec":{"code":"shop"}}`)
	e.put("/srv/www/shop/app/data/a.txt", "data")
	plan := helperapi.BackupPlan{ID: "p0123456789abcde", Storage: s3, KeepLocal: true, Retention: helperapi.DefaultRetention,
		Targets: []helperapi.BackupTarget{{Kind: "site", Site: "shop"}, {Kind: "directory", Root: "sites", Path: "shop/app/data"}}}
	if _, err := call[helperapi.Empty](t, e.r, helperapi.ActionBackupPlanApply, plan); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(e.f.StateDir, "backup/plans/p0123456789abcde.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("the plan (with credentials) must be root-only: %v", fi.Mode())
	}
	op, _, log := e.run(helperapi.ActionBackupRun, helperapi.PlanRef{Plan: plan.ID})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("run: %+v\n%s", op, log)
	}
	l, err := call[helperapi.BackupList](t, e.r, helperapi.ActionBackupList, helperapi.PlanRef{Plan: plan.ID})
	if err != nil || len(l.Files) != 2 {
		t.Fatalf("list %+v %v", l, err)
	}
	var site string
	for _, f := range l.Files {
		if !f.Local || !f.Remote || f.Encrypted {
			t.Fatalf("file %+v", f)
		}
		if f.Target == "site-shop" {
			site = f.Name
		}
	}
	if !strings.HasSuffix(site, ".tar.zst") || len(store.objects) != 2 {
		t.Fatalf("site backup %q, bucket %v", site, len(store.objects))
	}
	runs, _ := call[helperapi.BackupRuns](t, e.r, helperapi.ActionBackupRuns, helperapi.PlanRef{Plan: plan.ID})
	if len(runs.Runs) != 1 || !runs.Runs[0].OK || len(runs.Runs[0].Files) != 2 {
		t.Fatalf("runs %+v", runs)
	}

	// Restore into a new directory: tmp/ was left out, settings came along.
	if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileMkdir, helperapi.FileNewRequest{Root: "sites", Name: "copy"}); err != nil {
		t.Fatal(err)
	}
	op, _, log = e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: site, Dest: helperapi.FileRef{Root: "sites", Path: "copy"}})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("restore: %+v\n%s", op, log)
	}
	if e.read("/srv/www/copy/app/index.php") != "<?php echo 1;" || e.exists("/srv/www/copy/tmp/sess_1") || !e.exists("/srv/www/copy/.cairn/site.json") {
		t.Fatal("restored content")
	}

	// A copy only in the bucket is fetched first.
	if err := os.Remove(filepath.Join(e.f.Root, "/var/lib/cairn/backups/plans", plan.ID, site)); err != nil {
		t.Fatal(err)
	}
	if op, _, _ := e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: site, Overwrite: true,
		Dest: helperapi.FileRef{Root: "sites", Path: "copy"}}); op.State != helperapi.OpSucceeded {
		t.Fatalf("restore from the bucket: %+v", op)
	}
	p, err := call[helperapi.PresignResponse](t, e.r, helperapi.ActionBackupPresign, helperapi.BackupFilesRequest{Plan: plan.ID, Names: []string{site}})
	if err != nil || !strings.Contains(p.URL, plan.ID+"/"+site) {
		t.Fatalf("presign %+v %v", p, err)
	}
	if op, _, _ := e.run(helperapi.ActionBackupDelete, helperapi.BackupFilesRequest{Plan: plan.ID, Names: []string{site}, Remote: true}); op.State != helperapi.OpSucceeded {
		t.Fatalf("delete: %+v", op)
	}
	if _, ok := store.objects[plan.ID+"/"+site]; ok || e.exists("/var/lib/cairn/backups/plans/"+plan.ID+"/"+site) {
		t.Fatal("deleted backup still there")
	}

	// A failing bucket fails the run but keeps the local copies.
	store.failPut = true
	if op, _, _ := e.run(helperapi.ActionBackupRun, helperapi.PlanRef{Plan: plan.ID}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeBackupStorage {
		t.Fatalf("failing bucket: %+v", op)
	}
	runs, _ = call[helperapi.BackupRuns](t, e.r, helperapi.ActionBackupRuns, helperapi.PlanRef{Plan: plan.ID})
	if runs.Runs[0].OK || runs.Runs[0].Error == nil || len(runs.Runs[0].Files) != 2 {
		t.Fatalf("failed run %+v", runs.Runs[0])
	}
	b.Files.Log.Info("done")
}

// A directory target, and a restore's destination, inside one site never
// reach into another site through a link the site's owner made.
func TestDirectoryBackupsStayInTheirSite(t *testing.T) {
	e, _, _ := newBackupEnv(t)
	e.put("/srv/www/bar/app/config.php", "DB_PASSWORD=bar-secret")
	e.put("/srv/www/bar/app/uploads/keep.txt", "bar")
	e.put("/srv/www/foo/app/index.php", "foo")
	e.put("/srv/www/foo/app/storage/own.txt", "foo's own")
	if err := os.Symlink("../../bar/app", filepath.Join(e.f.Root, "/srv/www/foo/app/data")); err != nil {
		t.Fatal(err)
	}
	// A link that stays inside the site is the site's own business.
	if err := os.Symlink("storage", filepath.Join(e.f.Root, "/srv/www/foo/app/current")); err != nil {
		t.Fatal(err)
	}
	plan := helperapi.BackupPlan{ID: "p4123456789abcde", Retention: helperapi.DefaultRetention,
		Targets: []helperapi.BackupTarget{{Kind: "directory", Root: "sites", Path: "foo/app/data"}, {Kind: "directory", Root: "sites", Path: "foo/app/current"}}}
	if _, err := call[helperapi.Empty](t, e.r, helperapi.ActionBackupPlanApply, plan); err != nil {
		t.Fatal(err)
	}
	// The target behind the link fails; the other one is backed up.
	op, _, log := e.run(helperapi.ActionBackupRun, helperapi.PlanRef{Plan: plan.ID})
	l, err := call[helperapi.BackupList](t, e.r, helperapi.ActionBackupList, helperapi.PlanRef{Plan: plan.ID})
	if op.State != helperapi.OpFailed || err != nil || len(l.Files) != 1 || l.Files[0].Target != "dir-sites-foo-app-current" {
		t.Fatalf("backup through a link to another site: %+v %+v %v\n%s", op, l.Files, err, log)
	}
	own := l.Files[0].Name
	for _, dir := range []string{"check", "foo/restored"} {
		if err := os.MkdirAll(filepath.Join(e.f.Root, "/srv/www", dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if op, _, log := e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: own,
		Dest: helperapi.FileRef{Root: "sites", Path: "check"}}); op.State != helperapi.OpSucceeded || e.read("/srv/www/check/own.txt") != "foo's own" {
		t.Fatalf("restore of the site's own directory: %+v\n%s", op, log)
	}
	// Restoring into the site, through its own link too.
	if err := os.Symlink("../restored", filepath.Join(e.f.Root, "/srv/www/foo/app/back")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(e.f.Root, "/srv/www/foo/restored/into"), 0o750); err != nil {
		t.Fatal(err)
	}
	if op, _, log := e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: own,
		Dest: helperapi.FileRef{Root: "sites", Path: "foo/app/back/into"}}); op.State != helperapi.OpSucceeded || e.read("/srv/www/foo/restored/into/own.txt") != "foo's own" {
		t.Fatalf("restore through a link inside the site: %+v\n%s", op, log)
	}
	// Restoring into the other site through the link is refused.
	if op, _, log := e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: own,
		Dest: helperapi.FileRef{Root: "sites", Path: "foo/app/data/uploads"}}); op.State != helperapi.OpFailed || e.exists("/srv/www/bar/app/uploads/own.txt") {
		t.Fatalf("restore through a link to another site: %+v\n%s", op, log)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.f.Root, "/srv/www/bar/app/uploads")); len(entries) != 1 {
		t.Fatalf("the other site's directory now holds %v", entries)
	}
}

func TestEncryptedBackups(t *testing.T) {
	e, _, _ := newBackupEnv(t)
	e.put("/srv/www/shop/app/secret.txt", "top secret")
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	plan := helperapi.BackupPlan{ID: "p1123456789abcde", Retention: helperapi.Retention{Last: 3}, Recipients: []string{id.Recipient().String()},
		Targets: []helperapi.BackupTarget{{Kind: "directory", Root: "sites", Path: "shop/app"}}}
	if _, err := call[helperapi.Empty](t, e.r, helperapi.ActionBackupPlanApply, plan); err != nil {
		t.Fatal(err)
	}
	if op, _, log := e.run(helperapi.ActionBackupRun, helperapi.PlanRef{Plan: plan.ID}); op.State != helperapi.OpSucceeded {
		t.Fatalf("run %+v\n%s", op, log)
	}
	l, _ := call[helperapi.BackupList](t, e.r, helperapi.ActionBackupList, helperapi.PlanRef{Plan: plan.ID})
	name := l.Files[0].Name
	if !l.Files[0].Encrypted || !strings.HasSuffix(name, ".tar.zst.age") {
		t.Fatalf("file %+v", l.Files[0])
	}
	if data := e.read("/var/lib/cairn/backups/plans/" + plan.ID + "/" + name); strings.Contains(data, "top secret") || !strings.HasPrefix(data, "age-encryption.org") {
		t.Fatal("the backup is not encrypted")
	}
	dest := helperapi.FileRef{Root: "sites", Path: "shop"}
	if op, _, _ := e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: name, Dest: dest}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeBackupIdentity {
		t.Fatalf("restore without the key: %+v", op)
	}
	other, _ := age.GenerateX25519Identity()
	if op, _, _ := e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: name, Dest: dest, Identity: other.String()}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeBackupBadIdentity {
		t.Fatalf("restore with another key: %+v", op)
	}
	if _, err := call[helperapi.FileEntry](t, e.r, helperapi.ActionFileMkdir, helperapi.FileNewRequest{Root: "sites", Dir: "shop", Name: "restored"}); err != nil {
		t.Fatal(err)
	}
	dest.Path = "shop/restored"
	if op, _, log := e.run(helperapi.ActionBackupRestore, helperapi.BackupRestoreRequest{Plan: plan.ID, Name: name, Dest: dest, Identity: id.String()}); op.State != helperapi.OpSucceeded {
		t.Fatalf("restore: %+v\n%s", op, log)
	}
	if e.read("/srv/www/shop/restored/secret.txt") != "top secret" {
		t.Fatal("decrypted content")
	}
}

func TestRetention(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 11, 16, 3, 0, 0, 0, time.Local).AddDate(0, 0, -d) }
	var names []string
	for d := 0; d < 400; d++ {
		names = append(names, backupName(helperapi.BackupTarget{Kind: "site", Site: "shop"}, day(d), ".tar.zst", false))
	}
	// Two backups on the newest day: only one counts for that day.
	names = append(names, backupName(helperapi.BackupTarget{Kind: "site", Site: "shop"}, day(0).Add(-time.Hour), ".tar.zst", false))
	gone := doomed(names, helperapi.Retention{Last: 1, Daily: 7, Weekly: 4, Monthly: 6})
	kept := len(names) - len(gone)
	// 7 days, then the newest of up to 4 weeks and 6 months; buckets
	// overlap with the days, so between 7 and 17 remain.
	if kept < 10 || kept > 17 {
		t.Fatalf("kept %d of %d", kept, len(names))
	}
	if slicesContain(gone, names[0]) || !slicesContain(gone, names[len(names)-1]) {
		t.Fatal("the newest backup must stay; the second one that day goes")
	}
	if g := doomed(names[:5], helperapi.Retention{Last: 3}); len(g) != 2 {
		t.Fatalf("last 3 of 5: %v", g)
	}
	if key, at, ok := parseBackupName("db-postgresql-17-shop_20261116-030000.dump.age"); !ok || key != "db-postgresql-17-shop" || at.Hour() != 3 {
		t.Fatal("parse")
	}
	if engine, db, ok := dbOfBackup("db-postgresql-17-my_shop"); !ok || engine != "postgresql-17" || db != "my_shop" {
		t.Fatalf("db of backup: %s %s", engine, db)
	}
}

func slicesContain(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestStorageEndpoints(t *testing.T) {
	for ep, ok := range map[string]bool{"s3:8080": true, "localhost:9000": true, "s3.amazonaws.com": true, "10.0.0.5:9000": true,
		"[::1]:9000": true, "oss-cn-hangzhou.aliyuncs.com": true, "bad host": false, "s3:99999": false, "https://s3.example.com": false} {
		s := helperapi.S3Storage{Endpoint: ep, Bucket: "backups", AccessKey: "a"}
		if err := s.Validate(); (err == nil) != ok {
			t.Errorf("%s: %v", ep, err)
		}
	}
}

func TestStorageRefusesLocalAddresses(t *testing.T) {
	// The endpoint's name may resolve to this host when the helper
	// connects, whatever it did when the panel checked it.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	st, err := newMinioStore(helperapi.S3Storage{Endpoint: strings.TrimPrefix(srv.URL, "http://"), Insecure: true, PathStyle: true,
		Bucket: "backups", AccessKey: "a", SecretKey: "b", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = st.stat(ctx, "x")
	var denied *netx.DeniedError
	if !errors.As(err, &denied) || hits != 0 {
		t.Fatalf("loopback storage reached: %v (%d requests)", err, hits)
	}
}
