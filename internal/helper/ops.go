// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/locks"
)

// LockPkg serializes everything that runs the system package manager.
const LockPkg = "pkg"

const (
	maxOpLog     = 4 << 20
	opRetention  = 14 * 24 * time.Hour
	maxOpRecords = 500
)

// Ops runs operations: long privileged actions executed in the background.
// Each operation's state (<id>.json) and log (<id>.log) live in Dir, so the
// web process can follow it, and a restart of the web process loses nothing.
// Operations that were unfinished when the helper itself stopped are marked
// interrupted on the next start.
type Ops struct {
	Dir string
	Log *slog.Logger
	// Busy reports lock holders outside this process; a queued operation
	// waits while Busy[name]() is true (the online update holds "pkg" while
	// its detached unit runs, and so does a package manager transaction
	// that an earlier helper process started).
	Busy map[string]func() bool

	locks locks.Set
	mu    sync.Mutex
	live  map[string]*liveOp
	now   func() time.Time
}

type liveOp struct {
	cancel             context.CancelFunc
	cancelWhileRunning bool
	running            bool
}

// NewOps prepares dir, marks operations left unfinished by a previous run as
// interrupted and removes old records.
func NewOps(dir string, log *slog.Logger) (*Ops, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	o := &Ops{Dir: dir, Log: log, live: map[string]*liveOp{}, now: time.Now}
	o.recoverInterrupted()
	o.prune()
	return o, nil
}

// OpSpec configures an operation kind.
type OpSpec[Req any] struct {
	// Locks names the locks the operation needs; it is queued until all are
	// free. Lock names are compared exactly.
	Locks func(Req) []string
	// CancelWhileRunning allows op.cancel after the operation has started.
	// Leave it false for work that must not stop halfway, such as installing
	// packages; such operations can still be canceled while queued.
	CancelWhileRunning bool
	// Run does the work. Everything written to log reaches the operation's
	// log. A *helperapi.Error is reported as is; other errors become
	// op.failed with the error text as reason.
	Run func(ctx context.Context, c helperapi.Caller, req Req, log io.Writer) (any, error)
}

// RegisterOp adds an action that starts an operation and returns its OpRef.
func RegisterOp[Req any](r *Registry, o *Ops, action string, spec OpSpec[Req]) {
	Register(r, action, func(_ context.Context, c helperapi.Caller, req Req) (helperapi.OpRef, error) {
		var locks []string
		if spec.Locks != nil {
			locks = spec.Locks(req)
		}
		return o.start(action, c, locks, spec.CancelWhileRunning, func(ctx context.Context, log io.Writer) (any, error) {
			return spec.Run(ctx, c, req, log)
		})
	})
}

// RegisterOpActions adds op.get and op.cancel.
func RegisterOpActions(r *Registry, o *Ops) {
	Register(r, helperapi.ActionOpGet, func(_ context.Context, _ helperapi.Caller, req helperapi.OpGetRequest) (helperapi.OpGetResponse, error) {
		return o.Get(req.ID, req.Offset)
	})
	Register(r, helperapi.ActionOpCancel, func(_ context.Context, c helperapi.Caller, req helperapi.OpCancelRequest) (helperapi.Op, error) {
		op, err := o.Cancel(req.ID)
		if err == nil {
			o.Log.Info("operation cancel requested", "id", req.ID, "kind", op.Kind, "user", c.Username)
		}
		return op, err
	})
}

func newOpID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (o *Ops) statePath(id string) string { return filepath.Join(o.Dir, id+".json") }
func (o *Ops) logPath(id string) string   { return filepath.Join(o.Dir, id+".log") }

func (o *Ops) start(kind string, c helperapi.Caller, locks []string, cancelWhileRunning bool,
	fn func(context.Context, io.Writer) (any, error)) (helperapi.OpRef, error) {
	o.prune()
	id := newOpID()
	op := helperapi.Op{
		ID: id, Kind: kind, State: helperapi.OpQueued, CreatedAt: o.now().Unix(),
		WaitingFor: locks, Cancelable: true, User: c.Username,
	}
	if err := os.WriteFile(o.logPath(id), nil, 0o600); err != nil {
		return helperapi.OpRef{}, err
	}
	if err := o.save(op); err != nil {
		return helperapi.OpRef{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.mu.Lock()
	o.live[id] = &liveOp{cancel: cancel, cancelWhileRunning: cancelWhileRunning}
	o.mu.Unlock()
	o.Log.Info("operation queued", "id", id, "kind", kind, "locks", strings.Join(locks, ","), "user", c.Username)
	go o.run(ctx, cancel, op, locks, cancelWhileRunning, fn)
	return helperapi.OpRef{ID: id}, nil
}

func (o *Ops) run(ctx context.Context, cancel context.CancelFunc, op helperapi.Op, lockNames []string,
	cancelWhileRunning bool, fn func(context.Context, io.Writer) (any, error)) {
	defer cancel()
	defer func() {
		o.mu.Lock()
		delete(o.live, op.ID)
		o.mu.Unlock()
	}()
	logFile, err := os.OpenFile(o.logPath(op.ID), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		o.finish(op, nil, err)
		return
	}
	defer logFile.Close()
	logw := &cappedLog{w: logFile, limit: maxOpLog}

	release, err := o.locks.Acquire(ctx, lockNames, o.Busy)
	if err != nil {
		o.finish(op, nil, err)
		return
	}
	defer release()

	o.mu.Lock()
	if l := o.live[op.ID]; l != nil {
		l.running = true
	}
	o.mu.Unlock()
	op.State, op.StartedAt, op.WaitingFor, op.Cancelable = helperapi.OpRunning, o.now().Unix(), nil, cancelWhileRunning
	if err := o.save(op); err != nil {
		o.Log.Error("saving operation state failed", "id", op.ID, "err", err)
	}
	o.Log.Info("operation started", "id", op.ID, "kind", op.Kind)

	result, err := safeRun(ctx, logw, fn)
	o.finish(op, result, err)
}

// safeRun turns a panic in an operation into a failure instead of taking
// the whole helper down.
func safeRun(ctx context.Context, w io.Writer, fn func(context.Context, io.Writer) (any, error)) (res any, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("operation panicked: %v", v)
		}
	}()
	return fn(ctx, w)
}

func (o *Ops) finish(op helperapi.Op, result any, err error) {
	op.FinishedAt, op.WaitingFor, op.Cancelable = o.now().Unix(), nil, false
	var he *helperapi.Error
	switch {
	case err == nil:
		op.State = helperapi.OpSucceeded
		if result != nil {
			if raw, merr := json.Marshal(result); merr == nil {
				op.Result = raw
			}
		}
	case errors.Is(err, context.Canceled):
		op.State, op.Error = helperapi.OpCanceled, &helperapi.Error{Code: helperapi.CodeOpCanceled}
	case errors.As(err, &he):
		op.State, op.Error = helperapi.OpFailed, he
	default:
		op.State = helperapi.OpFailed
		op.Error = &helperapi.Error{Code: helperapi.CodeOpFailed, Params: map[string]any{"reason": tailString(err.Error(), 512)}}
	}
	if serr := o.save(op); serr != nil {
		o.Log.Error("saving operation state failed", "id", op.ID, "err", serr)
	}
	code := "ok"
	if op.Error != nil {
		code = op.Error.Code
	}
	o.Log.Info("operation finished", "id", op.ID, "kind", op.Kind, "state", op.State, "result", code)
}

// save writes the state atomically, so readers never see a partial file.
func (o *Ops) save(op helperapi.Op) error {
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(o.Dir, ".op-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), o.statePath(op.ID))
}

func (o *Ops) load(id string) (helperapi.Op, error) {
	var op helperapi.Op
	data, err := os.ReadFile(o.statePath(id))
	if errors.Is(err, os.ErrNotExist) {
		return op, &helperapi.Error{Code: helperapi.CodeOpNotFound}
	}
	if err != nil {
		return op, err
	}
	return op, json.Unmarshal(data, &op)
}

// Get returns the operation and up to MaxOpLogChunk log bytes from offset.
func (o *Ops) Get(id string, offset int64) (helperapi.OpGetResponse, error) {
	if !helperapi.ValidOpID(id) {
		return helperapi.OpGetResponse{}, &helperapi.Error{Code: helperapi.CodeOpNotFound}
	}
	op, err := o.load(id)
	if err != nil {
		return helperapi.OpGetResponse{}, err
	}
	f, err := os.Open(o.logPath(id))
	if err != nil {
		// A missing log reads as an empty one.
		return helperapi.OpGetResponse{Op: op, Offset: offset}, nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return helperapi.OpGetResponse{}, err
	}
	size := st.Size()
	offset = min(offset, size)
	buf := make([]byte, min(size-offset, helperapi.MaxOpLogChunk))
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return helperapi.OpGetResponse{}, err
	}
	return helperapi.OpGetResponse{Op: op, Log: string(buf[:n]), Offset: offset + int64(n), LogSize: size}, nil
}

// Cancel stops a queued operation, or a running one that allows it.
func (o *Ops) Cancel(id string) (helperapi.Op, error) {
	if !helperapi.ValidOpID(id) {
		return helperapi.Op{}, &helperapi.Error{Code: helperapi.CodeOpNotFound}
	}
	op, err := o.load(id)
	if err != nil {
		return op, err
	}
	o.mu.Lock()
	l := o.live[id]
	o.mu.Unlock()
	if l == nil || (l.running && !l.cancelWhileRunning) {
		return op, &helperapi.Error{Code: helperapi.CodeOpNotCancelable}
	}
	l.cancel()
	return op, nil
}

// Held reports whether a lock is currently held by an operation.
func (o *Ops) Held(name string) bool { return o.locks.Held(name) }

// TryLock takes name if it is free, in this process and outside it (used by
// work that is not an operation, such as the online update while it
// prepares). The returned function releases it.
func (o *Ops) TryLock(name string) (func(), bool) {
	if busy := o.Busy[name]; busy != nil && busy() {
		return nil, false
	}
	return o.locks.TryAcquire([]string{name})
}

func (o *Ops) records() []helperapi.Op {
	entries, err := os.ReadDir(o.Dir)
	if err != nil {
		return nil
	}
	var ops []helperapi.Op
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !helperapi.ValidOpID(id) {
			continue
		}
		if op, err := o.load(id); err == nil {
			ops = append(ops, op)
		}
	}
	return ops
}

func (o *Ops) recoverInterrupted() {
	for _, op := range o.records() {
		if helperapi.OpDone(op.State) {
			continue
		}
		op.State, op.FinishedAt, op.WaitingFor, op.Cancelable = helperapi.OpInterrupted, o.now().Unix(), nil, false
		op.Error = &helperapi.Error{Code: helperapi.CodeOpInterrupted}
		if err := o.save(op); err != nil {
			o.Log.Error("marking operation interrupted failed", "id", op.ID, "err", err)
		}
	}
}

// prune removes finished records older than opRetention, then the oldest
// finished ones beyond maxOpRecords.
func (o *Ops) prune() {
	ops := o.records()
	sort.Slice(ops, func(i, j int) bool { return ops[i].CreatedAt < ops[j].CreatedAt })
	excess := len(ops) - maxOpRecords
	cutoff := o.now().Add(-opRetention).Unix()
	for _, op := range ops {
		if !helperapi.OpDone(op.State) {
			continue
		}
		if op.CreatedAt >= cutoff && excess <= 0 {
			continue
		}
		_ = os.Remove(o.statePath(op.ID))
		_ = os.Remove(o.logPath(op.ID))
		excess--
	}
}

// cappedLog stops writing after limit bytes and notes the truncation once.
type cappedLog struct {
	w       io.Writer
	limit   int64
	written int64
	noted   bool
}

func (c *cappedLog) Write(p []byte) (int, error) {
	if room := c.limit - c.written; room > 0 {
		chunk := p[:min(int64(len(p)), room)]
		n, err := c.w.Write(chunk)
		c.written += int64(n)
		if err != nil {
			return n, err
		}
	} else if !c.noted {
		c.noted = true
		_, _ = io.WriteString(c.w, "\n[log truncated]\n")
	}
	return len(p), nil
}
