// SPDX-License-Identifier: AGPL-3.0-only

// Package jobs runs user-visible background tasks in the web process. A job
// is a Go function; its privileged steps start helper operations and follow
// them (Job.RunOp), so long work such as installing packages survives a
// restart of the web process on the helper side. Records live in SQLite and
// logs in <data dir>/jobs/<id>.log. Clients follow a job with long polling.
package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/audit"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/locks"
)

// Job states, the same as helper operation states.
const (
	Queued      = helperapi.OpQueued
	Running     = helperapi.OpRunning
	Succeeded   = helperapi.OpSucceeded
	Failed      = helperapi.OpFailed
	Canceled    = helperapi.OpCanceled
	Interrupted = helperapi.OpInterrupted
)

// Error codes.
const (
	CodeNotCancelable = "job.not_cancelable"
	CodeInterrupted   = "job.interrupted"
	CodeCanceled      = "job.canceled"
)

// EventFailed is published when a job fails, so notification channels can
// report it.
const EventFailed = "system.job_failed"

const (
	maxLog       = 4 << 20
	maxLogChunk  = 256 << 10
	retention    = 30 * 24 * time.Hour
	opPollPeriod = 300 * time.Millisecond
	progressGap  = time.Second
)

// Done reports whether state is final.
func Done(state string) bool { return helperapi.OpDone(state) }

// ErrorInfo is a structured failure.
type ErrorInfo struct {
	Code   string         `json:"code"`
	Params map[string]any `json:"params,omitempty"`
}

// Record is a job as stored and as shown to clients.
type Record struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Params     map[string]any `json:"params"`
	State      string         `json:"state"`
	Progress   *float64       `json:"progress"`
	Error      *ErrorInfo     `json:"error,omitempty"`
	Cancelable bool           `json:"cancelable"`
	Username   string         `json:"username"`
	Source     string         `json:"source"`
	CreatedAt  int64          `json:"createdAt"`
	StartedAt  int64          `json:"startedAt"`
	FinishedAt int64          `json:"finishedAt"`

	UserID           int64            `json:"-"`
	Remote           string           `json:"-"`
	OpID             string           `json:"-"`
	Permission       authz.Permission `json:"-"`
	ManagePermission authz.Permission `json:"-"`
}

// Actor is who started a job (empty for jobs the panel starts itself).
type Actor struct {
	UserID   int64
	Username string
	Source   string // web | api | system
	Remote   string
}

// Spec describes a job to start.
type Spec struct {
	// Kind identifies the job type, such as "software.refresh"; the UI
	// translates it.
	Kind string
	// Params are shown in the UI and the audit log; never put secrets here.
	Params map[string]any
	// Permission is needed to see the job; ManagePermission to cancel it.
	Permission       authz.Permission
	ManagePermission authz.Permission
	// Locks are web-side lock names; the job waits until all are free.
	Locks []string
	Actor Actor
}

// Func is a job's body. Returning an *api.Error or *helperapi.Error fails the
// job with that code; context.Canceled (after a cancel request) cancels it;
// any other error is logged and reported as internal.error.
type Func func(ctx context.Context, j *Job) error

// HelperClient is the part of the helper client jobs need.
type HelperClient interface {
	Call(ctx context.Context, action string, caller helperapi.Caller, req, resp any) error
}

// Manager starts, tracks and persists jobs.
type Manager struct {
	DB     *sql.DB
	Dir    string // log directory
	Helper HelperClient
	Events *events.Bus
	Log    *slog.Logger

	locks locks.Set
	mu    sync.Mutex
	live  map[string]*Job
	watch map[string]chan struct{}
	now   func() time.Time
}

// Init prepares the log directory, marks jobs that were unfinished when the
// web process stopped as interrupted, and prunes old jobs.
func (m *Manager) Init(ctx context.Context) error {
	m.live, m.watch, m.now = map[string]*Job{}, map[string]chan struct{}{}, time.Now
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return err
	}
	_, err := m.DB.ExecContext(ctx, `UPDATE jobs SET state = ?, error_code = ?, finished_at = ?
		WHERE state IN (?, ?)`, Interrupted, CodeInterrupted, m.now().Unix(), Queued, Running)
	if err != nil {
		return err
	}
	m.prune(ctx)
	return nil
}

// RunPruner removes old jobs once a day until ctx ends.
func (m *Manager) RunPruner(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.prune(ctx)
		}
	}
}

func (m *Manager) prune(ctx context.Context) {
	cutoff := m.now().Add(-retention).Unix()
	rows, err := m.DB.QueryContext(ctx, `SELECT id FROM jobs WHERE created_at < ? AND state NOT IN (?, ?)`, cutoff, Queued, Running)
	if err != nil {
		m.Log.Warn("pruning jobs failed", "err", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if _, err := m.DB.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id); err == nil {
			_ = os.Remove(m.logPath(id))
		}
	}
}

var idPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)

// ValidID reports whether s has the shape of a job ID.
func ValidID(s string) bool { return idPattern.MatchString(s) }

func (m *Manager) logPath(id string) string { return filepath.Join(m.Dir, id+".log") }

// Start records and launches a job. It returns as soon as the job is queued.
func (m *Manager) Start(ctx context.Context, spec Spec, fn Func) (Record, error) {
	if spec.Kind == "" || spec.Permission == "" || spec.ManagePermission == "" {
		return Record{}, errors.New("jobs: kind and permissions are required")
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	src := spec.Actor.Source
	if src == "" {
		src = "system"
	}
	rec := Record{
		ID: hex.EncodeToString(b), Kind: spec.Kind, Params: spec.Params, State: Queued, Cancelable: true,
		Username: spec.Actor.Username, Source: src, CreatedAt: m.now().Unix(),
		UserID: spec.Actor.UserID, Remote: spec.Actor.Remote,
		Permission: spec.Permission, ManagePermission: spec.ManagePermission,
	}
	if rec.Params == nil {
		rec.Params = map[string]any{}
	}
	params, err := json.Marshal(rec.Params)
	if err != nil {
		return Record{}, err
	}
	if err := os.WriteFile(m.logPath(rec.ID), nil, 0o600); err != nil {
		return Record{}, err
	}
	_, err = m.DB.ExecContext(ctx, `INSERT INTO jobs (id, kind, params, permission, manage_permission, state,
		user_id, username, source, remote, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Kind, string(params), string(rec.Permission), string(rec.ManagePermission), rec.State,
		rec.UserID, rec.Username, rec.Source, rec.Remote, rec.CreatedAt)
	if err != nil {
		return Record{}, err
	}
	jctx, cancel := context.WithCancel(context.Background())
	j := &Job{m: m, rec: rec, ctx: jctx, cancel: cancel, cancelable: true}
	m.mu.Lock()
	m.live[rec.ID] = j
	m.mu.Unlock()
	go m.run(j, spec.Locks, fn)
	return rec, nil
}

func (m *Manager) run(j *Job, lockNames []string, fn Func) {
	defer j.cancel()
	defer func() {
		m.mu.Lock()
		delete(m.live, j.rec.ID)
		m.mu.Unlock()
		m.notify(j.rec.ID)
	}()
	f, err := os.OpenFile(m.logPath(j.rec.ID), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		m.finish(j, err)
		return
	}
	defer f.Close()
	j.log = &cappedLog{w: f, limit: maxLog, notify: func() { m.notify(j.rec.ID) }}

	release, err := m.locks.Acquire(j.ctx, lockNames, nil)
	if err != nil {
		m.finish(j, err)
		return
	}
	defer release()
	j.setState(Running)
	m.finish(j, safeRun(j, fn))
}

func safeRun(j *Job, fn Func) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("job panicked: %v", v)
		}
	}()
	return fn(j.ctx, j)
}

func (m *Manager) finish(j *Job, err error) {
	var ae *api.Error
	var he *helperapi.Error
	state, info := Succeeded, (*ErrorInfo)(nil)
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		state, info = Canceled, &ErrorInfo{Code: CodeCanceled}
	case errors.As(err, &ae):
		state, info = Failed, &ErrorInfo{Code: ae.Code, Params: ae.Params}
	case errors.As(err, &he):
		state, info = Failed, &ErrorInfo{Code: he.Code, Params: he.Params}
		if he.Code == helperapi.CodeOpCanceled {
			state, info = Canceled, &ErrorInfo{Code: CodeCanceled}
		}
	default:
		m.Log.Error("job failed", "id", j.rec.ID, "kind", j.rec.Kind, "err", err)
		state, info = Failed, &ErrorInfo{Code: api.ErrInternal.Code}
	}
	if info != nil {
		j.Logf("\n%s: %s\n", state, info.Code)
	}
	j.mu.Lock()
	j.rec.State, j.rec.Error, j.rec.Cancelable, j.rec.FinishedAt = state, info, false, m.now().Unix()
	rec := j.rec
	j.mu.Unlock()
	code, params := "", "{}"
	if info != nil {
		code = info.Code
		if b, err := json.Marshal(info.Params); err == nil && info.Params != nil {
			params = string(b)
		}
	}
	// The job's own context may be canceled; the record must still be saved.
	ctx := context.Background()
	if _, err := m.DB.ExecContext(ctx, `UPDATE jobs SET state = ?, error_code = ?, error_params = ?, finished_at = ? WHERE id = ?`,
		state, code, params, rec.FinishedAt, rec.ID); err != nil {
		m.Log.Error("saving job failed", "id", rec.ID, "err", err)
	}
	result := "ok"
	if info != nil {
		result = info.Code
	}
	if err := audit.Record(ctx, m.DB, audit.Entry{
		UserID: rec.UserID, Username: rec.Username, Action: "job.finish", Result: result,
		Params: map[string]any{"id": rec.ID, "kind": rec.Kind}, Remote: rec.Remote, Source: rec.Source,
	}); err != nil {
		m.Log.Error("audit write failed", "action", "job.finish", "err", err)
	}
	if state == Failed {
		m.Events.Publish(events.Event{
			Type: EventFailed, Level: events.Warning, DedupeKey: "job:" + rec.ID,
			Params: map[string]any{"id": rec.ID, "kind": rec.Kind, "code": result},
		})
	}
	m.Log.Info("job finished", "id", rec.ID, "kind", rec.Kind, "state", state, "result", result)
}

// notify wakes every client waiting on the job.
func (m *Manager) notify(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.watch[id]; ok {
		close(ch)
		delete(m.watch, id)
	}
}

// unwatch drops the watch entry of a job that is not running (finished,
// or no job at all): nothing would ever close and remove it. Waiters on
// the same entry are woken, and find the job done.
func (m *Manager) unwatch(id string, ch <-chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, live := m.live[id]; live {
		return
	}
	if cur, ok := m.watch[id]; ok && cur == ch {
		close(cur)
		delete(m.watch, id)
	}
}

func (m *Manager) changed(id string) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.watch[id]
	if !ok {
		ch = make(chan struct{})
		m.watch[id] = ch
	}
	return ch
}

const selectJob = `SELECT id, kind, params, permission, manage_permission, state, progress, error_code, error_params,
	op_id, user_id, username, source, remote, created_at, started_at, finished_at FROM jobs`

func scanJob(row interface{ Scan(...any) error }) (Record, error) {
	var r Record
	var params, errParams, perm, manage, code string
	var progress sql.NullFloat64
	err := row.Scan(&r.ID, &r.Kind, &params, &perm, &manage, &r.State, &progress, &code, &errParams,
		&r.OpID, &r.UserID, &r.Username, &r.Source, &r.Remote, &r.CreatedAt, &r.StartedAt, &r.FinishedAt)
	if err != nil {
		return r, err
	}
	r.Permission, r.ManagePermission = authz.Permission(perm), authz.Permission(manage)
	_ = json.Unmarshal([]byte(params), &r.Params)
	if r.Params == nil {
		r.Params = map[string]any{}
	}
	if progress.Valid {
		r.Progress = &progress.Float64
	}
	if code != "" {
		r.Error = &ErrorInfo{Code: code}
		_ = json.Unmarshal([]byte(errParams), &r.Error.Params)
	}
	return r, nil
}

// Get returns a job; live jobs report their current cancelability.
func (m *Manager) Get(ctx context.Context, id string) (Record, error) {
	if !ValidID(id) {
		return Record{}, api.ErrNotFound
	}
	r, err := scanJob(m.DB.QueryRowContext(ctx, selectJob+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, api.ErrNotFound
	}
	if err != nil {
		return r, err
	}
	m.mu.Lock()
	if j := m.live[id]; j != nil {
		j.mu.Lock()
		r.Cancelable = j.cancelable
		j.mu.Unlock()
	}
	m.mu.Unlock()
	return r, nil
}

// ListFilter selects jobs; Visible filters by permission.
type ListFilter struct {
	Limit int
	// Before is the cursor returned by the previous page; 0 = newest.
	Before  int64
	Visible func(Record) bool
}

// List returns visible jobs newest first, and the cursor for the next page
// (0 when there are no more). The cursor is the row's insertion sequence, so
// jobs created in the same second are neither skipped nor repeated.
func (m *Manager) List(ctx context.Context, f ListFilter) ([]Record, int64, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	cursor := f.Before
	if cursor <= 0 {
		cursor = 1 << 62
	}
	const batch = 200
	out := []Record{}
	for {
		rows, err := m.DB.QueryContext(ctx, strings.Replace(selectJob, "SELECT ", "SELECT rowid, ", 1)+
			` WHERE rowid < ? ORDER BY rowid DESC LIMIT ?`, cursor, batch)
		if err != nil {
			return nil, 0, err
		}
		n := 0
		for rows.Next() {
			n++
			var seq int64
			r, err := scanJob(rowScanner{rows: rows, extra: &seq})
			if err != nil {
				rows.Close()
				return nil, 0, err
			}
			cursor = seq
			if f.Visible == nil || f.Visible(r) {
				out = append(out, r)
				if len(out) == f.Limit {
					rows.Close()
					return out, cursor, nil
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, 0, err
		}
		if n < batch {
			return out, 0, nil
		}
	}
}

// rowScanner prepends one extra column (the rowid) to a job scan.
type rowScanner struct {
	rows  *sql.Rows
	extra *int64
}

func (r rowScanner) Scan(dest ...any) error {
	return r.rows.Scan(append([]any{r.extra}, dest...)...)
}

// LogChunk is a piece of a job's log.
type LogChunk struct {
	Log     string `json:"log"`
	Offset  int64  `json:"offset"`
	LogSize int64  `json:"logSize"`
}

// ReadLog returns up to 256 KiB of log from offset.
func (m *Manager) ReadLog(id string, offset int64) (LogChunk, error) {
	if !ValidID(id) {
		return LogChunk{}, api.ErrNotFound
	}
	f, err := os.Open(m.logPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return LogChunk{Offset: 0}, nil
	}
	if err != nil {
		return LogChunk{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return LogChunk{}, err
	}
	size := st.Size()
	offset = min(max(offset, 0), size)
	buf := make([]byte, min(size-offset, maxLogChunk))
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return LogChunk{}, err
	}
	if offset+int64(n) < size {
		// A chunk ending inside a UTF-8 sequence would garble that
		// character in both chunks once each is sent as a JSON string:
		// the next chunk starts with it instead.
		n = completeRunes(buf[:n])
	}
	return LogChunk{Log: string(buf[:n]), Offset: offset + int64(n), LogSize: size}, nil
}

// completeRunes is the length of b without a UTF-8 sequence cut off at its
// end; never 0, so reading always moves on.
func completeRunes(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if i > 0 && !utf8.FullRune(b[i:]) {
				return i
			}
			break
		}
	}
	return len(b)
}

// Follow waits up to wait for new log data or a state change after offset,
// then returns the job and the next log chunk.
func (m *Manager) Follow(ctx context.Context, id string, offset int64, wait time.Duration) (Record, LogChunk, error) {
	changed := m.changed(id)
	defer m.unwatch(id, changed)
	rec, err := m.Get(ctx, id)
	if err != nil {
		return rec, LogChunk{}, err
	}
	chunk, err := m.ReadLog(id, offset)
	if err != nil || chunk.Log != "" || Done(rec.State) || wait <= 0 {
		return rec, chunk, err
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-changed:
	case <-t.C:
	case <-ctx.Done():
	}
	if rec, err = m.Get(ctx, id); err != nil {
		return rec, LogChunk{}, err
	}
	chunk, err = m.ReadLog(id, offset)
	return rec, chunk, err
}

// Cancel asks a job to stop. While the job follows a helper operation, the
// operation decides: packages being installed are never interrupted.
func (m *Manager) Cancel(ctx context.Context, id string, caller helperapi.Caller) (Record, error) {
	m.mu.Lock()
	j := m.live[id]
	m.mu.Unlock()
	if j == nil {
		if _, err := m.Get(ctx, id); err != nil {
			return Record{}, err
		}
		return Record{}, api.NewError(http.StatusConflict, CodeNotCancelable, nil)
	}
	j.mu.Lock()
	opID, cancelable := j.opID, j.cancelable
	j.mu.Unlock()
	if !cancelable {
		return Record{}, api.NewError(http.StatusConflict, CodeNotCancelable, nil)
	}
	if opID != "" {
		err := m.Helper.Call(ctx, helperapi.ActionOpCancel, caller, helperapi.OpCancelRequest{ID: opID}, nil)
		if he, ok := helperapi.AsError(err); ok && he.Code == helperapi.CodeOpNotCancelable {
			return Record{}, api.NewError(http.StatusConflict, CodeNotCancelable, nil)
		} else if err != nil {
			return Record{}, err
		}
	} else {
		j.cancel()
	}
	m.Log.Info("job cancel requested", "id", id, "kind", j.rec.Kind, "user", caller.Username)
	return m.Get(ctx, id)
}

// Job is the handle a running job uses.
type Job struct {
	m      *Manager
	ctx    context.Context
	cancel context.CancelFunc
	log    *cappedLog

	mu           sync.Mutex
	rec          Record
	opID         string
	cancelable   bool
	lastProgress time.Time
}

// ID returns the job's ID.
func (j *Job) ID() string { return j.rec.ID }

// Caller identifies the job's starter towards the helper.
func (j *Job) Caller() helperapi.Caller {
	return helperapi.Caller{UserID: j.rec.UserID, Username: j.rec.Username, Remote: j.rec.Remote, Source: j.rec.Source}
}

// Log returns the writer for the job's log.
func (j *Job) Log() io.Writer { return j.log }

// Logf appends a formatted line to the log.
func (j *Job) Logf(format string, args ...any) {
	if j.log != nil {
		_, _ = fmt.Fprintf(j.log, format, args...) // the log never fails the job
	}
}

func (j *Job) setState(state string) {
	j.mu.Lock()
	j.rec.State, j.rec.StartedAt = state, j.m.now().Unix()
	j.mu.Unlock()
	if _, err := j.m.DB.ExecContext(context.Background(), `UPDATE jobs SET state = ?, started_at = ? WHERE id = ?`,
		state, j.rec.StartedAt, j.rec.ID); err != nil {
		j.m.Log.Error("saving job failed", "id", j.rec.ID, "err", err)
	}
	j.m.notify(j.rec.ID)
}

// SetParam adds a parameter while the job runs, such as the identifier of
// what it made, for the UI to read from the job (never a secret: params
// are shown and audited).
func (j *Job) SetParam(key string, value any) {
	j.mu.Lock()
	params := make(map[string]any, len(j.rec.Params)+1)
	for k, v := range j.rec.Params {
		params[k] = v
	}
	params[key] = value
	j.rec.Params = params
	j.mu.Unlock()
	data, err := json.Marshal(params)
	if err == nil {
		_, err = j.m.DB.ExecContext(context.Background(), `UPDATE jobs SET params = ? WHERE id = ?`, string(data), j.rec.ID)
	}
	if err != nil {
		j.m.Log.Error("saving job params failed", "id", j.rec.ID, "err", err)
	}
	j.m.notify(j.rec.ID)
}

// Progress records completion between 0 and 1 (saved at most once a second).
func (j *Job) Progress(p float64) {
	p = min(max(p, 0), 1)
	j.mu.Lock()
	if time.Since(j.lastProgress) < progressGap && p < 1 {
		j.mu.Unlock()
		return
	}
	j.lastProgress = time.Now()
	j.mu.Unlock()
	if _, err := j.m.DB.ExecContext(context.Background(), `UPDATE jobs SET progress = ? WHERE id = ?`, p, j.rec.ID); err != nil {
		j.m.Log.Warn("saving job progress failed", "id", j.rec.ID, "err", err)
	}
	j.m.notify(j.rec.ID)
}

// RunOp starts a helper operation, copies its log into the job's log until it
// ends and decodes its result into result (may be nil). A failed or canceled
// operation is returned as its *helperapi.Error. If the web process stops
// meanwhile, the operation keeps running in the helper.
func (j *Job) RunOp(action string, req, result any) error {
	var ref helperapi.OpRef
	if err := j.m.Helper.Call(j.ctx, action, j.Caller(), req, &ref); err != nil {
		return err
	}
	j.mu.Lock()
	j.opID = ref.ID
	j.mu.Unlock()
	if _, err := j.m.DB.ExecContext(context.Background(), `UPDATE jobs SET op_id = ? WHERE id = ?`, ref.ID, j.rec.ID); err != nil {
		j.m.Log.Warn("saving job operation failed", "id", j.rec.ID, "err", err)
	}
	defer func() {
		j.mu.Lock()
		j.opID, j.cancelable = "", true
		j.mu.Unlock()
	}()

	var offset int64
	for {
		var res helperapi.OpGetResponse
		// Following uses a background context: canceling the job is
		// forwarded to the helper instead of abandoning the operation.
		if err := j.m.Helper.Call(context.Background(), helperapi.ActionOpGet, j.Caller(),
			helperapi.OpGetRequest{ID: ref.ID, Offset: offset}, &res); err != nil {
			return err
		}
		if res.Log != "" {
			_, _ = io.WriteString(j.log, res.Log)
		}
		offset = res.Offset
		j.mu.Lock()
		changedCancel := j.cancelable != res.Op.Cancelable
		j.cancelable = res.Op.Cancelable
		j.mu.Unlock()
		if changedCancel {
			j.m.notify(j.rec.ID)
		}
		if helperapi.OpDone(res.Op.State) && offset >= res.LogSize {
			if res.Op.State == helperapi.OpSucceeded {
				if result != nil && len(res.Op.Result) > 0 {
					return json.Unmarshal(res.Op.Result, result)
				}
				return nil
			}
			if res.Op.Error != nil {
				return res.Op.Error
			}
			return &helperapi.Error{Code: helperapi.CodeOpFailed}
		}
		if res.Log == "" {
			time.Sleep(opPollPeriod)
		}
	}
}

// cappedLog stops writing after limit bytes and wakes followers on writes.
type cappedLog struct {
	mu      sync.Mutex
	w       io.Writer
	limit   int64
	written int64
	noted   bool
	notify  func()
}

func (c *cappedLog) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := c.limit - c.written; room > 0 {
		n, err := c.w.Write(p[:min(int64(len(p)), room)])
		c.written += int64(n)
		if err != nil {
			return n, err
		}
	} else if !c.noted {
		c.noted = true
		_, _ = io.WriteString(c.w, "\n[log truncated]\n")
	}
	if c.notify != nil {
		c.notify()
	}
	return len(p), nil
}
