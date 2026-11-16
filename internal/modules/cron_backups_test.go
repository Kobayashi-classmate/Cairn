// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// published collects the events of a type.
func (e *env) published(typ string) func() []events.Event {
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	var got []events.Event
	e.bus.Subscribe(func(ev events.Event) {
		if ev.Type == typ {
			<-mu
			got = append(got, ev)
			mu <- struct{}{}
		}
	})
	return func() []events.Event {
		<-mu
		defer func() { mu <- struct{}{} }()
		return append([]events.Event(nil), got...)
	}
}

func TestCronModule(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.opsSucceed(helperapi.ActionCronApply, helperapi.ActionCronRun, helperapi.ActionCronRemove)
	task := map[string]any{"name": "Nightly cleanup", "type": "shell", "schedule": "daily", "user": "cw-shop", "script": "php artisan schedule:run", "enabled": true}
	admin.expect(admin.do("POST", "/api/v1/cron", map[string]any{"name": "x", "type": "shell", "schedule": "daily; rm -rf /", "user": "cw-shop", "script": "x"}), 400, "validation.invalid")
	admin.expect(admin.do("POST", "/api/v1/cron", map[string]any{"name": "x", "type": "backup", "schedule": "daily", "plan": "p0123456789abcde"}), 400, helperapi.CodeCronPlanNotFound)
	r := admin.runJob(admin.expect(admin.do("POST", "/api/v1/cron", task), 202, ""))
	spec := sentAs[helperapi.CronSpec](t, e, helperapi.ActionCronApply)
	if spec.User != "cw-shop" || spec.Script != "php artisan schedule:run" || !strings.HasPrefix(spec.ID, "t") {
		t.Fatalf("spec %+v (%v)", spec, r)
	}

	// Root tasks: administrators, with a second verification.
	root := map[string]any{"name": "Root job", "type": "shell", "schedule": "hourly", "user": "root", "script": "apt-get clean", "enabled": true}
	admin.expect(admin.do("POST", "/api/v1/cron", root), 403, "auth.step_up_required")
	admin.elevate("")
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/cron", root), 202, ""))
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "oper", "password": pw, "role": "operator"}), 201, "")
	op := e.client()
	op.expect(op.login("oper", pw), 200, "")
	op.elevate("")
	op.expect(op.do("POST", "/api/v1/cron", root), 403, "auth.forbidden")
	rootID := sentAs[helperapi.CronSpec](t, e, helperapi.ActionCronApply).ID
	op.expect(op.do("PUT", "/api/v1/cron/"+rootID, task), 403, "auth.forbidden")
	op.expect(op.do("DELETE", "/api/v1/cron/"+rootID, nil), 403, "auth.forbidden")

	// The list merges what systemd says.
	e.helper.respond(helperapi.ActionCronStatus, helperapi.CronStatus{Systemd: true, Tasks: map[string]helperapi.CronState{
		spec.ID: {NextRun: 1794873600, LastRun: 1794800000, Result: "failed"}}})
	list := admin.expect(admin.do("GET", "/api/v1/cron", nil), 200, "").body["tasks"].([]any)
	var found map[string]any
	for _, x := range list {
		if v := x.(map[string]any); v["id"] == spec.ID {
			found = v
		}
	}
	if found == nil || found["nextRun"] != float64(1794873600) || found["result"] != "failed" || found["state"] != "active" {
		t.Fatalf("list %v", list)
	}
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/cron/"+spec.ID+"/run", nil), 202, ""))
	e.helper.respond(helperapi.ActionCronRuns, helperapi.CronRuns{Runs: []helperapi.CronRun{{Invocation: strings.Repeat("ab", 16), Started: 1794800000, Result: "failed", ExitCode: 2}}})
	admin.expect(admin.do("GET", "/api/v1/cron/"+spec.ID+"/runs", nil), 200, "")
	e.helper.respond(helperapi.ActionCronLog, helperapi.CronLog{Lines: []string{"boom"}})
	admin.expect(admin.do("GET", "/api/v1/cron/"+spec.ID+"/runs/"+strings.Repeat("ab", 16)+"/log", nil), 200, "")
	admin.expect(admin.do("GET", "/api/v1/cron/"+spec.ID+"/runs/nope/log", nil), 400, "validation.invalid")

	// A failed run is reported once.
	got := e.published(events.CronFailed)
	e.cron.Check(context.Background())
	e.cron.Check(context.Background())
	if evs := got(); len(evs) != 1 || evs[0].Params["name"] != "Nightly cleanup" || evs[0].Params["exitCode"] != 2 {
		t.Fatalf("events %+v", evs)
	}

	e.helper.mu.Lock()
	e.helper.errs = map[string]error{helperapi.ActionCronSchedule: &helperapi.Error{Code: helperapi.CodeCronBadSchedule}}
	e.helper.mu.Unlock()
	admin.expect(admin.do("GET", "/api/v1/cron/schedule?expr=sometimes", nil), 400, helperapi.CodeCronBadSchedule)
	admin.runJob(admin.expect(admin.do("DELETE", "/api/v1/cron/"+spec.ID, nil), 202, ""))
}

func TestBackupsModule(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	storage := map[string]any{"name": "R2", "endpoint": "acct.r2.cloudflarestorage.com", "bucket": "panel-backups", "accessKey": "AK", "secretKey": "S3CRET"}
	admin.expect(admin.do("POST", "/api/v1/backups/storages", storage), 403, "auth.step_up_required")
	admin.elevate("")
	admin.expect(admin.do("POST", "/api/v1/backups/storages", map[string]any{"name": "x", "endpoint": "bad host!", "bucket": "b", "accessKey": "a", "secretKey": "s"}), 400, "validation.invalid")
	s := admin.expect(admin.do("POST", "/api/v1/backups/storages", storage), 201, "")
	sid := s.body["id"].(string)
	if strings.Contains(s.raw, "S3CRET") || s.body["secretSet"] != true {
		t.Fatalf("storage view leaks: %s", s.raw)
	}
	var sealed string
	_ = e.db.QueryRow(`SELECT secret FROM backup_storages WHERE id = ?`, sid).Scan(&sealed)
	if sealed == "" || strings.Contains(sealed, "S3CRET") {
		t.Fatal("secret stored in the clear")
	}
	admin.expect(admin.do("POST", "/api/v1/backups/storages/"+sid+"/test", nil), 204, "")
	if q := sentAs[helperapi.StorageTestRequest](t, e, helperapi.ActionBackupTest); q.Storage.SecretKey != "S3CRET" {
		t.Fatalf("test %+v", q)
	}

	// A plan reaches the helper with the bucket's credentials.
	recipient := "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p"
	plan := map[string]any{"name": "Shop nightly", "targets": []any{map[string]any{"kind": "site", "site": "shop"},
		map[string]any{"kind": "database", "engine": "mysql", "database": "shop"}}, "storageId": sid, "recipients": []string{recipient}}
	admin.expect(admin.do("POST", "/api/v1/backups/plans", map[string]any{"name": "x", "targets": []any{}}), 400, "validation.invalid")
	p := admin.expect(admin.do("POST", "/api/v1/backups/plans", plan), 201, "")
	pid := p.body["id"].(string)
	applied := sentAs[helperapi.BackupPlan](t, e, helperapi.ActionBackupPlanApply)
	if applied.ID != pid || applied.Storage == nil || applied.Storage.SecretKey != "S3CRET" || applied.Retention != helperapi.DefaultRetention || applied.KeepLocal {
		t.Fatalf("applied %+v", applied)
	}
	// Changing the bucket applies its plans again.
	storage["secretKey"] = ""
	storage["prefix"] = "cairn"
	admin.expect(admin.do("PUT", "/api/v1/backups/storages/"+sid, storage), 200, "")
	if applied := sentAs[helperapi.BackupPlan](t, e, helperapi.ActionBackupPlanApply); applied.Storage.Prefix != "cairn" || applied.Storage.SecretKey != "S3CRET" {
		t.Fatalf("re-applied %+v", applied.Storage)
	}
	admin.expect(admin.do("DELETE", "/api/v1/backups/storages/"+sid, nil), 409, "backup.storage_in_use")

	// A plan a task runs stays.
	e.opsSucceed(helperapi.ActionCronApply, helperapi.ActionBackupRun, helperapi.ActionBackupRestore)
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/cron", map[string]any{"name": "Backup", "type": "backup", "schedule": "daily", "plan": pid, "enabled": true}), 202, ""))
	admin.expect(admin.do("DELETE", "/api/v1/backups/plans/"+pid, nil), 409, "backup.plan_in_use")
	plans := admin.expect(admin.do("GET", "/api/v1/backups/plans", nil), 200, "").body["plans"].([]any)
	if v := plans[0].(map[string]any); len(v["tasks"].([]any)) != 1 || len(v["recipients"].([]any)) != 1 {
		t.Fatalf("plans %v", plans)
	}
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/backups/plans/"+pid+"/run", nil), 202, ""))

	// Downloads: the local copy, else a link to the bucket's.
	dir := filepath.Join(e.dataDir, "backups/plans", pid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	local := "site-shop_20261116-030000.tar.zst.age"
	if err := os.WriteFile(filepath.Join(dir, local), []byte("age-encryption.org/v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, body := admin.raw("GET", "/api/v1/backups/plans/"+pid+"/files/"+local, nil)
	if res.StatusCode != 200 || string(body) != "age-encryption.org/v1" {
		t.Fatalf("local download %d %q", res.StatusCode, body)
	}
	e.helper.respond(helperapi.ActionBackupPresign, helperapi.PresignResponse{URL: "https://acct.r2.example/x?sig=1", Expires: time.Now().Unix() + 900})
	nofollow := *admin.hc
	nofollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/backups/plans/"+pid+"/files/db-mysql-shop_20261116-030000.sql.zst.age", nil)
	rres, err := nofollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = rres.Body.Close()
	if rres.StatusCode != http.StatusFound || rres.Header.Get("Location") != "https://acct.r2.example/x?sig=1" {
		t.Fatalf("remote download %d %v", rres.StatusCode, rres.Header)
	}

	// Restores carry the private key to the helper and nowhere else.
	// The key is generated per run so no private key lives in the repository.
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	key := identity.String()
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/backups/plans/"+pid+"/restore", map[string]any{"name": local, "identity": key,
		"dest": map[string]any{"root": "sites", "path": "shop"}}), 202, ""))
	if q := sentAs[helperapi.BackupRestoreRequest](t, e, helperapi.ActionBackupRestore); q.Identity != key || q.Dest.Path != "shop" {
		t.Fatalf("restore %+v", q)
	}
	var leaked int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE params LIKE '%AGE-SECRET-KEY%'`).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("the private key reached the audit log")
	}
	admin.expect(admin.do("POST", "/api/v1/backups/plans/"+pid+"/restore", map[string]any{"name": local, "identity": "not a key",
		"dest": map[string]any{"root": "sites", "path": "shop"}}), 400, "validation.invalid")

	// A failed run is reported once.
	got := e.published(events.BackupFailed)
	e.helper.respond(helperapi.ActionBackupRuns, helperapi.BackupRuns{Runs: []helperapi.BackupRun{{Started: 1794800000, Error: &helperapi.Error{Code: helperapi.CodeBackupStorage}}}})
	e.backups.Check(context.Background())
	e.backups.Check(context.Background())
	if evs := got(); len(evs) != 1 || evs[0].Params["code"] != helperapi.CodeBackupStorage {
		t.Fatalf("events %+v", evs)
	}
}

func TestLogsModule(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionLogJournal, helperapi.Journal{Entries: []helperapi.JournalEntry{{Message: "started"}}, Cursor: "s=1"})
	r := admin.expect(admin.do("GET", "/api/v1/logs/journal?unit=nginx.service&priority=4&grep=error&limit=50", nil), 200, "")
	if r.body["cursor"] != "s=1" {
		t.Fatalf("journal %s", r.raw)
	}
	if q := sentAs[helperapi.JournalRequest](t, e, helperapi.ActionLogJournal); q.Unit != "nginx.service" || q.Priority != 4 || q.Limit != 50 {
		t.Fatalf("sent %+v", q)
	}
	admin.expect(admin.do("GET", "/api/v1/logs/journal?unit=x%3Brm.service", nil), 400, "validation.invalid")
	admin.expect(admin.do("GET", "/api/v1/logs/journal?priority=9", nil), 400, "validation.invalid")
	e.helper.mu.Lock()
	e.helper.errs = map[string]error{helperapi.ActionLogJournal: &helperapi.Error{Code: helperapi.CodeLogNoJournal}}
	e.helper.mu.Unlock()
	admin.expect(admin.do("GET", "/api/v1/logs/journal", nil), 404, helperapi.CodeLogNoJournal)
}
