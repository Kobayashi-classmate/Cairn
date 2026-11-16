// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// Scheduled tasks: a oneshot service and a timer per task. The service
// records each run itself (ExecStartPre and ExecStopPost run the helper
// binary with full privileges), so the panel can list runs and read each
// run's output from the journal by its invocation ID.

const (
	// Not below /etc/cairn: that is the panel's (root:cairn 0750), and a
	// task's user must reach its script.
	cronEtcDir   = "/etc/cairn-cron"
	helperBinary = "/usr/lib/cairn/cairn-helper"
	cronRunsKept = 100
)

func cronUnit(id string) string    { return "cairn-cron-" + id }
func cronService(id string) string { return path.Join(appUnitDir, cronUnit(id)+".service") }
func cronTimer(id string) string   { return path.Join(appUnitDir, cronUnit(id)+".timer") }
func cronScript(id string) string  { return path.Join(cronEtcDir, id+".sh") }
func cronEnv(id string) string     { return path.Join(cronEtcDir, id+".env") }

// Cron manages scheduled tasks.
type Cron struct {
	Sites   *Sites
	Files   *Files
	Backups *Backups
}

// RegisterCron adds the task actions.
func RegisterCron(r *Registry, o *Ops, c *Cron) {
	lock := func(any) []string { return []string{"cron"} }
	RegisterOp(r, o, helperapi.ActionCronApply, OpSpec[helperapi.CronSpec]{Locks: func(s helperapi.CronSpec) []string { return lock(s) }, Run: c.apply})
	RegisterOp(r, o, helperapi.ActionCronRemove, OpSpec[helperapi.CronRef]{Locks: func(s helperapi.CronRef) []string { return lock(s) }, Run: c.remove})
	RegisterOp(r, o, helperapi.ActionCronRun, OpSpec[helperapi.CronRef]{Locks: func(s helperapi.CronRef) []string { return lock(s) }, Run: c.runNow})
	Register(r, helperapi.ActionCronStatus, func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.CronStatus, error) {
		return c.status(ctx), nil
	})
	Register(r, helperapi.ActionCronRuns, func(_ context.Context, _ helperapi.Caller, q helperapi.CronRef) (helperapi.CronRuns, error) {
		return helperapi.CronRuns{Runs: c.runs(q.ID)}, nil
	})
	Register(r, helperapi.ActionCronLog, c.log)
	Register(r, helperapi.ActionCronSchedule, func(ctx context.Context, _ helperapi.Caller, q helperapi.ScheduleRequest) (helperapi.ScheduleInfo, error) {
		return checkSchedule(ctx, q.Schedule, 3)
	})
}

func cronErr(code string, params map[string]any) error {
	return &helperapi.Error{Code: code, Params: params}
}

func (c *Cron) stateDir(parts ...string) string {
	return filepath.Join(append([]string{c.Files.StateDir, "cron"}, parts...)...)
}

// Schedules ---------------------------------------------------------------

// checkSchedule asks systemd to read an OnCalendar expression.
func checkSchedule(ctx context.Context, s string, n int) (helperapi.ScheduleInfo, error) {
	out, err := queryCommand(ctx, "systemd-analyze", "calendar", "--iterations="+strconv.Itoa(n), s)
	if errors.Is(err, execx.ErrNotFound) {
		return helperapi.ScheduleInfo{}, cronErr(helperapi.CodeCronSystemd, nil)
	}
	if err != nil {
		return helperapi.ScheduleInfo{}, cronErr(helperapi.CodeCronBadSchedule, map[string]any{"schedule": s, "detail": errDetail(err)})
	}
	info := helperapi.ScheduleInfo{Next: []int64{}}
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if !ok {
			continue
		}
		switch {
		case key == "Normalized form":
			info.Normalized = val
		case key == "Next elapse" || strings.HasPrefix(key, "Iteration #"):
			// The UTC line follows when the system's zone is another.
			if i+1 < len(lines) {
				if k, v, ok := strings.Cut(strings.TrimSpace(lines[i+1]), ": "); ok && k == "(in UTC)" {
					val = v
				}
			}
			if t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", val); err == nil {
				info.Next = append(info.Next, t.Unix())
			}
		}
	}
	if info.Normalized == "" {
		return info, cronErr(helperapi.CodeCronBadSchedule, map[string]any{"schedule": s})
	}
	return info, nil
}

// Units ---------------------------------------------------------------------

var cronServiceTemplate = template.Must(template.New("service").Parse(`# {{.Managed}}
[Unit]
Description=Cairn task {{.ID}}
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStartPre=+{{.Bin}} task start {{.ID}}
ExecStart={{.Exec}}
ExecStopPost=+{{.Bin}} task done {{.ID}}
TimeoutStartSec={{.Timeout}}min
SyslogIdentifier={{.Unit}}
{{- range .Extra}}
{{.}}
{{- end}}
`))

var cronTimerTemplate = template.Must(template.New("timer").Parse(`# {{.Managed}}
[Unit]
Description=Cairn task {{.ID}} schedule

[Timer]
OnCalendar={{.Schedule}}
Persistent=true

[Install]
WantedBy=timers.target
`))

// shellUser checks who a shell task runs as, as for terminals.
func (c *Cron) shellUser(name string) (passwdEntry, helperapi.TermUser, error) {
	var allowed *helperapi.TermUser
	for _, u := range c.Files.termUsers().Users {
		if u.Name == name {
			u := u
			allowed = &u
		}
	}
	if allowed == nil {
		if name == "root" {
			return passwdEntry{}, helperapi.TermUser{}, fileErr(helperapi.CodeTermRootDisabled, nil)
		}
		return passwdEntry{}, helperapi.TermUser{}, fileErr(helperapi.CodeTermUserNotAllowed, map[string]any{"user": name})
	}
	for _, e := range c.Files.passwd() {
		if e.name == name {
			return e, *allowed, nil
		}
	}
	return passwdEntry{}, helperapi.TermUser{}, fileErr(helperapi.CodeTermUserNotAllowed, map[string]any{"user": name})
}

// render returns the task's files.
func (c *Cron) render(spec helperapi.CronSpec) ([]fileChange, error) {
	timeout := spec.TimeoutMin
	if timeout == 0 {
		timeout = 60
	}
	data := map[string]any{"Managed": siteManaged, "ID": spec.ID, "Unit": cronUnit(spec.ID), "Bin": helperBinary, "Timeout": timeout,
		"Schedule": spec.Schedule}
	var extra []string
	var changes []fileChange
	switch spec.Type {
	case helperapi.CronShell:
		e, u, err := c.shellUser(spec.User)
		if err != nil {
			return nil, err
		}
		script := spec.Script
		if !strings.HasPrefix(script, "#!") {
			script = "#!/bin/sh\n" + script
		}
		if !strings.HasSuffix(script, "\n") {
			script += "\n"
		}
		changes = append(changes, fileChange{path: cronScript(spec.ID), data: []byte(script), mode: 0o750})
		data["Exec"] = cronScript(spec.ID)
		extra = append(extra, "User="+e.name, "WorkingDirectory="+u.Home)
		if u.Kind != helperapi.TermUserRoot {
			extra = append(extra, "NoNewPrivileges=yes", "PrivateTmp=yes")
		}
		switch u.Kind {
		case helperapi.TermUserSite:
			extra = append(extra, "ProtectSystem=strict", "ProtectHome=yes", "ReadWritePaths="+siteDirOf(u.Site))
		case helperapi.TermUserUser:
			extra = append(extra, "ProtectSystem=full")
		}
	case helperapi.CronURL:
		changes = append(changes, fileChange{path: cronEnv(spec.ID), data: []byte(`CAIRN_URL="` + envFileQuote(spec.URL) + "\"\n"), mode: 0o600})
		data["Exec"] = helperBinary + " task url"
		extra = append(extra, "DynamicUser=yes", "EnvironmentFile="+cronEnv(spec.ID), "NoNewPrivileges=yes", "PrivateTmp=yes",
			"ProtectSystem=strict", "ProtectHome=yes", "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX")
	case helperapi.CronBackup:
		if _, err := c.Backups.plan(spec.Plan); err != nil {
			return nil, cronErr(helperapi.CodeCronPlanNotFound, map[string]any{"plan": spec.Plan})
		}
		data["Exec"] = helperBinary + " task backup " + spec.ID
		extra = append(extra, "Nice=10", "IOSchedulingClass=idle")
	}
	data["Extra"] = extra
	var service, timer strings.Builder
	if err := cronServiceTemplate.Execute(&service, data); err != nil {
		return nil, err
	}
	if err := cronTimerTemplate.Execute(&timer, data); err != nil {
		return nil, err
	}
	changes = append(changes, fileChange{path: cronService(spec.ID), data: []byte(service.String())},
		fileChange{path: cronTimer(spec.ID), data: []byte(timer.String())})
	return changes, nil
}

func (c *Cron) apply(ctx context.Context, _ helperapi.Caller, spec helperapi.CronSpec, log io.Writer) (any, error) {
	if !systemdRunning() {
		return nil, cronErr(helperapi.CodeCronSystemd, nil)
	}
	if _, err := checkSchedule(ctx, spec.Schedule, 1); err != nil {
		return nil, err
	}
	changes, err := c.render(spec)
	if err != nil {
		return nil, err
	}
	// The other type's files go when a task changes type.
	for _, p := range []string{cronScript(spec.ID), cronEnv(spec.ID)} {
		found := false
		for _, ch := range changes {
			found = found || ch.path == p
		}
		if !found {
			changes = append(changes, fileChange{path: p})
		}
	}
	if err := os.MkdirAll(c.Sites.path(cronEtcDir), 0o755); err != nil { //nolint:gosec // G301: scripts are read by their users.
		return nil, err
	}
	checks := []configCheck{{name: "systemd-analyze", argv: []string{"systemd-analyze", "verify", cronService(spec.ID), cronTimer(spec.ID)}}}
	if _, err := c.Sites.transact(ctx, log, changes, checks, nil); err != nil {
		if he, ok := helperapi.AsError(err); ok && he.Code == helperapi.CodeSiteConfigInvalid {
			return nil, cronErr(helperapi.CodeCronBadSchedule, he.Params)
		}
		return nil, err
	}
	if spec.Type == helperapi.CronShell {
		// The script is its user's to read and run, not to change.
		if e, _, err := c.shellUser(spec.User); err == nil {
			_ = os.Lchown(c.Sites.path(cronScript(spec.ID)), 0, e.gid)
		}
	}
	if err := runCommand(ctx, log, "systemctl", "daemon-reload"); err != nil {
		return nil, err
	}
	timer := cronUnit(spec.ID) + ".timer"
	if spec.Enabled {
		err = runCommand(ctx, log, "systemctl", "enable", "--now", timer)
	} else {
		_ = runCommand(ctx, log, "systemctl", "disable", "--now", timer)
	}
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(spec)
	if err := os.MkdirAll(c.stateDir(), 0o700); err != nil {
		return nil, err
	}
	return nil, writeFileMode(c.stateDir(spec.ID+".json"), data, 0o600)
}

func (c *Cron) spec(id string) (helperapi.CronSpec, error) {
	var s helperapi.CronSpec
	data, err := os.ReadFile(c.stateDir(id + ".json")) //nolint:gosec // G304: a validated task id.
	if err != nil || json.Unmarshal(data, &s) != nil || s.ID != id {
		return s, cronErr(helperapi.CodeCronNotFound, map[string]any{"id": id})
	}
	return s, nil
}

func (c *Cron) remove(ctx context.Context, _ helperapi.Caller, req helperapi.CronRef, log io.Writer) (any, error) {
	if _, err := c.spec(req.ID); err != nil {
		return nil, err
	}
	if systemdRunning() {
		_ = runCommand(ctx, log, "systemctl", "disable", "--now", cronUnit(req.ID)+".timer")
		_ = runCommand(ctx, log, "systemctl", "stop", cronUnit(req.ID)+".service")
	}
	changes := []fileChange{{path: cronService(req.ID)}, {path: cronTimer(req.ID)}, {path: cronScript(req.ID)}, {path: cronEnv(req.ID)}}
	if _, err := c.Sites.transact(ctx, log, changes, nil, nil); err != nil {
		return nil, err
	}
	if systemdRunning() {
		_ = runCommand(ctx, log, "systemctl", "daemon-reload")
	}
	_ = os.Remove(c.stateDir("runs", req.ID+".jsonl"))
	return nil, os.Remove(c.stateDir(req.ID + ".json"))
}

func (c *Cron) runNow(ctx context.Context, _ helperapi.Caller, req helperapi.CronRef, log io.Writer) (any, error) {
	if _, err := c.spec(req.ID); err != nil {
		return nil, err
	}
	if !systemdRunning() {
		return nil, cronErr(helperapi.CodeCronSystemd, nil)
	}
	_ = os.MkdirAll(c.stateDir(), 0o700)
	_ = os.WriteFile(c.stateDir(req.ID+".manual"), nil, 0o600)
	return nil, runCommand(ctx, log, "systemctl", "start", "--no-block", cronUnit(req.ID)+".service")
}

// Status ---------------------------------------------------------------------

func (c *Cron) specs() []helperapi.CronSpec {
	entries, _ := os.ReadDir(c.stateDir())
	var out []helperapi.CronSpec
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !helperapi.ValidCronID(id) {
			continue
		}
		if s, err := c.spec(id); err == nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (c *Cron) status(ctx context.Context) helperapi.CronStatus {
	st := helperapi.CronStatus{Systemd: systemdRunning(), Tasks: map[string]helperapi.CronState{}}
	specs := c.specs()
	active := map[string]string{}
	if st.Systemd && len(specs) > 0 {
		args := []string{"show", "--property=Id,ActiveState"}
		for _, s := range specs {
			args = append(args, cronUnit(s.ID)+".service")
		}
		if out, err := queryCommand(ctx, "systemctl", args...); err == nil {
			var unit string
			for _, line := range strings.Split(out, "\n") {
				k, v, _ := strings.Cut(line, "=")
				switch k {
				case "Id":
					unit = v
				case "ActiveState":
					active[unit] = v
				}
			}
		}
	}
	next := map[string]int64{}
	for _, s := range specs {
		state := helperapi.CronState{Spec: s, Running: active[cronUnit(s.ID)+".service"] == "activating"}
		if s.Enabled && st.Systemd {
			if n, ok := next[s.Schedule]; ok {
				state.NextRun = n
			} else if info, err := checkSchedule(ctx, s.Schedule, 1); err == nil && len(info.Next) > 0 {
				state.NextRun, next[s.Schedule] = info.Next[0], info.Next[0]
			}
		}
		if runs := c.runs(s.ID); len(runs) > 0 {
			state.LastRun, state.Result = runs[0].Started, runs[0].Result
		}
		st.Tasks[s.ID] = state
	}
	return st
}

// Runs ------------------------------------------------------------------------

func (c *Cron) runs(id string) []helperapi.CronRun {
	data, _ := os.ReadFile(c.stateDir("runs", id+".jsonl")) //nolint:gosec // G304: a validated task id.
	out := []helperapi.CronRun{}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var r helperapi.CronRun
		if json.Unmarshal([]byte(lines[i]), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

func (c *Cron) saveRuns(id string, runs []helperapi.CronRun) error {
	if len(runs) > cronRunsKept {
		runs = runs[:cronRunsKept]
	}
	var b strings.Builder
	for i := len(runs) - 1; i >= 0; i-- {
		line, _ := json.Marshal(runs[i])
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(c.stateDir("runs"), 0o700); err != nil {
		return err
	}
	return writeFileMode(c.stateDir("runs", id+".jsonl"), []byte(b.String()), 0o600)
}

func (c *Cron) log(ctx context.Context, _ helperapi.Caller, req helperapi.CronLogRequest) (helperapi.CronLog, error) {
	out := helperapi.CronLog{Lines: []string{}}
	if !hasJournal(c.Files.Root) {
		return out, fileErr(helperapi.CodeLogNoJournal, nil)
	}
	text, err := queryCommand(ctx, "journalctl", "--no-pager", "--output=cat", "--lines=2000", "_SYSTEMD_INVOCATION_ID="+req.Invocation)
	if err != nil {
		return out, fileErr(helperapi.CodeOpFailed, map[string]any{"detail": errDetail(err)})
	}
	out.Lines = strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(out.Lines) == 1 && out.Lines[0] == "" {
		out.Lines = []string{}
	}
	out.Truncated = len(out.Lines) >= 2000
	return out, nil
}

// The task's own process (cairn-helper task ...) --------------------------------

// TaskStart records a run's start; it refuses a root shell task while
// root access is off.
func (c *Cron) TaskStart(id string) error {
	spec, err := c.spec(id)
	if err != nil {
		return err
	}
	if spec.Type == helperapi.CronShell && spec.User == "root" && !c.Files.access().RootTerminal {
		return errors.New("root tasks are switched off: allow root terminals to run them")
	}
	manual := false
	if err := os.Remove(c.stateDir(id + ".manual")); err == nil {
		manual = true
	}
	run := helperapi.CronRun{Invocation: os.Getenv("INVOCATION_ID"), Started: time.Now().Unix(), Result: "running", Manual: manual}
	return c.saveRuns(id, append([]helperapi.CronRun{run}, c.runs(id)...))
}

// TaskDone records how a run ended (systemd's SERVICE_RESULT and
// EXIT_STATUS).
func (c *Cron) TaskDone(id string) error {
	runs := c.runs(id)
	inv := os.Getenv("INVOCATION_ID")
	i := 0
	for j, r := range runs {
		if r.Invocation == inv {
			i = j
			break
		}
	}
	if len(runs) == 0 {
		runs = []helperapi.CronRun{{Invocation: inv, Started: time.Now().Unix()}}
	}
	r := &runs[i]
	r.Finished = time.Now().Unix()
	switch os.Getenv("SERVICE_RESULT") {
	case "success":
		r.Result = "success"
	case "timeout":
		r.Result = "timeout"
	default:
		r.Result = "failed"
	}
	r.ExitCode, _ = strconv.Atoi(os.Getenv("EXIT_STATUS"))
	return c.saveRuns(id, runs)
}

// TaskURL fetches $CAIRN_URL; an HTTP error fails the run.
func TaskURL(ctx context.Context, out io.Writer) error {
	u := os.Getenv("CAIRN_URL")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil) //nolint:gosec // G704: the task's own URL, set by an administrator.
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Cairn")
	res, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req) //nolint:gosec // G704: as above.
	if err != nil {
		return err
	}
	defer res.Body.Close()
	n, _ := io.Copy(io.Discard, io.LimitReader(res.Body, 64<<20))
	_, _ = fmt.Fprintf(out, "%s: %s (%d bytes)\n", req.URL.Redacted(), res.Status, n)
	if res.StatusCode >= 400 {
		return fmt.Errorf("%s", res.Status)
	}
	return nil
}

// TaskBackup runs a backup task's plan.
func (c *Cron) TaskBackup(ctx context.Context, id string, out io.Writer) error {
	spec, err := c.spec(id)
	if err != nil {
		return err
	}
	if spec.Type != helperapi.CronBackup {
		return fmt.Errorf("task %s is not a backup", id)
	}
	_, err = c.Backups.Run(ctx, spec.Plan, out)
	return err
}
