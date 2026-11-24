// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	e.helper.respond(helperapi.ActionTermUsers, helperapi.TermUsers{RootTerminal: true, Users: []helperapi.TermUser{
		{Name: "cw-shop", Kind: helperapi.TermUserSite, Home: "/srv/www/shop", Site: "shop"},
		{Name: "debian", Kind: helperapi.TermUserUser, Home: "/home/debian"},
		{Name: "root", Kind: helperapi.TermUserRoot, Home: "/root"}}})
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
	// Running one is running a root script.
	op.expect(op.do("POST", "/api/v1/cron/"+rootID+"/run", nil), 403, "auth.forbidden")
	// Ordinary users may sudo (the default user of cloud images): the same
	// rules as root. Operators run tasks as site owners only.
	sudoer := map[string]any{"name": "As debian", "type": "shell", "schedule": "hourly", "user": "debian", "script": "id", "enabled": true}
	op.expect(op.do("POST", "/api/v1/cron", sudoer), 403, "auth.forbidden")
	op.expect(op.do("POST", "/api/v1/cron", map[string]any{"name": "Nobody", "type": "shell", "schedule": "hourly", "user": "ghost", "script": "id", "enabled": true}), 403, "auth.forbidden")
	if users := op.expect(op.do("GET", "/api/v1/cron/users", nil), 200, "").body["users"].([]any); len(users) != 1 || users[0].(map[string]any)["name"] != "cw-shop" {
		t.Fatalf("operator's run-as users %v", users)
	}
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/cron", sudoer), 202, ""))
	sudoID := sentAs[helperapi.CronSpec](t, e, helperapi.ActionCronApply).ID
	op.expect(op.do("POST", "/api/v1/cron/"+sudoID+"/run", nil), 403, "auth.forbidden")
	op.expect(op.do("PUT", "/api/v1/cron/"+sudoID, task), 403, "auth.forbidden")
	// Scripts and URLs (they may hold secrets) show only to those who may
	// change the task: readers see neither, operators the scripts of site
	// owners' tasks only.
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/cron", map[string]any{"name": "Ping", "type": "url", "schedule": "hourly",
		"url": "https://example.com/cron?key=s3cret", "enabled": true}), 202, ""))
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "aud", "password": pw, "role": "auditor"}), 201, "")
	aud := e.client()
	aud.expect(aud.login("aud", pw), 200, "")
	shown := func(c *client) map[string]string {
		out := map[string]string{}
		for _, x := range c.expect(c.do("GET", "/api/v1/cron", nil), 200, "").body["tasks"].([]any) {
			v := x.(map[string]any)
			script, _ := v["script"].(string)
			url, _ := v["url"].(string)
			out[v["name"].(string)] = script + url
		}
		return out
	}
	if got := shown(aud); got["Nightly cleanup"] != "" || got["Root job"] != "" || got["Ping"] != "" {
		t.Fatalf("auditor sees %v", got)
	}
	if got := shown(op); got["Nightly cleanup"] == "" || got["Root job"] != "" || got["As debian"] != "" || got["Ping"] == "" {
		t.Fatalf("operator sees %v", got)
	}
	if got := shown(admin); got["Root job"] == "" || got["As debian"] == "" {
		t.Fatalf("administrator sees %v", got)
	}
	// A task of a user that is gone (a deleted site's owner) may be deleted,
	// and only deleted, by operators.
	orphan := map[string]any{"name": "Old site", "type": "shell", "schedule": "daily", "user": "cw-gone", "script": "true", "enabled": true}
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/cron", orphan), 202, ""))
	orphanID := sentAs[helperapi.CronSpec](t, e, helperapi.ActionCronApply).ID
	op.expect(op.do("POST", "/api/v1/cron/"+orphanID+"/run", nil), 403, "auth.forbidden")
	op.expect(op.do("PUT", "/api/v1/cron/"+orphanID, orphan), 403, "auth.forbidden")
	op.runJob(op.expect(op.do("DELETE", "/api/v1/cron/"+orphanID, nil), 202, ""))
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
	// The output of a privileged task is for those with cron.root; that of a
	// site owner's for every reader.
	aud.expect(aud.do("GET", "/api/v1/cron/"+spec.ID+"/runs/"+strings.Repeat("ab", 16)+"/log", nil), 200, "")
	for _, id := range []string{rootID, sudoID} {
		aud.expect(aud.do("GET", "/api/v1/cron/"+id+"/runs/"+strings.Repeat("ab", 16)+"/log", nil), 403, "auth.forbidden")
		op.expect(op.do("GET", "/api/v1/cron/"+id+"/runs/"+strings.Repeat("ab", 16)+"/log", nil), 403, "auth.forbidden")
		admin.expect(admin.do("GET", "/api/v1/cron/"+id+"/runs/"+strings.Repeat("ab", 16)+"/log", nil), 200, "")
	}
	// So are a privileged task's runs: they name the invocations, and one of
	// them asked for under another task's ID would reach its output.
	reader := e.bearer(func() string {
		r := admin.expect(admin.do("POST", "/api/v1/me/tokens", map[string]any{"name": "reader", "permissions": []string{"cron.read"}, "expiresInDays": 1}), 201, "")
		return r.body["token"].(string)
	}(), "GET", "/api/v1/cron/"+rootID+"/runs", nil)
	if reader.status != 403 || reader.code() != "auth.forbidden" {
		t.Fatalf("token reads the root task's runs: %d %s", reader.status, reader.raw)
	}
	for _, id := range []string{rootID, sudoID} {
		aud.expect(aud.do("GET", "/api/v1/cron/"+id+"/runs", nil), 403, "auth.forbidden")
		op.expect(op.do("GET", "/api/v1/cron/"+id+"/runs", nil), 403, "auth.forbidden")
		admin.expect(admin.do("GET", "/api/v1/cron/"+id+"/runs", nil), 200, "")
	}
	aud.expect(aud.do("GET", "/api/v1/cron/"+spec.ID+"/runs", nil), 200, "")
	op.expect(op.do("GET", "/api/v1/cron/"+spec.ID+"/runs", nil), 200, "")
	// The helper finds no such run of the task named: not found.
	e.helper.mu.Lock()
	e.helper.errs = map[string]error{helperapi.ActionCronLog: &helperapi.Error{Code: helperapi.CodeCronNotFound}}
	e.helper.mu.Unlock()
	aud.expect(aud.do("GET", "/api/v1/cron/"+spec.ID+"/runs/"+strings.Repeat("d4", 16)+"/log", nil), 404, helperapi.CodeCronNotFound)
	if q := sentAs[helperapi.CronLogRequest](t, e, helperapi.ActionCronLog); q.ID != spec.ID || q.Invocation != strings.Repeat("d4", 16) {
		t.Fatalf("log request %+v", q)
	}
	e.helper.mu.Lock()
	e.helper.errs = nil
	e.helper.mu.Unlock()

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
	admin.expect(admin.do("POST", "/api/v1/backups/storages", map[string]any{"name": "x", "endpoint": "bad host!", "bucket": "my-bucket", "accessKey": "a", "secretKey": "s"}), 400, "validation.invalid")
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
	// ?link=1 tells where to download from, as JSON (errors stay JSON too).
	if got := admin.expect(admin.do("GET", "/api/v1/backups/plans/"+pid+"/files/"+local+"?link=1", nil), 200, "").body["url"]; got != "/api/v1/backups/plans/"+pid+"/files/"+local {
		t.Fatalf("local link %v", got)
	}
	if got := admin.expect(admin.do("GET", "/api/v1/backups/plans/"+pid+"/files/db-mysql-shop_20261116-030000.sql.zst.age?link=1", nil), 200, "").body["url"]; got != "https://acct.r2.example/x?sig=1" {
		t.Fatalf("remote link %v", got)
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

	// Storage may be on the LAN (a MinIO), but not on this host or at a
	// metadata service, also behind a name; when saving and when testing.
	e.resolver.set("metadata.example", "100.100.100.200")
	for _, ep := range []string{"127.0.0.1:9000", "localhost:9000", "[::1]:9000", "169.254.169.254", "metadata.example", "[fd00:ec2::254]:80", "0.0.0.0:9000"} {
		body := map[string]any{"name": "x", "endpoint": ep, "bucket": "my-bucket", "accessKey": "a", "secretKey": "s"}
		admin.expect(admin.do("POST", "/api/v1/backups/storages", body), 400, "backup.endpoint_not_allowed")
		admin.expect(admin.do("POST", "/api/v1/backups/storages/test", body), 400, "backup.endpoint_not_allowed")
		admin.expect(admin.do("PUT", "/api/v1/backups/storages/"+sid, body), 400, "backup.endpoint_not_allowed")
	}
	admin.expect(admin.do("POST", "/api/v1/backups/storages", map[string]any{"name": "MinIO", "endpoint": "10.0.0.5:9000", "bucket": "my-bucket",
		"accessKey": "a", "secretKey": "s", "insecure": true, "pathStyle": true}), 201, "")
	// Testing other settings with a saved bucket's key takes a second
	// verification: the key would go to the endpoint the request names.
	fresh := e.client()
	fresh.expect(fresh.login("admin", pw), 200, "")
	fresh.expect(fresh.do("POST", "/api/v1/backups/storages/test", map[string]any{"id": sid, "endpoint": "collector.example", "bucket": "my-bucket", "accessKey": "AK"}),
		403, "auth.step_up_required")
	fresh.expect(fresh.do("POST", "/api/v1/backups/storages/test", map[string]any{"endpoint": "collector.example", "bucket": "my-bucket", "accessKey": "a", "secretKey": "s"}), 204, "")

	// Directories outside the sites root are the file manager's
	// administrators' (files.admin): backing them up, restoring into them,
	// and reaching the backups of such a plan.
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "oper", "password": pw, "role": "operator"}), 201, "")
	op := e.client()
	op.expect(op.login("oper", pw), 200, "")
	op.elevate("")
	system := map[string]any{"name": "System", "targets": []any{map[string]any{"kind": "directory", "root": "system", "path": "etc"}}, "storageId": sid}
	op.expect(op.do("POST", "/api/v1/backups/plans", system), 403, "auth.forbidden")
	op.expect(op.do("POST", "/api/v1/backups/plans", map[string]any{"name": "Dumps", "targets": []any{map[string]any{"kind": "directory", "root": "backups"}}}),
		403, "auth.forbidden")
	op.expect(op.do("POST", "/api/v1/backups/plans", map[string]any{"name": "Shop files", "targets": []any{map[string]any{"kind": "directory", "root": "sites", "path": "shop"}}}),
		201, "")
	sysID := admin.expect(admin.do("POST", "/api/v1/backups/plans", system), 201, "").body["id"].(string)
	op.expect(op.do("PUT", "/api/v1/backups/plans/"+sysID, system), 403, "auth.forbidden")
	// Nor deleted by them (with or without its backups).
	op.expect(op.do("DELETE", "/api/v1/backups/plans/"+sysID+"?files=true", nil), 403, "auth.forbidden")
	sysFile := "dir-system-etc_20261116-030000.tar.zst"
	op.expect(op.do("GET", "/api/v1/backups/plans/"+sysID+"/files/"+sysFile, nil), 403, "auth.forbidden")
	op.expect(op.do("GET", "/api/v1/backups/plans/"+sysID+"/files/"+sysFile+"?link=1", nil), 403, "auth.forbidden")
	op.expect(op.do("POST", "/api/v1/backups/plans/"+sysID+"/delete", map[string]any{"names": []string{sysFile}}), 403, "auth.forbidden")
	op.expect(op.do("POST", "/api/v1/backups/plans/"+sysID+"/restore", map[string]any{"name": sysFile, "dest": map[string]any{"root": "sites", "path": "x"}}),
		403, "auth.forbidden")
	op.expect(op.do("POST", "/api/v1/backups/plans/"+pid+"/restore", map[string]any{"name": local, "dest": map[string]any{"root": "system", "path": "etc"}}),
		403, "auth.forbidden")
	// The bucket of such a plan cannot be moved by them either.
	op.expect(op.do("PUT", "/api/v1/backups/storages/"+sid, map[string]any{"name": "R2", "endpoint": "collector.example", "bucket": "panel-backups", "accessKey": "AK"}),
		403, "auth.forbidden")
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/backups/plans/"+sysID+"/restore", map[string]any{"name": sysFile, "dest": map[string]any{"root": "system", "path": "restore"}}), 202, ""))

	// Rewriting such a plan to the sites does not hand its earlier backups
	// over: changing it takes the permission and a second verification,
	// and a backup goes by what it holds (its name), not by the plan now.
	shopOnly := map[string]any{"name": "System", "targets": []any{map[string]any{"kind": "site", "site": "shop"}}, "storageId": sid}
	op.expect(op.do("PUT", "/api/v1/backups/plans/"+sysID, shopOnly), 403, "auth.forbidden")
	fresh.expect(fresh.do("PUT", "/api/v1/backups/plans/"+sysID, shopOnly), 403, "auth.step_up_required")
	admin.expect(admin.do("PUT", "/api/v1/backups/plans/"+sysID, shopOnly), 200, "")
	for _, plan := range []string{sysID, pid} {
		op.expect(op.do("GET", "/api/v1/backups/plans/"+plan+"/files/"+sysFile, nil), 403, "auth.forbidden")
		op.expect(op.do("POST", "/api/v1/backups/plans/"+plan+"/restore", map[string]any{"name": sysFile, "dest": map[string]any{"root": "sites", "path": "x"}}),
			403, "auth.forbidden")
		op.expect(op.do("GET", "/api/v1/backups/plans/"+plan+"/files/dir-backups_20261116-030000.tar.zst", nil), 403, "auth.forbidden")
	}
	// Backups of sites stay the managers'.
	siteFile := "dir-sites-shop_20261116-030000.tar.zst"
	if err := os.MkdirAll(filepath.Join(e.dataDir, "backups/plans", sysID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dataDir, "backups/plans", sysID, siteFile), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, _ := op.raw("GET", "/api/v1/backups/plans/"+sysID+"/files/"+siteFile, nil); res.StatusCode != 200 {
		t.Fatalf("site backup download %d", res.StatusCode)
	}

	// Deleting a bucket while a plan is saved with it, or a plan while a
	// task is saved for it: one of the two fails, nothing points nowhere.
	site := []any{map[string]any{"kind": "site", "site": "shop"}}
	for i := 0; i < 5; i++ {
		st := admin.expect(admin.do("POST", "/api/v1/backups/storages", map[string]any{"name": fmt.Sprintf("tmp%d", i), "endpoint": "s3.example.com",
			"bucket": "my-bucket", "accessKey": "a", "secretKey": "s"}), 201, "").body["id"].(string)
		plan := admin.expect(admin.do("POST", "/api/v1/backups/plans", map[string]any{"name": fmt.Sprintf("q%d", i), "targets": site}), 201, "").body["id"].(string)
		var wg sync.WaitGroup
		wg.Add(4)
		go func() { defer wg.Done(); admin.do("DELETE", "/api/v1/backups/storages/"+st, nil) }()
		go func() {
			defer wg.Done()
			admin.do("POST", "/api/v1/backups/plans", map[string]any{"name": fmt.Sprintf("p%d", i), "targets": site, "storageId": st})
		}()
		go func() { defer wg.Done(); admin.do("DELETE", "/api/v1/backups/plans/"+plan, nil) }()
		go func() {
			defer wg.Done()
			admin.do("POST", "/api/v1/cron", map[string]any{"name": fmt.Sprintf("t%d", i), "type": "backup", "schedule": "daily", "plan": plan, "enabled": true})
		}()
		wg.Wait()
	}
	var dangling int
	_ = e.db.QueryRow(`SELECT count(*) FROM backup_plans WHERE storage_id != '' AND storage_id NOT IN (SELECT id FROM backup_storages)`).Scan(&dangling)
	if dangling != 0 {
		t.Fatalf("%d plans use a deleted bucket", dangling)
	}
	_ = e.db.QueryRow(`SELECT count(*) FROM cron_tasks WHERE json_extract(spec, '$.type') = 'backup'
		AND json_extract(spec, '$.plan') NOT IN (SELECT id FROM backup_plans)`).Scan(&dangling)
	if dangling != 0 {
		t.Fatalf("%d tasks run a deleted plan", dangling)
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

func TestCronRecoverInterrupted(t *testing.T) {
	e := newEnv(t)
	for _, q := range []string{
		`INSERT INTO jobs (id, kind, permission, manage_permission, state, created_at) VALUES
			('aaaaaaaaaaaaaaaaaaaaaaaa', 'cron.update', 'cron.read', 'cron.manage', 'interrupted', 1),
			('bbbbbbbbbbbbbbbbbbbbbbbb', 'cron.update', 'cron.read', 'cron.manage', 'running', 1)`,
		`INSERT INTO cron_tasks (id, name, spec, state, last_job, created_at, updated_at) VALUES
			('tgone', 'gone', '{}', 'applying', 'aaaaaaaaaaaaaaaaaaaaaaaa', 1, 1),
			('tnever', 'never', '{}', 'applying', '', 1, 1),
			('tbusy', 'busy', '{}', 'applying', 'bbbbbbbbbbbbbbbbbbbbbbbb', 1, 1),
			('tfine', 'fine', '{}', 'active', 'aaaaaaaaaaaaaaaaaaaaaaaa', 1, 1)`,
	} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.cron.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"tgone": "failed job.interrupted", "tnever": "failed job.interrupted", "tbusy": "applying ", "tfine": "active ",
	} {
		var state, code string
		_ = e.db.QueryRow(`SELECT state, error_code FROM cron_tasks WHERE id = ?`, id).Scan(&state, &code)
		if got := state + " " + code; got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
}
