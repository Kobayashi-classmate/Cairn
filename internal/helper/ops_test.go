// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/locks"
)

type testReq struct {
	Name string `json:"name"`
}

func newTestOps(t *testing.T) (*Ops, *Registry) {
	t.Helper()
	o, err := NewOps(t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	RegisterOpActions(r, o)
	return o, r
}

func call[Resp any](t *testing.T, r *Registry, action string, req any) (Resp, error) {
	t.Helper()
	var resp Resp
	body, _ := json.Marshal(req)
	out, err := r.handlers[action](context.Background(), helperapi.Caller{Username: "admin", Source: "web"}, body)
	if err != nil {
		return resp, err
	}
	raw, _ := json.Marshal(out)
	_ = json.Unmarshal(raw, &resp)
	return resp, nil
}

func waitOp(t *testing.T, o *Ops, id string, want ...string) helperapi.Op {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, err := o.Get(id, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if res.Op.State == w {
				return res.Op
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	res, _ := o.Get(id, 0)
	t.Fatalf("operation %s: state %s, want %v", id, res.Op.State, want)
	return helperapi.Op{}
}

func TestOperationLifecycleAndLog(t *testing.T) {
	o, r := newTestOps(t)
	RegisterOp(r, o, "test.echo", OpSpec[testReq]{
		Run: func(_ context.Context, c helperapi.Caller, req testReq, log io.Writer) (any, error) {
			_, _ = fmt.Fprintf(log, "hello %s from %s\n", req.Name, c.Username)
			return map[string]string{"greeted": req.Name}, nil
		},
	})
	ref, err := call[helperapi.OpRef](t, r, "test.echo", testReq{Name: "world"})
	if err != nil || !helperapi.ValidOpID(ref.ID) {
		t.Fatalf("start: %v %+v", err, ref)
	}
	op := waitOp(t, o, ref.ID, helperapi.OpSucceeded)
	if op.Kind != "test.echo" || op.User != "admin" || op.StartedAt == 0 || op.FinishedAt == 0 || op.Cancelable {
		t.Fatalf("op: %+v", op)
	}
	if string(op.Result) != `{"greeted":"world"}` {
		t.Fatalf("result %s", op.Result)
	}
	// The log is read in chunks from an offset.
	res, _ := o.Get(ref.ID, 6)
	if res.Log != "world from admin\n" || res.Offset != res.LogSize || res.LogSize != 23 {
		t.Fatalf("log chunk: %+v", res)
	}
	if res, _ := o.Get(ref.ID, 1000); res.Log != "" || res.Offset != 23 {
		t.Fatalf("offset past the end: %+v", res)
	}
	if _, err := call[helperapi.OpGetResponse](t, r, helperapi.ActionOpGet, helperapi.OpGetRequest{ID: "../../etc/passwd"}); code(err) != helperapi.CodeInvalidRequest {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := o.Get(strings.Repeat("a", 24), 0); code(err) != helperapi.CodeOpNotFound {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestOperationFailures(t *testing.T) {
	o, r := newTestOps(t)
	RegisterOp(r, o, "test.fail", OpSpec[testReq]{
		Run: func(_ context.Context, _ helperapi.Caller, req testReq, _ io.Writer) (any, error) {
			switch req.Name {
			case "typed":
				return nil, &helperapi.Error{Code: "pkg.not_found", Params: map[string]any{"name": "x"}}
			case "panic":
				panic("boom")
			}
			return nil, errors.New("apt-get exited with status 100")
		},
	})
	for name, want := range map[string]string{"typed": "pkg.not_found", "plain": helperapi.CodeOpFailed, "panic": helperapi.CodeOpFailed} {
		ref, _ := call[helperapi.OpRef](t, r, "test.fail", testReq{Name: name})
		op := waitOp(t, o, ref.ID, helperapi.OpFailed)
		if op.Error == nil || op.Error.Code != want {
			t.Errorf("%s: error %+v", name, op.Error)
		}
	}
}

func TestOperationLocksAndCancel(t *testing.T) {
	o, r := newTestOps(t)
	gate := make(chan struct{})
	var ran atomic.Int32
	spec := func(cancelWhileRunning bool) OpSpec[testReq] {
		return OpSpec[testReq]{
			Locks:              func(testReq) []string { return []string{LockPkg} },
			CancelWhileRunning: cancelWhileRunning,
			Run: func(ctx context.Context, _ helperapi.Caller, req testReq, _ io.Writer) (any, error) {
				ran.Add(1)
				if req.Name == "block" {
					select {
					case <-gate:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return nil, nil
			},
		}
	}
	RegisterOp(r, o, "test.install", spec(false))
	RegisterOp(r, o, "test.refresh", spec(true))

	first, _ := call[helperapi.OpRef](t, r, "test.install", testReq{Name: "block"})
	waitOp(t, o, first.ID, helperapi.OpRunning)
	second, _ := call[helperapi.OpRef](t, r, "test.install", testReq{Name: "quick"})
	third, _ := call[helperapi.OpRef](t, r, "test.install", testReq{Name: "quick"})

	// The second and third wait for the package lock.
	time.Sleep(50 * time.Millisecond)
	if op, _ := o.Get(second.ID, 0); op.Op.State != helperapi.OpQueued || op.Op.WaitingFor[0] != LockPkg || !op.Op.Cancelable {
		t.Fatalf("second: %+v", op.Op)
	}
	if !o.Held(LockPkg) {
		t.Fatal("lock not held")
	}
	// A running install cannot be canceled; a queued one can.
	if _, err := call[helperapi.Op](t, r, helperapi.ActionOpCancel, helperapi.OpCancelRequest(first)); code(err) != helperapi.CodeOpNotCancelable {
		t.Fatalf("cancel running install: %v", err)
	}
	if _, err := call[helperapi.Op](t, r, helperapi.ActionOpCancel, helperapi.OpCancelRequest(third)); err != nil {
		t.Fatal(err)
	}
	if op := waitOp(t, o, third.ID, helperapi.OpCanceled); op.Error.Code != helperapi.CodeOpCanceled || op.StartedAt != 0 {
		t.Fatalf("third: %+v", op)
	}
	close(gate)
	waitOp(t, o, first.ID, helperapi.OpSucceeded)
	waitOp(t, o, second.ID, helperapi.OpSucceeded)
	if ran.Load() != 2 {
		t.Fatalf("ran %d operations, want 2 (the canceled one must not run)", ran.Load())
	}
	if _, err := o.Cancel(first.ID); code(err) != helperapi.CodeOpNotCancelable {
		t.Fatalf("cancel finished: %v", err)
	}

	// A running operation that allows it is canceled through its context.
	gate = make(chan struct{})
	refresh, _ := call[helperapi.OpRef](t, r, "test.refresh", testReq{Name: "block"})
	waitOp(t, o, refresh.ID, helperapi.OpRunning)
	if _, err := o.Cancel(refresh.ID); err != nil {
		t.Fatal(err)
	}
	waitOp(t, o, refresh.ID, helperapi.OpCanceled)
	if o.Held(LockPkg) {
		t.Fatal("lock still held after cancel")
	}
}

func TestOperationWaitsForExternalHolder(t *testing.T) {
	old := locks.RecheckInterval
	locks.RecheckInterval = 20 * time.Millisecond
	t.Cleanup(func() { locks.RecheckInterval = old })
	o, r := newTestOps(t)
	var updating atomic.Bool
	updating.Store(true)
	o.Busy = map[string]func() bool{LockPkg: updating.Load}
	RegisterOp(r, o, "test.install", OpSpec[testReq]{
		Locks: func(testReq) []string { return []string{LockPkg} },
		Run:   func(context.Context, helperapi.Caller, testReq, io.Writer) (any, error) { return nil, nil },
	})
	ref, _ := call[helperapi.OpRef](t, r, "test.install", testReq{})
	time.Sleep(100 * time.Millisecond)
	if op, _ := o.Get(ref.ID, 0); op.Op.State != helperapi.OpQueued {
		t.Fatalf("ran while the update held the package manager: %s", op.Op.State)
	}
	updating.Store(false)
	waitOp(t, o, ref.ID, helperapi.OpSucceeded)
}

func TestUnfinishedOperationsAreInterruptedOnStart(t *testing.T) {
	dir := t.TempDir()
	id := strings.Repeat("ab", 12)
	running, _ := json.Marshal(helperapi.Op{ID: id, Kind: "pkg.refresh", State: helperapi.OpRunning, CreatedAt: time.Now().Unix(), Cancelable: true})
	if err := os.WriteFile(filepath.Join(dir, id+".json"), running, 0o600); err != nil {
		t.Fatal(err)
	}
	old := strings.Repeat("cd", 12)
	ancient, _ := json.Marshal(helperapi.Op{ID: old, State: helperapi.OpSucceeded, CreatedAt: time.Now().Add(-30 * 24 * time.Hour).Unix()})
	_ = os.WriteFile(filepath.Join(dir, old+".json"), ancient, 0o600)
	_ = os.WriteFile(filepath.Join(dir, old+".log"), []byte("x"), 0o600)

	o, err := NewOps(dir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	res, err := o.Get(id, 0)
	if err != nil || res.Op.State != helperapi.OpInterrupted || res.Op.Error.Code != helperapi.CodeOpInterrupted || res.Op.Cancelable {
		t.Fatalf("not marked interrupted: %+v %v", res.Op, err)
	}
	if _, err := os.Stat(filepath.Join(dir, old+".log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old record not pruned")
	}
}

func TestPkgRefresh(t *testing.T) {
	var got []string
	orig := runCommand
	runCommand = func(_ context.Context, log io.Writer, name string, args ...string) error {
		got = append(got, name+" "+strings.Join(args, " "))
		_, _ = fmt.Fprintln(log, "Hit:1 http://deb.debian.org/debian trixie InRelease")
		return nil
	}
	t.Cleanup(func() { runCommand = orig })

	for manager, want := range map[string]string{"apt": "apt-get update", "dnf": "dnf makecache --assumeyes"} {
		got = nil
		o, r := newTestOps(t)
		RegisterPkg(r, o, manager)
		ref, err := call[helperapi.OpRef](t, r, helperapi.ActionPkgRefresh, helperapi.Empty{})
		if err != nil {
			t.Fatal(err)
		}
		waitOp(t, o, ref.ID, helperapi.OpSucceeded)
		if len(got) != 1 || got[0] != want {
			t.Fatalf("%s ran %q", manager, got)
		}
	}
	o, r := newTestOps(t)
	RegisterPkg(r, o, "")
	ref, _ := call[helperapi.OpRef](t, r, helperapi.ActionPkgRefresh, helperapi.Empty{})
	if op := waitOp(t, o, ref.ID, helperapi.OpFailed); op.Error.Code != CodePkgUnsupported {
		t.Fatalf("unsupported: %+v", op.Error)
	}
}

func TestUpdateRefusedWhilePackageOperationRuns(t *testing.T) {
	o, _ := newTestOps(t)
	release, ok := o.TryLock(LockPkg)
	if !ok {
		t.Fatal("lock")
	}
	defer release()
	u := &Updater{StateDir: t.TempDir(), Version: "0.1.0", Format: "deb", Arch: "amd64", Log: slog.New(slog.DiscardHandler), Ops: o}
	if _, err := u.Start(context.Background(), "0.2.0"); code(err) != helperapi.CodePkgBusy {
		t.Fatalf("update while busy: %v", err)
	}
}
