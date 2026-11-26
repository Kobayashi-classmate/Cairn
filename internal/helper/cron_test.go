// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/netx"
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
		"User=cw-shop", "WorkingDirectory=/srv/www/shop", "ProtectSystem=strict", "ReadWritePaths=/srv/www/shop", "NoNewPrivileges=yes", "TimeoutStartSec=60min",
		// As a site's owner, it may not listen on a port (an app's among them).
		"SocketBindDeny=any"} {
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
	if unit := e.read("/etc/systemd/system/cairn-cron-t0123456789abcde.service"); strings.Contains(unit, "SocketBind") {
		t.Fatalf("a login user's task:\n%s", unit)
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
	if !strings.Contains(unit, "DynamicUser=yes") || strings.Contains(unit, "s3cret") ||
		!strings.Contains(unit, "IPAddressDeny=169.254.0.0/16 fe80::/10 100.100.100.200/32 fd00:ec2::254/128") {
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

func TestCronRunRecordsAndFailedEnable(t *testing.T) {
	e := newCronEnv(t)
	spec := helperapi.CronSpec{ID: "t4123456789abcde", Type: "shell", Schedule: "daily", User: "alice", Script: "true", Enabled: true}
	if op, log := e.run(helperapi.ActionCronApply, spec); op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op, log)
	}
	// The expression is never an option of systemd-analyze.
	if !e.f.ran("query systemd-analyze calendar --iterations=1 -- daily") {
		t.Fatal("schedule checked without --")
	}
	for _, s := range []string{"-h", "--version", "-*-* 00:00"} {
		if helperapi.ValidSchedule(s) {
			t.Errorf("schedule %q accepted", s)
		}
	}
	if !helperapi.ValidSchedule("*-*-* 00:00:00") {
		t.Error("a schedule starting with * refused")
	}

	t.Setenv("INVOCATION_ID", strings.Repeat("a1", 16))
	if err := e.c.TaskStart(spec.ID); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERVICE_RESULT", "success")
	t.Setenv("EXIT_STATUS", "0")
	if err := e.c.TaskDone(spec.ID); err != nil {
		t.Fatal(err)
	}
	// A run whose start was not recorded gets a record of its own; the
	// earlier run keeps its result.
	t.Setenv("INVOCATION_ID", strings.Repeat("b2", 16))
	t.Setenv("SERVICE_RESULT", "exit-code")
	t.Setenv("EXIT_STATUS", "1")
	if err := e.c.TaskDone(spec.ID); err != nil {
		t.Fatal(err)
	}
	runs := e.c.runs(spec.ID)
	if len(runs) != 2 || runs[0].Invocation != strings.Repeat("b2", 16) || runs[0].Result != "failed" || runs[0].Started == 0 ||
		runs[1].Invocation != strings.Repeat("a1", 16) || runs[1].Result != "success" {
		t.Fatalf("runs %+v", runs)
	}

	// systemd failing to take a new task up: the task is saved with its
	// files, so removing it cleans them up.
	other := helperapi.CronSpec{ID: "t5123456789abcde", Type: "shell", Schedule: "daily", User: "alice", Script: "true", Enabled: true}
	e.f.failPrefix = "systemctl enable"
	if op, _ := e.run(helperapi.ActionCronApply, other); op.State != helperapi.OpFailed {
		t.Fatalf("apply with a failing enable: %+v", op)
	}
	e.f.failPrefix = ""
	if _, err := e.c.spec(other.ID); err != nil || !e.exists("/etc/systemd/system/cairn-cron-t5123456789abcde.timer") {
		t.Fatalf("task not saved with its files: %v", err)
	}
	if op, log := e.run(helperapi.ActionCronRemove, helperapi.CronRef{ID: other.ID}); op.State != helperapi.OpSucceeded {
		t.Fatalf("remove: %+v\n%s", op, log)
	}
	for _, p := range []string{"/etc/systemd/system/cairn-cron-t5123456789abcde.timer", "/etc/systemd/system/cairn-cron-t5123456789abcde.service", "/etc/cairn-cron/t5123456789abcde.sh"} {
		if e.exists(p) {
			t.Errorf("%s left behind", p)
		}
	}
}

func TestJournal(t *testing.T) {
	e := newCronEnv(t)
	writeSystemFile(t, e.st.Root, "/usr/bin/journalctl", "#!binary")
	e.f.journal = `{"__CURSOR":"s=1;i=1","__REALTIME_TIMESTAMP":"1794800000000000","PRIORITY":"3","_SYSTEMD_UNIT":"nginx.service","SYSLOG_IDENTIFIER":"nginx","_PID":"42","MESSAGE":"bind() failed"}
{"__CURSOR":"s=1;i=2","__REALTIME_TIMESTAMP":"1794800000000001","MESSAGE":[104,105]}
`
	RegisterLogs(e.r, &Logs{Root: e.st.Root, Software: e.st.Software, Sites: e.st, Cron: e.c})
	// The whole journal (administrators).
	j, err := call[helperapi.Journal](t, e.r, helperapi.ActionLogJournalAll, helperapi.JournalRequest{Unit: "nginx.service", Priority: 3, Grep: "failed"})
	if err != nil || len(j.Entries) != 2 || j.Entries[0].Message != "bind() failed" || j.Entries[0].PID != 42 || j.Entries[1].Message != "hi" || j.Cursor != "s=1;i=2" {
		t.Fatalf("journal %+v %v", j, err)
	}
	if !e.f.ran("query journalctl --no-pager --output=json --all --lines=200 --unit=nginx.service --priority=3 --grep=failed") {
		t.Fatalf("journalctl arguments: %v", e.f.cmds)
	}
	for _, action := range []string{helperapi.ActionLogJournal, helperapi.ActionLogJournalAll} {
		if _, err := call[helperapi.Journal](t, e.r, action, helperapi.JournalRequest{Unit: "nginx; rm"}); err == nil {
			t.Fatalf("%s: a bad unit name must be refused", action)
		}
	}
}

// log.journal and log.units cover what every reader of logs may see: the
// panel's web service, the installed software, app sites and the tasks
// that run as no one but a site's owner. The rest of the journal (root's
// tasks, sudo, sshd, the helper's record of every action) is only in
// log.journal_all.
func TestJournalForReaders(t *testing.T) {
	e := newCronEnv(t)
	writeSystemFile(t, e.st.Root, "/usr/bin/journalctl", "#!binary")
	e.f.journal = `{"__CURSOR":"s=1;i=1","__REALTIME_TIMESTAMP":"1794800000000000","PRIORITY":"6","_SYSTEMD_UNIT":"php8.4-fpm.service","MESSAGE":"ready"}` + "\n"
	e.f.installed = map[string]string{"php8.4-fpm": "8.4.27-1~deb13u1", "php8.4-cli": "8.4.27-1~deb13u1"}
	writeSystemFile(t, e.st.Root, siteStateDir+"/node.json", `{"spec":{"code":"node","type":"app","app":{"command":"npm start"}}}`)
	writeSystemFile(t, e.st.Root, siteStateDir+"/blog.json", `{"spec":{"code":"blog","type":"php"}}`)
	tasks := map[string]helperapi.CronSpec{
		"t1000000000000aa": {Type: helperapi.CronShell, User: "cw-shop"}, // the site's owner: shared
		"t2000000000000aa": {Type: helperapi.CronShell, User: "root"},
		"t3000000000000aa": {Type: helperapi.CronShell, User: "alice"},   // a login user
		"t4000000000000aa": {Type: helperapi.CronShell, User: "cw-gone"}, // no such site any more
		"t5000000000000aa": {Type: helperapi.CronURL, URL: "http://127.0.0.1/cron"},
		"t6000000000000aa": {Type: helperapi.CronBackup, Plan: "p1"},
	}
	for id, s := range tasks {
		s.ID, s.Schedule = id, "daily"
		data, _ := json.Marshal(s)
		writeSystemFile(t, "/", e.c.stateDir(id+".json"), string(data))
	}
	RegisterLogs(e.r, &Logs{Root: e.st.Root, Software: e.st.Software, Sites: e.st, Cron: e.c})

	want := []string{"cairn-app-node.service", "cairn-cron-t1000000000000aa.service", "cairn-cron-t5000000000000aa.service",
		"cairn-cron-t6000000000000aa.service", "cairn-web.service", "php8.4-fpm.service"}
	u, err := call[helperapi.Units](t, e.r, helperapi.ActionLogUnits, helperapi.Empty{})
	if err != nil || !slices.Equal(u.Units, want) {
		t.Fatalf("units %v %v, want %v", u.Units, err, want)
	}
	journalRuns := func() []string {
		e.f.mu.Lock()
		defer e.f.mu.Unlock()
		var out []string
		for _, c := range e.f.cmds {
			if strings.HasPrefix(c, "query journalctl") {
				out = append(out, c)
			}
		}
		return out
	}
	// No unit named: all of them, and only them.
	j, err := call[helperapi.Journal](t, e.r, helperapi.ActionLogJournal, helperapi.JournalRequest{Grep: "COMMAND"})
	if err != nil || len(j.Entries) != 1 {
		t.Fatalf("journal %+v %v", j, err)
	}
	runs := journalRuns()
	if len(runs) != 1 || runs[0] != "query journalctl --no-pager --output=json --all --lines=200 --unit="+strings.Join(want, " --unit=")+" --grep=COMMAND --case-sensitive=no" {
		t.Fatalf("journalctl arguments: %v", runs)
	}
	// One of them.
	if _, err := call[helperapi.Journal](t, e.r, helperapi.ActionLogJournal, helperapi.JournalRequest{Unit: "php8.4-fpm.service"}); err != nil {
		t.Fatal(err)
	}
	if runs := journalRuns(); len(runs) != 2 || !strings.HasSuffix(runs[1], " --lines=200 --unit=php8.4-fpm.service") {
		t.Fatalf("journalctl arguments: %v", runs)
	}
	// Anything else is refused before the journal is read.
	for _, unit := range []string{"ssh.service", "sshd.service", "cairn-helper.service", "cron.service", "user@0.service", "session-3.scope",
		"cairn-cron-t2000000000000aa.service", "cairn-cron-t3000000000000aa.service", "cairn-cron-t4000000000000aa.service", "cairn-term-0123456789abcdef.service"} {
		_, err := call[helperapi.Journal](t, e.r, helperapi.ActionLogJournal, helperapi.JournalRequest{Unit: unit})
		if he, ok := helperapi.AsError(err); !ok || he.Code != helperapi.CodeLogUnitNotAllowed {
			t.Errorf("%s: %v", unit, err)
		}
	}
	if runs := journalRuns(); len(runs) != 2 {
		t.Fatalf("the journal was read for a unit not allowed: %v", runs)
	}
	// Administrators: the whole journal, every unit.
	if _, err := call[helperapi.Journal](t, e.r, helperapi.ActionLogJournalAll, helperapi.JournalRequest{Unit: "ssh.service"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call[helperapi.Journal](t, e.r, helperapi.ActionLogJournalAll, helperapi.JournalRequest{}); err != nil {
		t.Fatal(err)
	}
	if runs := journalRuns(); len(runs) != 4 || !strings.HasSuffix(runs[2], " --unit=ssh.service") || strings.Contains(runs[3], "--unit") {
		t.Fatalf("journalctl arguments: %v", runs)
	}
	if _, err := call[helperapi.Units](t, e.r, helperapi.ActionLogUnitsAll, helperapi.Empty{}); err != nil || !e.f.ran("query systemctl list-units --type=service --all") {
		t.Fatalf("all units: %v %v", err, e.f.cmds)
	}
}

// A run's output is read only for a run of the task named, from that
// task's unit.
func TestCronLogOwnRunsOnly(t *testing.T) {
	e := newCronEnv(t)
	writeSystemFile(t, e.st.Root, "/usr/bin/journalctl", "#!binary")
	e.f.journal = "task output\n"
	own, other := "t6123456789abcde", "t7123456789abcde"
	mine, theirs := strings.Repeat("a1", 16), strings.Repeat("c3", 16)
	if err := e.c.saveRuns(own, []helperapi.CronRun{{Invocation: mine, Started: 1794800000, Result: "success"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.c.saveRuns(other, []helperapi.CronRun{{Invocation: theirs, Started: 1794800000, Result: "success"}}); err != nil {
		t.Fatal(err)
	}

	out, err := call[helperapi.CronLog](t, e.r, helperapi.ActionCronLog, helperapi.CronLogRequest{ID: own, Invocation: mine})
	if err != nil || len(out.Lines) != 1 || out.Lines[0] != "task output" {
		t.Fatalf("own run: %+v %v", out, err)
	}
	if !e.f.ran("query journalctl --no-pager --output=cat --lines=2000 _SYSTEMD_UNIT=cairn-cron-" + own + ".service _SYSTEMD_INVOCATION_ID=" + mine) {
		t.Fatalf("journalctl arguments: %v", e.f.cmds)
	}

	// Another task's invocation, and one of no task at all (a task without
	// any recorded run), are not found and the journal is not read.
	e.f.mu.Lock()
	e.f.cmds = nil
	e.f.mu.Unlock()
	for _, req := range []helperapi.CronLogRequest{{ID: own, Invocation: theirs}, {ID: "t0123456789abcde", Invocation: theirs}} {
		_, err := call[helperapi.CronLog](t, e.r, helperapi.ActionCronLog, req)
		if he, ok := helperapi.AsError(err); !ok || he.Code != helperapi.CodeCronNotFound {
			t.Fatalf("%+v: %v", req, err)
		}
	}
	if e.f.ran("query journalctl") {
		t.Fatal("journal read for a foreign invocation")
	}
}

// A URL task's log names the scheme and host of its URL, nothing else of
// it: its user, password, path and query (where a site's cron key is)
// never reach the journal, whether the request works or not, also when a
// redirect fails.
func TestTaskURLLogsOnlyTheHost(t *testing.T) {
	secret := []string{"bob", "pw-in-userinfo", "cron.php", "s3cret-token", "nightly", "redirect-token"}
	clean := func(what, text string) {
		t.Helper()
		for _, s := range secret {
			if strings.Contains(text, s) {
				t.Errorf("%s names %q: %s", what, s, text)
			}
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	t.Cleanup(srv.Close)
	t.Setenv("CAIRN_URL", strings.Replace(srv.URL, "http://", "http://bob:pw-in-userinfo@", 1)+"/cron.php?key=s3cret-token&job=nightly")
	var out strings.Builder
	if err := TaskURL(t.Context(), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != srv.URL+": 200 OK (2 bytes)\n" {
		t.Errorf("success: %q", out.String())
	}
	clean("the success line", out.String())

	// Nothing listens there any more.
	srv.Close()
	err := TaskURL(t.Context(), &out)
	if err == nil || !strings.Contains(err.Error(), srv.URL) || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("failure: %v", err)
	}
	clean("the failure", err.Error())
	// An HTTP error is the status alone.
	notFound := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(notFound.Close)
	t.Setenv("CAIRN_URL", notFound.URL+"/cron.php?key=s3cret-token")
	if err := TaskURL(t.Context(), io.Discard); err == nil || err.Error() != "404 Not Found" {
		t.Fatalf("not found: %v", err)
	}
	// A redirect to where nothing listens.
	moved := httptest.NewServer(http.RedirectHandler(srv.URL+"/next?t=redirect-token", http.StatusFound))
	t.Cleanup(moved.Close)
	t.Setenv("CAIRN_URL", moved.URL+"/cron.php?key=s3cret-token")
	if err := TaskURL(t.Context(), io.Discard); err == nil {
		t.Fatal("a redirect to a closed port worked")
	} else {
		clean("a failed redirect", err.Error())
	}
}

func TestTaskURLAddresses(t *testing.T) {
	// A site's own cron URL on this host is fine.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++ }))
	t.Cleanup(srv.Close)
	t.Setenv("CAIRN_URL", srv.URL+"/cron")
	var out strings.Builder
	if err := TaskURL(t.Context(), &out); err != nil || hits != 1 {
		t.Fatalf("loopback: %v (%d requests) %s", err, hits, out.String())
	}
	// Metadata services and link-local addresses are refused before any
	// packet leaves, also through a proxy from the environment.
	t.Setenv("HTTP_PROXY", srv.URL)
	for _, u := range []string{"http://169.254.169.254/latest/meta-data/", "http://100.100.100.200/latest/meta-data/", "http://[fd00:ec2::254]/", "http://[fe80::1]/"} {
		t.Setenv("CAIRN_URL", u)
		err := TaskURL(t.Context(), io.Discard)
		var denied *netx.DeniedError
		if !errors.As(err, &denied) {
			t.Errorf("%s: %v", u, err)
		}
	}
	if hits != 1 {
		t.Fatalf("the proxy was used: %d requests", hits)
	}
}
