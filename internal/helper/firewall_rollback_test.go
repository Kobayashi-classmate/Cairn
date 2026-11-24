// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// newDebianFirewall is Debian: the platform has no default of its own
// (nftables), ufw is installed and running and Cairn manages it.
func newDebianFirewall(t *testing.T) (f *Firewall, ufw, nft *fakeBackend) {
	t.Helper()
	old, oldSystemd := sshDetect, systemdRunning
	t.Cleanup(func() { sshDetect, systemdRunning = old, oldSystemd })
	sshDetect = func(context.Context, string) sshEndpoints { return sshEndpoints{Ports: []int{22}, Installed: true} }
	systemdRunning = func() bool { return false }
	ufw, nft = &fakeBackend{on: true}, &fakeBackend{}
	f = &Firewall{StateDir: t.TempDir(), Default: "none", PanelPort: 28443,
		backends: map[string]fwBackend{helperapi.FirewallUFW: ufw, helperapi.FirewallFirewalld: &fakeBackend{}, helperapi.FirewallNftables: nft}}
	fakeClock(t, f)
	if err := f.save(fwConfig{Enabled: true, Backend: helperapi.FirewallUFW}); err != nil {
		t.Fatal(err)
	}
	return f, ufw, nft
}

// Switching the firewall on may use another backend than the one recorded
// before (ufw was switched off, the platform's is nftables): the rollback
// undoes it where it was applied.
func TestFirewallRollbackUndoesOnTheBackendApplied(t *testing.T) {
	f, ufw, nft := newDebianFirewall(t)
	ctx := context.Background()
	if err := f.Disable(ctx, io.Discard); err != nil {
		t.Fatal(err)
	}
	p, err := f.Enable(ctx, fwAdmin, true, io.Discard)
	if err != nil || p == nil || ufw.on || !nft.on {
		t.Fatalf("enable: %+v %v (ufw on %v, nftables on %v)", p, err, ufw.on, nft.on)
	}
	if cfg, _ := f.load(); cfg.Pending.Backend != helperapi.FirewallNftables || cfg.Pending.Previous.Backend != helperapi.FirewallUFW {
		t.Fatalf("pending: applied on %q, before %q", cfg.Pending.Backend, cfg.Pending.Previous.Backend)
	}
	if err := f.Rollback(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	st := f.Status(ctx)
	if nft.on || ufw.on {
		t.Fatalf("after the rollback: ufw on %v, nftables on %v", ufw.on, nft.on)
	}
	if st.Enabled || st.Pending != nil || st.LastRollback == nil || st.LastRollback.Kind != "enable" {
		t.Fatalf("status after the rollback: %+v", st)
	}
}

// A state file written before the applied backend was recorded is still
// read, and its pending change undone the way it was then.
func TestFirewallReadsOlderPendingState(t *testing.T) {
	f, fake := newTestFirewall(t)
	fake.on = true
	ctx := context.Background()
	expires := f.clock().Add(30 * time.Second).Unix()
	old := `{"backend":"ufw","enabled":true,"blockPing":true,
  "rules":[{"id":"r000000000000001","action":"allow","protocol":"tcp","ports":["8080"]}],"addresses":[],
  "pending":{"id":"x000000000000001","expires":` + strconv.FormatInt(expires, 10) + `,"kind":"ping",
    "previous":{"backend":"ufw","enabled":true,"blockPing":false,
      "rules":[{"id":"r000000000000001","action":"allow","protocol":"tcp","ports":["8080"]}],"addresses":[]}}}`
	write := func() {
		if err := os.MkdirAll(f.stateDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.stateDir("state.json"), []byte(old), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	st := f.Status(ctx)
	if st.Pending == nil || st.Pending.ID != "x000000000000001" || st.Pending.Kind != "ping" || st.Pending.RollbackFailed != nil || !st.BlockPing {
		t.Fatalf("status: %+v", st)
	}
	if _, err := f.setRule(ctx, fwAdmin, rule9999); !hasCode(err, helperapi.CodeFirewallPending) {
		t.Fatalf("a change while the old one is pending: %v", err)
	}
	if err := f.Rollback(ctx, "x000000000000001"); err != nil {
		t.Fatal(err)
	}
	if st := f.Status(ctx); st.Pending != nil || st.BlockPing || st.LastRollback == nil || len(fake.applied) != 1 || fake.last().BlockPing {
		t.Fatalf("after the rollback: %+v, applied %+v", st, fake.applied)
	}
	// And it can be confirmed.
	write()
	if err := f.Confirm(ctx, "x000000000000001"); err != nil {
		t.Fatal(err)
	}
	if st := f.Status(ctx); st.Pending != nil || !st.BlockPing {
		t.Fatalf("after the confirmation: %+v", st)
	}
}

// A rollback whose backend command fails is not a rollback: the change
// stays pending with the failure on record, the timer tries again a few
// times, and a later try that works ends it.
func TestFirewallFailedRollbackStaysPending(t *testing.T) {
	f, fake := newTestFirewall(t)
	fake.on = true
	ctx := context.Background()
	ch, err := f.setRule(ctx, fwAdmin, rule9999)
	if err != nil || ch.Pending == nil {
		t.Fatalf("setRule: %+v %v", ch, err)
	}
	tx := ch.Pending.ID
	applies := len(fake.applied)
	var pauses []time.Duration
	wait := fwSleep
	fwSleep = func(d time.Duration) { pauses = append(pauses, d); wait(d) }

	fake.fail = true // the backend command fails while the timer rolls back
	rerr := f.Rollback(ctx, tx)
	if !hasCode(rerr, helperapi.CodeFirewallRollbackFailed) {
		t.Fatalf("rollback: %v", rerr)
	}
	// Waited for the change to expire, then tried three times.
	if len(pauses) != 1+len(fwRollbackRetry) || !slices.Equal(pauses[1:], fwRollbackRetry) {
		t.Fatalf("pauses: %v", pauses)
	}
	cfg, _ := f.load()
	st := f.Status(ctx)
	if cfg.Pending == nil || cfg.LastRollback != nil || st.LastRollback != nil || len(fake.applied) != applies ||
		!slices.ContainsFunc(fake.last().Rules, func(r helperapi.FirewallRule) bool { return r.Ports[0] == "9999" }) {
		t.Fatalf("after the failed rollback: pending %+v, last rollback %+v", cfg.Pending, st.LastRollback)
	}
	if failed := st.Pending.RollbackFailed; failed == nil || failed.Attempts != 3 || failed.At == 0 || failed.Detail == "" {
		t.Fatalf("failure on record: %+v", st.Pending)
	}
	// Still pending: no other change, and no keeping what may be half undone.
	if _, err := f.setRule(ctx, fwAdmin, helperapi.FirewallRule{Action: "allow", Protocol: "tcp", Ports: []string{"81"}}); !hasCode(err, helperapi.CodeFirewallPending) {
		t.Fatalf("a change after the failed rollback: %v", err)
	}
	if err := f.Confirm(ctx, tx); !hasCode(err, helperapi.CodeFirewallRollbackFailed) {
		t.Fatalf("confirming after the failed rollback: %v", err)
	}
	// "Roll back now" fails the same way and leaves it pending too.
	if err := f.RollbackNow(ctx, tx); !hasCode(err, helperapi.CodeFirewallRollbackFailed) {
		t.Fatalf("rollback on request: %v", err)
	}
	if cfg, _ := f.load(); cfg.Pending == nil || cfg.Pending.RollbackFailed.Attempts != 4 || cfg.LastRollback != nil {
		t.Fatalf("after the failed request: %+v", cfg.Pending)
	}

	fake.fail = false
	if err := f.RollbackNow(ctx, tx); err != nil {
		t.Fatalf("retry: %v", err)
	}
	st = f.Status(ctx)
	if st.Pending != nil || st.LastRollback == nil || !st.LastRollback.Requested || st.LastRollback.ID != tx ||
		slices.ContainsFunc(fake.last().Rules, func(r helperapi.FirewallRule) bool { return r.Ports[0] == "9999" }) {
		t.Fatalf("after the retry: %+v, rules %+v", st, fake.last().Rules)
	}
}

// The timer's own second try can be the one that works.
func TestFirewallRollbackRetries(t *testing.T) {
	f, fake := newTestFirewall(t)
	fake.on = true
	ctx := context.Background()
	ch, err := f.setRule(ctx, fwAdmin, rule9999)
	if err != nil || ch.Pending == nil {
		t.Fatalf("setRule: %+v %v", ch, err)
	}
	fake.failNext = 1
	if err := f.Rollback(ctx, ch.Pending.ID); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	st := f.Status(ctx)
	if st.Pending != nil || st.LastRollback == nil || st.LastRollback.Requested ||
		slices.ContainsFunc(fake.last().Rules, func(r helperapi.FirewallRule) bool { return r.Ports[0] == "9999" }) {
		t.Fatalf("after the second try: %+v", st)
	}
}

// When the apply fails and the old rules cannot be put back either, the
// state does not claim they were: the change stays pending for its timer.
func TestFirewallFailedApplyThatCannotBeUndone(t *testing.T) {
	f, fake := newTestFirewall(t)
	fake.on = true
	ctx := context.Background()
	fake.fail = true
	if _, err := f.setRule(ctx, fwAdmin, rule9999); !hasCode(err, helperapi.CodeFirewallApplyFailed) {
		t.Fatalf("failed apply: %v", err)
	}
	cfg, _ := f.load()
	if cfg.Pending == nil || !cfg.Pending.Applying || cfg.Pending.RollbackFailed == nil || len(f.timers) != 1 {
		t.Fatalf("state: %+v, timers %d", cfg.Pending, len(f.timers))
	}
	if err := f.Confirm(ctx, cfg.Pending.ID); !hasCode(err, helperapi.CodeFirewallNoPending) {
		t.Fatalf("confirming a change that was never applied: %v", err)
	}
	fake.fail = false
	// The timer does not wait for a change that never was in place.
	var pauses []time.Duration
	wait := fwSleep
	fwSleep = func(d time.Duration) { pauses = append(pauses, d); wait(d) }
	if err := f.Rollback(ctx, cfg.Pending.ID); err != nil || len(pauses) != 0 {
		t.Fatalf("rollback: %v, pauses %v", err, pauses)
	}
	if st := f.Status(ctx); st.Pending != nil || st.LastRollback == nil || slices.ContainsFunc(st.Rules, func(r helperapi.FirewallRule) bool { return r.Ports[0] == "9999" }) {
		t.Fatalf("after the rollback: %+v", st)
	}
}

// restarted is the firewall as a new process finds it: the same state and
// system, no timers, the clock d later.
func restarted(t *testing.T, f *Firewall, d time.Duration) *Firewall {
	t.Helper()
	g := &Firewall{StateDir: f.StateDir, Default: f.Default, PanelPort: f.PanelPort, Executable: f.Executable, backends: f.backends}
	fakeClock(t, g)
	fwSleep(d)
	return g
}

// A change left pending by a reboot (its timer is a transient unit; the
// rules are on disk) is taken over when the system comes up.
func TestFirewallRecover(t *testing.T) {
	ctx := context.Background()
	pending := func(t *testing.T) (*Firewall, *fakeBackend, string) {
		t.Helper()
		f, fake := newTestFirewall(t)
		fake.on = true
		ch, err := f.setRule(ctx, fwAdmin, rule9999)
		if err != nil || ch.Pending == nil {
			t.Fatalf("setRule: %+v %v", ch, err)
		}
		f.timers[ch.Pending.ID].Stop() // the machine goes down
		return f, fake, ch.Pending.ID
	}
	has9999 := func(fake *fakeBackend) bool {
		return slices.ContainsFunc(fake.last().Rules, func(r helperapi.FirewallRule) bool { return r.Ports[0] == "9999" })
	}
	t.Run("expired while the machine was down", func(t *testing.T) {
		f, fake, tx := pending(t)
		g := restarted(t, f, 2*time.Minute)
		if err := g.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		st := g.Status(ctx)
		if st.Pending != nil || st.LastRollback == nil || st.LastRollback.ID != tx || st.LastRollback.Requested || has9999(fake) {
			t.Fatalf("after the takeover: %+v", st)
		}
		// Changes are possible again.
		if _, err := g.setRule(ctx, fwAdmin, rule9999); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("time left: the timer is started again", func(t *testing.T) {
		f, fake, tx := pending(t)
		g := restarted(t, f, 20*time.Second)
		if err := g.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		if st := g.Status(ctx); st.Pending == nil || st.LastRollback != nil || !has9999(fake) || g.timers[tx] == nil {
			t.Fatalf("after the takeover: %+v, timers %v", st, g.timers)
		}
		// It can still be confirmed in what is left of its time.
		if err := g.Confirm(ctx, tx); err != nil || len(g.timers) != 0 {
			t.Fatalf("confirm: %v, timers %v", err, g.timers)
		}
	})
	t.Run("time left, with systemd", func(t *testing.T) {
		f, _, tx := pending(t)
		g := restarted(t, f, 20*time.Second)
		g.Executable = "/usr/lib/cairn/cairn-helper"
		oldQuery := queryCommand
		t.Cleanup(func() { queryCommand = oldQuery })
		systemdRunning = func() bool { return true }
		var calls []string
		active := "inactive\ninactive\n"
		queryCommand = func(_ context.Context, name string, args ...string) (string, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if name == "systemctl" && args[0] == "is-active" {
				return active, os.ErrNotExist
			}
			return "", nil
		}
		if err := g.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		unit := fwRollbackUnit + tx
		if len(calls) != 2 || calls[0] != "systemctl is-active "+unit+".timer "+unit+".service" ||
			!strings.HasPrefix(calls[1], "systemd-run --unit="+unit+" ") || !regexp.MustCompile(` --on-active=(39|40) `).MatchString(calls[1]) ||
			!strings.HasSuffix(calls[1], " firewall rollback "+tx) {
			t.Fatalf("calls: %q", calls)
		}
		// With the timer still there (the helper restarted, not the
		// machine) nothing is started.
		calls, active = nil, "active\ninactive\n"
		if err := g.Recover(ctx); err != nil || len(calls) != 1 {
			t.Fatalf("with the timer waiting: %v %q", err, calls)
		}
	})
	t.Run("the apply was cut short", func(t *testing.T) {
		f, fake, tx := pending(t)
		cfg, _ := f.load()
		cfg.Pending.Applying = true // the helper stopped before the rules were all in place
		if err := f.save(cfg); err != nil {
			t.Fatal(err)
		}
		g := restarted(t, f, time.Second)
		if err := g.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		if st := g.Status(ctx); st.Pending != nil || st.LastRollback == nil || st.LastRollback.ID != tx || has9999(fake) {
			t.Fatalf("after the takeover: %+v", st)
		}
	})
	t.Run("the rollback fails", func(t *testing.T) {
		f, fake, _ := pending(t)
		g := restarted(t, f, 2*time.Minute)
		fake.fail = true
		if err := g.Recover(ctx); !hasCode(err, helperapi.CodeFirewallRollbackFailed) {
			t.Fatalf("takeover: %v", err)
		}
		if st := g.Status(ctx); st.Pending == nil || st.Pending.RollbackFailed == nil || st.LastRollback != nil {
			t.Fatalf("after the failed takeover: %+v", st)
		}
		// The next start tries again.
		fake.fail = false
		if err := restarted(t, g, time.Minute).Recover(ctx); err != nil {
			t.Fatal(err)
		}
		if st := g.Status(ctx); st.Pending != nil || st.LastRollback == nil || has9999(fake) {
			t.Fatalf("after the second takeover: %+v", st)
		}
	})
	t.Run("nothing pending", func(t *testing.T) {
		f, fake := newTestFirewall(t)
		fake.on = true
		if err := f.Recover(ctx); err != nil || len(fake.applied) != 0 {
			t.Fatalf("%v %+v", err, fake.applied)
		}
	})
}
