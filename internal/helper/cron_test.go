// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

const calendarOut = `  Original form: daily
Normalized form: *-*-* 00:00:00
    Next elapse: Tue 2026-11-17 08:00:00 CST
       (in UTC): Tue 2026-11-17 00:00:00 UTC
       From now: 8h left
`

type cronTestEnv struct {
	*siteEnv
	c *Cron
}

func newCronEnv(t *testing.T) *cronTestEnv {
	e := newSiteEnv(t)
	e.f.systemd = true
	e.f.calendar = calendarOut
	files := &Files{Root: e.st.Root, DataDir: "/var/lib/cairn", PanelUser: "cairn", StateDir: filepath.Join(e.st.Root, "/var/lib/cairn-helper"),
		RootAccess: "panel", Log: slog.New(slog.DiscardHandler)}
	b := &Backups{Files: files, DBs: &DBs{Root: e.st.Root}, Log: files.Log}
	c := &Cron{Sites: e.st, Files: files, Backups: b}
	RegisterFiles(e.r, e.o, files)
	RegisterCron(e.r, e.o, c)
	RegisterBackups(e.r, e.o, b)
	writeSystemFile(t, e.st.Root, "/etc/passwd", strings.Join([]string{
		"root:x:0:0:root:/root:/bin/bash", "alice:x:1000:1000::/home/alice:/bin/bash", "cw-shop:x:997:997::/srv/www/shop:/usr/sbin/nologin"}, "\n"))
	if err := os.MkdirAll(filepath.Join(e.st.Root, "/srv/www/shop"), 0o750); err != nil {
		t.Fatal(err)
	}
	return &cronTestEnv{siteEnv: e, c: c}
}

func TestCronShellTasks(t *testing.T) {
	e := newCronEnv(t)
	spec := helperapi.CronSpec{ID: "t0123456789abcde", Type: "shell", Schedule: "daily", User: "cw-shop", Script: "php app/cron.php", Enabled: true}
	op, log := e.run(helperapi.ActionCronApply, spec)
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op, log)
	}
	unit := e.read("/etc/systemd/system/cairn-cron-t0123456789abcde.service")
	for _, want := range []string{"Type=oneshot", "ExecStartPre=+/usr/lib/cairn/cairn-helper task start t0123456789abcde",
		"ExecStart=/etc/cairn-cron/t0123456789abcde.sh", "ExecStopPost=+/usr/lib/cairn/cairn-helper task done t0123456789abcde",
		"User=cw-shop", "WorkingDirectory=/srv/www/shop", "ProtectSystem=strict", "ReadWritePaths=/srv/www/shop", "NoNewPrivileges=yes", "TimeoutStartSec=60min"} {
		if !strings.Contains(unit, want) {
			t.Errorf("service lacks %q:\n%s", want, unit)
		}
	}
	if timer := e.read("/etc/systemd/system/cairn-cron-t0123456789abcde.timer"); !strings.Contains(timer, "OnCalendar=daily\nPersistent=true") {
		t.Fatalf("timer:\n%s", timer)
	}
	script := e.read("/etc/cairn-cron/t0123456789abcde.sh")
	if fi, _ := os.Stat(filepath.Join(e.st.Root, "/etc/cairn-cron/t0123456789abcde.sh")); script != "#!/bin/sh\nphp app/cron.php\n" || fi.Mode().Perm() != 0o750 {
		t.Fatalf("script %q %v", script, fi.Mode())
	}
	if !e.f.ran("systemd-analyze verify") || !e.f.ran("systemctl enable --now cairn-cron-t0123456789abcde.timer") {
		t.Fatal("not verified or enabled")
	}

	// Root needs the switch; users that cannot log in are refused.
	spec.User = "root"
	if op, _ := e.run(helperapi.ActionCronApply, spec); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeTermRootDisabled {
		t.Fatalf("root task while off: %+v", op)
	}
	spec.User = "www-data"
	if op, _ := e.run(helperapi.ActionCronApply, spec); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeTermUserNotAllowed {
		t.Fatalf("www-data: %+v", op)
	}
	if _, err := call[helperapi.AccessSettings](t, e.r, helperapi.ActionAccessSet, helperapi.AccessSettings{RootTerminal: true}); err != nil {
		t.Fatal(err)
	}
	spec.User = "root"
	if op, log := e.run(helperapi.ActionCronApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("root task: %+v\n%s", op, log)
	}
	if unit := e.read("/etc/systemd/system/cairn-cron-t0123456789abcde.service"); strings.Contains(unit, "ProtectSystem") || !strings.Contains(unit, "User=root") {
		t.Fatalf("root service:\n%s", unit)
	}
	// Switched off again, a root task refuses to start.
	if _, err := call[helperapi.AccessSettings](t, e.r, helperapi.ActionAccessSet, helperapi.AccessSettings{}); err != nil {
		t.Fatal(err)
	}
	if err := e.c.TaskStart(spec.ID); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Fatalf("root task start: %v", err)
	}

	// Runs are recorded by the service itself.
	spec.User = "alice"
	if op, _ := e.run(helperapi.ActionCronApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("alice: %+v", op)
	}
	if op, _ := e.run(helperapi.ActionCronRun, helperapi.CronRef{ID: spec.ID}); op.State != helperapi.OpSucceeded || !e.f.ran("systemctl start --no-block cairn-cron-t0123456789abcde.service") {
		t.Fatalf("run now: %+v", op)
	}
	t.Setenv("INVOCATION_ID", strings.Repeat("ab", 16))
	if err := e.c.TaskStart(spec.ID); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERVICE_RESULT", "exit-code")
	t.Setenv("EXIT_STATUS", "3")
	if err := e.c.TaskDone(spec.ID); err != nil {
		t.Fatal(err)
	}
	runs, _ := call[helperapi.CronRuns](t, e.r, helperapi.ActionCronRuns, helperapi.CronRef{ID: spec.ID})
	if len(runs.Runs) != 1 || runs.Runs[0].Result != "failed" || runs.Runs[0].ExitCode != 3 || !runs.Runs[0].Manual || runs.Runs[0].Finished == 0 {
		t.Fatalf("runs %+v", runs)
	}
	e.f.systemctlShow = "Id=cairn-cron-t0123456789abcde.service\nActiveState=inactive\n"
	st, _ := call[helperapi.CronStatus](t, e.r, helperapi.ActionCronStatus, helperapi.Empty{})
	s := st.Tasks[spec.ID]
	if !st.Systemd || s.NextRun != 1794873600 || s.Result != "failed" || s.Running {
		t.Fatalf("status %+v", s)
	}

	if op, _ := e.run(helperapi.ActionCronRemove, helperapi.CronRef{ID: spec.ID}); op.State != helperapi.OpSucceeded || e.exists("/etc/systemd/system/cairn-cron-t0123456789abcde.timer") {
		t.Fatalf("remove: %+v", op)
	}
}

func TestCronURLAndBackupTasks(t *testing.T) {
	e := newCronEnv(t)
	url := helperapi.CronSpec{ID: "t1123456789abcde", Type: "url", Schedule: "hourly", URL: "https://example.com/cron?token=s3cret&x=$HOME", Enabled: true}
	if op, log := e.run(helperapi.ActionCronApply, url); op.State != helperapi.OpSucceeded {
		t.Fatalf("url: %+v\n%s", op, log)
	}
	unit := e.read("/etc/systemd/system/cairn-cron-t1123456789abcde.service")
	if !strings.Contains(unit, "DynamicUser=yes") || strings.Contains(unit, "s3cret") {
		t.Fatalf("url service:\n%s", unit)
	}
	env := e.read("/etc/cairn-cron/t1123456789abcde.env")
	if fi, _ := os.Stat(filepath.Join(e.st.Root, "/etc/cairn-cron/t1123456789abcde.env")); env != "CAIRN_URL=\"https://example.com/cron?token=s3cret&x=\\$HOME\"\n" || fi.Mode().Perm() != 0o600 {
		t.Fatalf("env %q %v", env, fi.Mode())
	}
	backup := helperapi.CronSpec{ID: "t2123456789abcde", Type: "backup", Schedule: "daily", Plan: "p0123456789abcde", Enabled: true}
	if op, _ := e.run(helperapi.ActionCronApply, backup); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeCronPlanNotFound {
		t.Fatalf("backup without plan: %+v", op)
	}
	if _, err := call[helperapi.Empty](t, e.r, helperapi.ActionBackupPlanApply, helperapi.BackupPlan{}); err == nil {
		t.Fatal("an empty plan must be refused")
	}
	if err := e.c.Backups.savePlan(helperapi.BackupPlan{ID: "p0123456789abcde", Retention: helperapi.Retention{Last: 1},
		Targets: []helperapi.BackupTarget{{Kind: "site", Site: "shop"}}}); err != nil {
		t.Fatal(err)
	}
	if op, log := e.run(helperapi.ActionCronApply, backup); op.State != helperapi.OpSucceeded ||
		!strings.Contains(e.read("/etc/systemd/system/cairn-cron-t2123456789abcde.service"), "ExecStart=/usr/lib/cairn/cairn-helper task backup t2123456789abcde") {
		t.Fatalf("backup task: %+v\n%s", op, log)
	}
	// A bad schedule, as systemd reads it.
	e.f.calendar = ""
	url.Schedule = "every tuesday-ish"
	if op, _ := e.run(helperapi.ActionCronApply, url); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeCronBadSchedule {
		t.Fatalf("bad schedule: %+v", op)
	}
	e.f.calendar = calendarOut
	info, err := call[helperapi.ScheduleInfo](t, e.r, helperapi.ActionCronSchedule, helperapi.ScheduleRequest{Schedule: "daily"})
	if err != nil || info.Normalized != "*-*-* 00:00:00" || len(info.Next) != 1 || info.Next[0] != 1794873600 {
		t.Fatalf("schedule %+v %v", info, err)
	}
	// Without systemd there are no tasks.
	e.f.systemd = false
	if op, _ := e.run(helperapi.ActionCronApply, url); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeCronSystemd {
		t.Fatalf("no systemd: %+v", op)
	}
	if _, err := call[helperapi.OpRef](t, e.r, helperapi.ActionCronApply, helperapi.CronSpec{ID: "t3123456789abcde", Type: "shell", Schedule: "daily; rm -rf /", User: "alice", Script: "x"}); err == nil {
		t.Fatal("a schedule with ; must be refused")
	}
}

func TestJournal(t *testing.T) {
	e := newCronEnv(t)
	writeSystemFile(t, e.st.Root, "/usr/bin/journalctl", "#!binary")
	e.f.journal = `{"__CURSOR":"s=1;i=1","__REALTIME_TIMESTAMP":"1794800000000000","PRIORITY":"3","_SYSTEMD_UNIT":"nginx.service","SYSLOG_IDENTIFIER":"nginx","_PID":"42","MESSAGE":"bind() failed"}
{"__CURSOR":"s=1;i=2","__REALTIME_TIMESTAMP":"1794800000000001","MESSAGE":[104,105]}
`
	RegisterLogs(e.r, e.st.Root)
	j, err := call[helperapi.Journal](t, e.r, helperapi.ActionLogJournal, helperapi.JournalRequest{Unit: "nginx.service", Priority: 3, Grep: "failed"})
	if err != nil || len(j.Entries) != 2 || j.Entries[0].Message != "bind() failed" || j.Entries[0].PID != 42 || j.Entries[1].Message != "hi" || j.Cursor != "s=1;i=2" {
		t.Fatalf("journal %+v %v", j, err)
	}
	if !e.f.ran("query journalctl --no-pager --output=json --lines=200 --unit=nginx.service --priority=3 --grep=failed") {
		t.Fatalf("journalctl arguments: %v", e.f.cmds)
	}
	if _, err := call[helperapi.Journal](t, e.r, helperapi.ActionLogJournal, helperapi.JournalRequest{Unit: "nginx; rm"}); err == nil {
		t.Fatal("a bad unit name must be refused")
	}
}
