// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// ctxBackend treats the context the way the real backends do: they run
// every command through execx, which starts nothing on a context that has
// ended and kills what is running when it ends.
type ctxBackend struct {
	mu      sync.Mutex
	on      bool
	applied []fwRender // successful applies in order; the last one is in force
	refused int        // applies refused because the context had ended
	// before runs when the next apply is called, before anything changes.
	before func()
	// after runs once, right after the next successful apply.
	after func()
	// started and release make the next apply slow: it takes effect, closes
	// started and keeps running (one of ufw's several commands) until
	// release is closed.
	started, release chan struct{}
}

func (b *ctxBackend) installed() bool    { return true }
func (b *ctxBackend) packages() []string { return nil }
func (b *ctxBackend) active(ctx context.Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.on && ctx.Err() == nil // "ufw status" does not run on an ended context
}
func (b *ctxBackend) loaded(ctx context.Context) bool   { return b.active(ctx) }
func (b *ctxBackend) external(context.Context) []string { return nil }
func (b *ctxBackend) disable(ctx context.Context, _ io.Writer) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	b.mu.Lock()
	b.on = false
	b.mu.Unlock()
	return nil
}

func (b *ctxBackend) apply(ctx context.Context, _ io.Writer, r fwRender) error {
	b.mu.Lock()
	before := b.before
	b.before = nil
	b.mu.Unlock()
	if before != nil {
		before()
	}
	if ctx.Err() != nil {
		b.mu.Lock()
		b.refused++
		b.mu.Unlock()
		return fmt.Errorf("execx: ufw: %w", ctx.Err())
	}
	b.mu.Lock()
	b.on = true
	b.applied = append(b.applied, r)
	after, started, release := b.after, b.started, b.release
	b.after, b.started, b.release = nil, nil, nil
	b.mu.Unlock()
	if after != nil {
		after()
	}
	if started != nil {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			return fmt.Errorf("execx: ufw: %w", ctx.Err())
		}
	}
	return nil
}

// ports are the ports of the rules in force.
func (b *ctxBackend) ports() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.applied) == 0 {
		return nil
	}
	var ports []string
	for _, r := range b.applied[len(b.applied)-1].Rules {
		ports = append(ports, r.Ports...)
	}
	return ports
}

func (b *ctxBackend) counts() (applied, refused int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.applied), b.refused
}

// newCtxFirewall is a firewall that is on, with a backend that minds its
// context.
func newCtxFirewall(t *testing.T) (*Firewall, *ctxBackend) {
	t.Helper()
	f, _ := newTestFirewall(t)
	b := &ctxBackend{on: true}
	f.backends[helperapi.FirewallUFW] = b
	return f, b
}

var rule9999 = helperapi.FirewallRule{Action: "allow", Protocol: "tcp", Ports: []string{"9999"}}

func statePorts(cfg fwConfig) []string {
	var ports []string
	for _, r := range cfg.Rules {
		ports = append(ports, r.Ports...)
	}
	return ports
}

// The pending change and its rollback timer exist before the first rule
// changes: whatever stops the helper during the apply, something undoes it.
func TestFirewallChangeIsPendingBeforeRulesChange(t *testing.T) {
	f, b := newCtxFirewall(t)
	var during fwConfig
	timers := 0
	b.before = func() {
		during, _ = f.load()
		timers = len(f.timers)
	}
	ch, err := f.setRule(context.Background(), fwAdmin, rule9999)
	if err != nil || ch.Pending == nil {
		t.Fatalf("%+v %v", ch, err)
	}
	if during.Pending == nil || !during.Pending.Applying || during.Pending.Backend != helperapi.FirewallUFW || timers != 1 {
		t.Fatalf("when the rules were applied: pending %+v, timers %d", during.Pending, timers)
	}
	if !slices.Contains(statePorts(during), "9999") || slices.Contains(statePorts(during.Pending.Previous), "9999") {
		t.Fatalf("stored before the apply: %v, to go back to: %v", statePorts(during), statePorts(during.Pending.Previous))
	}
	cfg, _ := f.load()
	if cfg.Pending == nil || cfg.Pending.Applying || cfg.Pending.ID != ch.Pending.ID || cfg.Pending.Expires != ch.Pending.Expires {
		t.Fatalf("after the apply: %+v, answered %+v", cfg.Pending, ch.Pending)
	}
	// The change waits from the moment its rules are in force.
	if left := time.Unix(ch.Pending.Expires, 0).Sub(f.clock()); left < fwConfirm-2*time.Second || left > fwConfirm {
		t.Fatalf("expires in %v", left)
	}
}

// With systemd the timer is a unit of its own: it is started before the
// apply and stopped again when the apply fails.
func TestFirewallTimerUnitAroundTheApply(t *testing.T) {
	f, b := newCtxFirewall(t)
	f.Executable = "/usr/lib/cairn/cairn-helper"
	oldQuery := queryCommand
	t.Cleanup(func() { queryCommand = oldQuery })
	systemdRunning = func() bool { return true }
	var calls []string
	queryCommand = func(ctx context.Context, name string, args ...string) (string, error) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}
	b.before = func() { calls = append(calls, "apply") }
	ch, err := f.setRule(context.Background(), fwAdmin, rule9999)
	if err != nil || ch.Pending == nil {
		t.Fatalf("%+v %v", ch, err)
	}
	unit := fwRollbackUnit + ch.Pending.ID
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "systemd-run --unit="+unit+" ") || calls[1] != "apply" ||
		!strings.Contains(calls[0], "--on-active=60 ") || !strings.HasSuffix(calls[0], " firewall rollback "+ch.Pending.ID) {
		t.Fatalf("calls: %q", calls)
	}
	if err := f.Confirm(context.Background(), ch.Pending.ID); err != nil {
		t.Fatal(err)
	}
	if last := calls[len(calls)-1]; last != "systemctl stop "+unit+".timer" {
		t.Fatalf("after the confirmation: %q", calls)
	}

	// An apply that fails: the old rules come back and the timer goes.
	calls = nil
	fail := &fakeBackend{on: true, failNext: 1}
	f.backends[helperapi.FirewallUFW] = fail
	_, err = f.setRule(context.Background(), fwAdmin, helperapi.FirewallRule{Action: "allow", Protocol: "tcp", Ports: []string{"81"}})
	if !hasCode(err, helperapi.CodeFirewallApplyFailed) {
		t.Fatalf("failed apply: %v", err)
	}
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "systemd-run ") || !strings.HasPrefix(calls[1], "systemctl stop "+fwRollbackUnit) {
		t.Fatalf("calls around a failed apply: %q", calls)
	}
	if cfg, _ := f.load(); cfg.Pending != nil || slices.Contains(statePorts(cfg), "81") {
		t.Fatalf("state after a failed apply: %+v", cfg)
	}
	if len(fail.applied) != 1 || slices.ContainsFunc(fail.last().Rules, func(r helperapi.FirewallRule) bool { return r.Ports[0] == "81" }) {
		t.Fatalf("the old rules were not put back: %+v", fail.applied)
	}
}

// A request that ends (a closed tab, a proxy's timeout) while the helper
// works on a change must not leave new rules in force without a pending
// change and a timer.
func TestFirewallChangeOutlivesItsRequest(t *testing.T) {
	check := func(t *testing.T, f *Firewall, b *ctxBackend, ch helperapi.FirewallChange, err error) {
		t.Helper()
		if err != nil || ch.Pending == nil {
			t.Fatalf("change: %+v %v", ch, err)
		}
		cfg, _ := f.load()
		applied, refused := b.counts()
		if !slices.Contains(b.ports(), "9999") || applied != 1 || refused != 0 {
			t.Fatalf("rules in force %v, applies %d, refused %d", b.ports(), applied, refused)
		}
		if cfg.Pending == nil || cfg.Pending.Applying || !slices.Contains(statePorts(cfg), "9999") || len(f.timers) != 1 {
			t.Fatalf("state: pending %+v, ports %v, timers %d", cfg.Pending, statePorts(cfg), len(f.timers))
		}
		// And it is undone like any other when nobody confirms it.
		if err := f.Rollback(context.Background(), ch.Pending.ID); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(b.ports(), "9999") {
			t.Fatalf("not rolled back: %v", b.ports())
		}
	}
	t.Run("gone once the rules are in force", func(t *testing.T) {
		f, b := newCtxFirewall(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		b.after = cancel
		ch, err := f.setRule(ctx, fwAdmin, rule9999)
		check(t, f, b, ch, err)
	})
	t.Run("gone before the helper starts", func(t *testing.T) {
		// Not "the firewall is off, so only store it" either: the backend
		// cannot be asked on an ended context.
		f, b := newCtxFirewall(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ch, err := f.setRule(ctx, fwAdmin, rule9999)
		check(t, f, b, ch, err)
	})
}

// "Roll back now" over a request that has ended: the old rules come back
// and the timer stops, or nothing is recorded as rolled back.
func TestFirewallRollbackNowOutlivesItsRequest(t *testing.T) {
	f, b := newCtxFirewall(t)
	bg := context.Background()
	ch, err := f.setRule(bg, fwAdmin, rule9999)
	if err != nil || ch.Pending == nil {
		t.Fatalf("%+v %v", ch, err)
	}
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if err := f.RollbackNow(ctx, ch.Pending.ID); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	cfg, _ := f.load()
	if _, refused := b.counts(); refused != 0 || slices.Contains(b.ports(), "9999") {
		t.Fatalf("rules in force %v, refused %d", b.ports(), refused)
	}
	if cfg.Pending != nil || cfg.LastRollback == nil || !cfg.LastRollback.Requested || len(f.timers) != 0 {
		t.Fatalf("state: pending %+v, last rollback %+v, timers %d", cfg.Pending, cfg.LastRollback, len(f.timers))
	}
}

// The whole way: a client of the helper's socket that gives up in the
// middle of a change (cairn-web does when the browser goes away) does not
// stop the change. It ends pending, with its timer.
func TestFirewallChangeSurvivesClientDisconnect(t *testing.T) {
	f, b := newCtxFirewall(t)
	log := slog.New(slog.DiscardHandler)
	f.Log = log
	sock := filepath.Join(t.TempDir(), "h.sock")
	l, err := Listen(sock, -1)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	ops, err := NewOps(t.TempDir(), log)
	if err != nil {
		t.Fatal(err)
	}
	RegisterFirewall(reg, ops, f)
	srv := &Server{Registry: reg, Allowed: func(uint32) bool { return true }, Log: log}
	sctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(sctx, l); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	client := helperapi.NewClient(sock)

	started, release := make(chan struct{}), make(chan struct{})
	b.mu.Lock()
	b.started, b.release = started, release
	b.mu.Unlock()
	browser, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	errc := make(chan error, 1)
	go func() {
		var ch helperapi.FirewallChange
		errc <- client.Call(browser, helperapi.ActionFirewallRuleSet, helperapi.Caller{Username: "admin", UserID: 1, Source: "web", Remote: "203.0.113.5"}, rule9999, &ch)
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the change did not start")
	}
	disconnect()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("client: %v", err)
	}
	// The helper notices the closed connection at once; the command of the
	// apply ends a moment later.
	time.Sleep(100 * time.Millisecond)
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := f.Status(context.Background()) // waits for the change to end
		if st.Pending != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pending change after the client went away: rules in force %v, status %+v", b.ports(), st)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cfg, _ := f.load()
	applied, refused := b.counts()
	if !slices.Contains(b.ports(), "9999") || applied != 1 || refused != 0 || cfg.Pending.Applying || !slices.Contains(statePorts(cfg), "9999") {
		t.Fatalf("rules in force %v, applies %d, refused %d, state %+v", b.ports(), applied, refused, cfg.Pending)
	}
	f.mu.Lock()
	timers := len(f.timers)
	f.mu.Unlock()
	if timers != 1 {
		t.Fatalf("timers: %d", timers)
	}
}
