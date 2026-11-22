// SPDX-License-Identifier: AGPL-3.0-only

// Package cron manages scheduled tasks: shell scripts (run as a site owner
// or an ordinary user, root only while root access is on), URLs to fetch
// and backup plans to run, each a systemd service and timer applied by the
// root helper. The panel keeps the tasks; the services record their own
// runs, which the panel lists and turns into notifications on failure.
package cron

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Permissions of this module.
const (
	Read   authz.Permission = "cron.read"
	Manage authz.Permission = "cron.manage"
	// Root creates, changes, runs and deletes shell tasks that run as root
	// or as a user that is not a site owner (on cloud images the default
	// user can sudo without a password) (administrators).
	Root authz.Permission = "cron.root"
)

// Task states.
const (
	StateApplying = "applying"
	StateActive   = "active"
	StateFailed   = "failed"
)

const checkEvery = time.Minute

// Module implements module.Module.
type Module struct {
	Jobs   *jobs.Manager
	Helper jobs.HelperClient
	DB     *sql.DB
	Events *events.Bus
	Log    *slog.Logger
}

// ID implements module.Module.
func (m *Module) ID() string { return "cron" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{
		Optional: true,
		Permissions: []module.Permission{
			{Name: Read, Roles: []authz.Role{authz.Operator, authz.Auditor}},
			{Name: Manage, Roles: []authz.Role{authz.Operator}},
			{Name: Root},
		},
		Nav: []module.NavItem{
			{Key: "cron", LabelKey: "common.nav.cron", Icon: "time", Route: "cron", Permission: Read, Group: module.GroupOps, Order: 21},
		},
	}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	doc := func(s string) api.RouteOption { return api.Doc("cron", s) }
	job := api.Returns(202, jobResponse{})
	r.Handle("GET /api/v1/cron", m.list, api.Require(Read), doc("Scheduled tasks with their next and last runs"), api.Returns(200, listResponse{}))
	r.Handle("GET /api/v1/cron/users", m.users, api.Require(Manage), doc("Who a shell task can run as (root only for those allowed)"),
		api.Returns(200, helperapi.TermUsers{}))
	r.Handle("GET /api/v1/cron/schedule", m.schedule, api.Require(Read), doc("How systemd reads a schedule: ?expr= (OnCalendar)"),
		api.Returns(200, helperapi.ScheduleInfo{}))
	r.Handle("POST /api/v1/cron", m.create, api.Require(Manage), api.Body(taskRequest{}), doc("Create a task (starts a job)"), job)
	r.Handle("PUT /api/v1/cron/{id}", m.update, api.Require(Manage), api.Body(taskRequest{}), doc("Change a task (starts a job)"), job)
	r.Handle("DELETE /api/v1/cron/{id}", m.remove, api.Require(Manage), doc("Delete a task (starts a job)"), job)
	r.Handle("POST /api/v1/cron/{id}/run", m.run, api.Require(Manage), api.NoBody(), doc("Run a task now (starts a job)"), job)
	r.Handle("GET /api/v1/cron/{id}/runs", m.runs, api.Require(Read), doc("A task's runs, newest first"), api.Returns(200, helperapi.CronRuns{}))
	r.Handle("GET /api/v1/cron/{id}/runs/{invocation}/log", m.runLog, api.Require(Read),
		doc("A run's output; that of a task running as root or a user other than a site owner needs cron.root"), api.Returns(200, helperapi.CronLog{}))
}

type taskRequest struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Schedule   string `json:"schedule"`
	User       string `json:"user,omitempty"`
	Script     string `json:"script,omitempty"`
	URL        string `json:"url,omitempty"`
	Plan       string `json:"plan,omitempty"`
	TimeoutMin int    `json:"timeoutMin,omitempty"`
	Enabled    bool   `json:"enabled"`
}

type taskView struct {
	helperapi.CronSpec
	Name      string `json:"name"`
	State     string `json:"state"`
	ErrorCode string `json:"errorCode,omitempty"`
	LastJob   string `json:"lastJob,omitempty"`
	NextRun   int64  `json:"nextRun,omitempty"`
	LastRun   int64  `json:"lastRun,omitempty"`
	Running   bool   `json:"running"`
	Result    string `json:"result,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type listResponse struct {
	Systemd         bool       `json:"systemd"`
	StatusAvailable bool       `json:"statusAvailable"`
	Tasks           []taskView `json:"tasks"`
}

type jobResponse struct {
	Job jobs.Record `json:"job"`
}

func callerOf(r *http.Request) helperapi.Caller {
	who := modutil.ActorOf(r)
	return helperapi.Caller{UserID: who.UserID, Username: who.Username, Remote: api.ClientIP(r), Source: who.Source}
}

func helperError(err error) error {
	if he, ok := helperapi.AsError(err); ok {
		status := http.StatusConflict
		if he.Code == helperapi.CodeCronNotFound {
			status = http.StatusNotFound
		}
		return api.NewError(status, he.Code, he.Params)
	}
	return err
}

type row struct {
	name                 string
	spec                 helperapi.CronSpec
	state, code, lastJob string
	notified             int64
	created, updated     int64
}

const selectTask = `SELECT name, spec, state, error_code, last_job, notified_run, created_at, updated_at FROM cron_tasks`

func scan(sc interface{ Scan(...any) error }) (row, error) {
	var rw row
	var spec string
	if err := sc.Scan(&rw.name, &spec, &rw.state, &rw.code, &rw.lastJob, &rw.notified, &rw.created, &rw.updated); err != nil {
		return rw, err
	}
	return rw, json.Unmarshal([]byte(spec), &rw.spec)
}

func (m *Module) load(ctx context.Context, id string) (row, error) {
	rw, err := scan(m.DB.QueryRowContext(ctx, selectTask+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return rw, api.NewError(http.StatusNotFound, helperapi.CodeCronNotFound, map[string]any{"id": id})
	}
	return rw, err
}

func (m *Module) all(ctx context.Context) ([]row, error) {
	rows, err := m.DB.QueryContext(ctx, selectTask+` ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		rw, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rw)
	}
	return out, rows.Err()
}

func (m *Module) status(ctx context.Context, r *http.Request) (helperapi.CronStatus, bool) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	caller := helperapi.Caller{Source: "system", Username: "cairn"}
	if r != nil {
		caller = callerOf(r)
	}
	var st helperapi.CronStatus
	if err := m.Helper.Call(ctx, helperapi.ActionCronStatus, caller, nil, &st); err != nil {
		m.Log.Warn("task status unavailable", "err", err)
		return st, false
	}
	return st, true
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	st, known := m.status(r.Context(), r)
	rows, err := m.all(r.Context())
	if err != nil {
		return err
	}
	resp := listResponse{Systemd: st.Systemd || !known, StatusAvailable: known, Tasks: []taskView{}}
	// Scripts and URLs can hold passwords and keys: they show only to those
	// who may change the task.
	p, ok := api.PrincipalFrom(r.Context())
	manage, root := ok && p.Can(Manage), ok && p.Can(Root)
	var users *helperapi.TermUsers
	if manage && !root {
		if list, err := m.termUsers(r); err == nil {
			users = &list
		} // without the list every shell task counts as privileged
	}
	for _, rw := range rows {
		v := taskView{CronSpec: rw.spec, Name: rw.name, State: rw.state, ErrorCode: rw.code, LastJob: rw.lastJob, CreatedAt: rw.created, UpdatedAt: rw.updated}
		if !manage {
			v.Script, v.URL = "", ""
		} else if !root && v.Type == helperapi.CronShell && (users == nil || runAs(*users, rw.spec) != helperapi.TermUserSite) {
			v.Script = ""
		}
		if s, ok := st.Tasks[rw.spec.ID]; ok {
			v.NextRun, v.LastRun, v.Running, v.Result = s.NextRun, s.LastRun, s.Running, s.Result
		}
		resp.Tasks = append(resp.Tasks, v)
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) termUsers(r *http.Request) (helperapi.TermUsers, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var resp helperapi.TermUsers
	if err := m.Helper.Call(ctx, helperapi.ActionTermUsers, callerOf(r), nil, &resp); err != nil {
		return resp, helperError(err)
	}
	return resp, nil
}

func (m *Module) users(w http.ResponseWriter, r *http.Request) error {
	resp, err := m.termUsers(r)
	if err != nil {
		return err
	}
	// Others than site owners are for those with the Root permission.
	if p, ok := api.PrincipalFrom(r.Context()); !ok || !p.Can(Root) {
		users := resp.Users[:0]
		for _, u := range resp.Users {
			if u.Kind == helperapi.TermUserSite {
				users = append(users, u)
			}
		}
		resp.Users = users
	}
	if resp.Users == nil {
		resp.Users = []helperapi.TermUser{}
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) schedule(w http.ResponseWriter, r *http.Request) error {
	req := helperapi.ScheduleRequest{Schedule: r.URL.Query().Get("expr")}
	if err := req.Validate(); err != nil {
		var he *helperapi.Error
		if errors.As(err, &he) {
			return api.NewError(http.StatusBadRequest, he.Code, he.Params)
		}
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var info helperapi.ScheduleInfo
	if err := m.Helper.Call(ctx, helperapi.ActionCronSchedule, callerOf(r), req, &info); err != nil {
		if he, ok := helperapi.AsError(err); ok && he.Code == helperapi.CodeCronBadSchedule {
			return api.NewError(http.StatusBadRequest, he.Code, he.Params)
		}
		return helperError(err)
	}
	return api.WriteJSON(w, http.StatusOK, info)
}

// runAs is the kind (helperapi.TermUser*) of who a task runs as, as the
// helper lists them ("" for a user it does not list, such as the owner of
// a deleted site); tasks that run no shell count as a site owner's.
func runAs(users helperapi.TermUsers, s helperapi.CronSpec) string {
	if s.Type != helperapi.CronShell {
		return helperapi.TermUserSite
	}
	if s.User == helperapi.TermUserRoot {
		return helperapi.TermUserRoot
	}
	for _, u := range users.Users {
		if u.Name == s.User {
			return u.Kind
		}
	}
	return ""
}

// kindOf is runAs with the helper's current list.
func (m *Module) kindOf(r *http.Request, s helperapi.CronSpec) (string, error) {
	if s.Type != helperapi.CronShell || s.User == helperapi.TermUserRoot {
		return runAs(helperapi.TermUsers{}, s), nil
	}
	users, err := m.termUsers(r)
	if err != nil {
		return "", err
	}
	return runAs(users, s), nil
}

// checkPrivileged refuses a privileged task (one that runs as root or as
// a user that is not a site owner; the helper checks the name against the
// same list again when it applies the task) to callers without Root; with
// stepUp, it also needs a second verification. Unknown users count as
// privileged, unless unknownOK (deleting a task of a deleted site's owner
// stops something, it starts nothing).
func (m *Module) checkPrivileged(r *http.Request, s helperapi.CronSpec, stepUp, unknownOK bool) error {
	kind, err := m.kindOf(r, s)
	if err != nil || kind == helperapi.TermUserSite || kind == "" && unknownOK {
		return err
	}
	p, ok := api.PrincipalFrom(r.Context())
	if !ok || !p.Can(Root) {
		return api.ErrForbidden
	}
	if stepUp && p.ElevatedUntil <= time.Now().Unix() {
		return api.ErrStepUpRequired
	}
	return nil
}

// spec checks a request; privileged tasks (see privileged), before and
// after the change, need the Root permission and a second verification.
func (m *Module) spec(r *http.Request, req taskRequest, id string, was *helperapi.CronSpec) (helperapi.CronSpec, string, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > 64 || strings.ContainsAny(name, "\x00\r\n") {
		return helperapi.CronSpec{}, "", api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "name", "rule": "max"})
	}
	s := helperapi.CronSpec{ID: id, Type: req.Type, Schedule: strings.TrimSpace(req.Schedule), User: req.User, Script: req.Script, URL: req.URL,
		Plan: req.Plan, TimeoutMin: req.TimeoutMin, Enabled: req.Enabled}
	if err := s.Validate(); err != nil {
		var he *helperapi.Error
		if errors.As(err, &he) {
			return s, "", api.NewError(http.StatusBadRequest, he.Code, he.Params)
		}
		return s, "", err
	}
	if s.Type == helperapi.CronBackup {
		var n int
		_ = m.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM backup_plans WHERE id = ?`, s.Plan).Scan(&n)
		if n == 0 {
			return s, "", planNotFound(s.Plan)
		}
	}
	if was != nil {
		if err := m.checkPrivileged(r, *was, true, false); err != nil {
			return s, "", err
		}
	}
	if err := m.checkPrivileged(r, s, true, false); err != nil {
		return s, "", err
	}
	return s, name, nil
}

func planNotFound(plan string) error {
	return api.NewError(http.StatusBadRequest, helperapi.CodeCronPlanNotFound, map[string]any{"plan": plan})
}

// planGuard ends the statements that store a task: a backup task is stored
// only while its plan exists, in the same statement, so a plan deleted
// meanwhile (backups module, which checks the other way round) is never
// left with a task. Its arguments are the task's type and plan.
const planGuard = `(? != 'backup' OR EXISTS (SELECT 1 FROM backup_plans WHERE id = ?))`

// stored reports a statement guarded by planGuard that changed nothing.
func stored(res sql.Result, s helperapi.CronSpec) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return planNotFound(s.Plan)
	}
	return nil
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "t" + hex.EncodeToString(b)[:15]
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) error {
	var req taskRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	s, name, err := m.spec(r, req, newID(), nil)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(s)
	now := time.Now().Unix()
	res, err := m.DB.ExecContext(r.Context(), `INSERT INTO cron_tasks (id, name, spec, state, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ? WHERE `+planGuard, s.ID, name, string(data), StateApplying, now, now, s.Type, s.Plan)
	if err != nil {
		return err
	}
	if err := stored(res, s); err != nil {
		return err
	}
	return m.applyJob(w, r, "cron.create", s, name)
}

func (m *Module) update(w http.ResponseWriter, r *http.Request) error {
	var req taskRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	rw, err := m.load(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	s, name, err := m.spec(r, req, rw.spec.ID, &rw.spec)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(s)
	res, err := m.DB.ExecContext(r.Context(), `UPDATE cron_tasks SET name = ?, spec = ?, state = ?, error_code = '', updated_at = ?
		WHERE id = ? AND `+planGuard, name, string(data), StateApplying, time.Now().Unix(), s.ID, s.Type, s.Plan)
	if err != nil {
		return err
	}
	if err := stored(res, s); err != nil {
		return err
	}
	return m.applyJob(w, r, "cron.update", s, name)
}

func (m *Module) start(r *http.Request, kind string, params map[string]any, fn jobs.Func) (jobs.Record, error) {
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: kind, Params: params, Permission: Read, ManagePermission: Manage, Locks: []string{"cron"},
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, fn)
	if err != nil {
		return job, err
	}
	audit := map[string]any{"job": job.ID}
	for k, v := range params {
		audit[k] = v
	}
	modutil.Audit(r, m.DB, m.Log, who, kind, "ok", audit)
	return job, nil
}

func (m *Module) applyJob(w http.ResponseWriter, r *http.Request, kind string, s helperapi.CronSpec, name string) error {
	params := map[string]any{"task": s.ID, "name": name, "type": s.Type, "schedule": s.Schedule, "enabled": s.Enabled}
	if s.Type == helperapi.CronShell {
		params["user"] = s.User
	}
	job, err := m.start(r, kind, params, func(ctx context.Context, j *jobs.Job) error {
		err := j.RunOp(helperapi.ActionCronApply, s, nil)
		state, code := StateActive, ""
		if err != nil {
			state = StateFailed
			if he, ok := helperapi.AsError(err); ok {
				code = he.Code
			} else {
				code = api.ErrInternal.Code
			}
		}
		if _, dbErr := m.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE cron_tasks SET state = ?, error_code = ?, last_job = ?, updated_at = ? WHERE id = ?`,
			state, code, j.ID(), time.Now().Unix(), s.ID); dbErr != nil {
			m.Log.Error("saving task state failed", "task", s.ID, "err", dbErr)
		}
		return err
	})
	if err != nil {
		// No job will finish it: it does not stay applying.
		if _, dbErr := m.DB.ExecContext(context.WithoutCancel(r.Context()), `UPDATE cron_tasks SET state = ?, error_code = ?, updated_at = ? WHERE id = ?`,
			StateFailed, api.ErrInternal.Code, time.Now().Unix(), s.ID); dbErr != nil {
			m.Log.Error("saving task state failed", "task", s.ID, "err", dbErr)
		}
		return err
	}
	_, _ = m.DB.ExecContext(r.Context(), `UPDATE cron_tasks SET last_job = ? WHERE id = ?`, job.ID, s.ID)
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) remove(w http.ResponseWriter, r *http.Request) error {
	rw, err := m.load(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := m.checkPrivileged(r, rw.spec, false, true); err != nil {
		return err
	}
	id := rw.spec.ID
	job, err := m.start(r, "cron.delete", map[string]any{"task": id, "name": rw.name}, func(ctx context.Context, j *jobs.Job) error {
		err := j.RunOp(helperapi.ActionCronRemove, helperapi.CronRef{ID: id}, nil)
		if he, ok := helperapi.AsError(err); ok && he.Code == helperapi.CodeCronNotFound {
			err = nil
		}
		if err != nil {
			return err
		}
		_, err = m.DB.ExecContext(context.WithoutCancel(ctx), `DELETE FROM cron_tasks WHERE id = ?`, id)
		return err
	})
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) run(w http.ResponseWriter, r *http.Request) error {
	rw, err := m.load(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	// Running a root task is running a root script now.
	if err := m.checkPrivileged(r, rw.spec, false, false); err != nil {
		return err
	}
	id := rw.spec.ID
	job, err := m.start(r, "cron.run", map[string]any{"task": id, "name": rw.name}, func(_ context.Context, j *jobs.Job) error {
		if err := j.RunOp(helperapi.ActionCronRun, helperapi.CronRef{ID: id}, nil); err != nil {
			return err
		}
		j.Logf("Started; the run's output is in the task's history.\n")
		return nil
	})
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) runs(w http.ResponseWriter, r *http.Request) error {
	rw, err := m.load(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var resp helperapi.CronRuns
	if err := m.Helper.Call(ctx, helperapi.ActionCronRuns, callerOf(r), helperapi.CronRef{ID: rw.spec.ID}, &resp); err != nil {
		return helperError(err)
	}
	if resp.Runs == nil {
		resp.Runs = []helperapi.CronRun{}
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) runLog(w http.ResponseWriter, r *http.Request) error {
	rw, err := m.load(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	req := helperapi.CronLogRequest{ID: rw.spec.ID, Invocation: r.PathValue("invocation")}
	if err := req.Validate(); err != nil {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "invocation", "rule": "invocation_id"})
	}
	// What a privileged task prints (as root or another user than a site
	// owner) is for those who may change it.
	if err := m.checkPrivileged(r, rw.spec, false, false); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var resp helperapi.CronLog
	if err := m.Helper.Call(ctx, helperapi.ActionCronLog, callerOf(r), req, &resp); err != nil {
		return helperError(err)
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

// Recover marks the tasks left applying by a job that no longer runs (the
// web process stopped while applying them) as failed, so they can be saved
// again. Call it after the job manager marked such jobs interrupted.
func (m *Module) Recover(ctx context.Context) error {
	res, err := m.DB.ExecContext(ctx, `UPDATE cron_tasks SET state = ?, error_code = ?, updated_at = ? WHERE state = ?
		AND NOT EXISTS (SELECT 1 FROM jobs WHERE jobs.id = cron_tasks.last_job AND jobs.state IN (?, ?))`,
		StateFailed, jobs.CodeInterrupted, time.Now().Unix(), StateApplying, jobs.Queued, jobs.Running)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		m.Log.Warn("task changes interrupted by a restart marked failed", "count", n)
	}
	return nil
}

// Run reports failed runs as they appear.
func (m *Module) Run(ctx context.Context) {
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Check(ctx)
		}
	}
}

// Check publishes cron.failed for each task whose last run failed and was
// not reported yet.
func (m *Module) Check(ctx context.Context) {
	rows, err := m.all(ctx)
	if err != nil || len(rows) == 0 {
		return
	}
	st, ok := m.status(ctx, nil)
	if !ok {
		return
	}
	for _, rw := range rows {
		s, ok := st.Tasks[rw.spec.ID]
		if !ok || s.LastRun <= rw.notified || (s.Result != "failed" && s.Result != "timeout") {
			continue
		}
		var exit int
		var runs helperapi.CronRuns
		if m.Helper.Call(ctx, helperapi.ActionCronRuns, helperapi.Caller{Source: "system", Username: "cairn"}, helperapi.CronRef{ID: rw.spec.ID}, &runs) == nil && len(runs.Runs) > 0 {
			exit = runs.Runs[0].ExitCode
		}
		if _, err := m.DB.ExecContext(ctx, `UPDATE cron_tasks SET notified_run = ? WHERE id = ?`, s.LastRun, rw.spec.ID); err != nil {
			continue
		}
		m.Events.Publish(events.Event{Type: events.CronFailed, Level: events.Warning, DedupeKey: "cron-failed:" + rw.spec.ID + ":" + time.Unix(s.LastRun, 0).UTC().Format(time.RFC3339),
			Params: map[string]any{"task": rw.spec.ID, "name": rw.name, "result": s.Result, "exitCode": exit}})
	}
}
