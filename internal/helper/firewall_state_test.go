// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// tellingBackend is a fake that can tell whether its rules are current;
// it never says they are, so Reapply always has something to rewrite.
type tellingBackend struct{ *fakeBackend }

func (tellingBackend) current(context.Context, fwRender) bool { return false }

// debianFirewall is Debian (no platform default: Cairn's own nftables
// table) where Cairn took over a running ufw: the state says ufw is on.
func debianFirewall(t *testing.T) (f *Firewall, ufw, nft *fakeBackend) {
	t.Helper()
	old, oldSystemd := sshDetect, systemdRunning
	t.Cleanup(func() { sshDetect, systemdRunning = old, oldSystemd })
	sshDetect = func(context.Context, string) sshEndpoints { return sshEndpoints{Ports: []int{22}, Installed: true} }
	systemdRunning = func() bool { return false }
	ufw, nft = &fakeBackend{on: true}, &fakeBackend{}
	f = &Firewall{StateDir: t.TempDir(), Default: "none", PanelPort: 28443,
		backends: map[string]fwBackend{helperapi.FirewallUFW: tellingBackend{ufw}, helperapi.FirewallFirewalld: &fakeBackend{},
			helperapi.FirewallNftables: tellingBackend{nft}}}
	fakeClock(t, f)
	if err := f.save(fwConfig{Enabled: true, Backend: helperapi.FirewallUFW,
		Rules: []helperapi.FirewallRule{{ID: "r00000000000001", Action: "allow", Protocol: "tcp", Ports: []string{"443"}}}}); err != nil {
		t.Fatal(err)
	}
	return f, ufw, nft
}

func storedConfig(t *testing.T, f *Firewall) fwConfig {
	t.Helper()
	cfg, err := f.load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// ufw switched off outside the panel: the firewall is off, and neither a
// change nor the helper's start switches Cairn's nftables table on in its
// place.
func TestFirewallStoppedOutsideThePanelIsOff(t *testing.T) {
	f, ufw, nft := debianFirewall(t)
	ctx := context.Background()
	ufw.on = false // "ufw disable" on the command line
	if st := f.Status(ctx); st.Enabled {
		t.Fatalf("status: enabled with nothing enforcing rules (%+v)", st)
	}
	f.Startup(ctx)
	if nft.on || len(nft.applied) != 0 || len(ufw.applied) != 0 {
		t.Fatalf("the helper's start applied rules: nft %v (%d), ufw %d", nft.on, len(nft.applied), len(ufw.applied))
	}
	if cfg := storedConfig(t, f); cfg.Enabled {
		t.Fatalf("still recorded as on: %+v", cfg)
	}
	ch, err := f.setRule(ctx, fwAdmin, helperapi.FirewallRule{Action: "allow", Protocol: "tcp", Ports: []string{"8080"}})
	if err != nil || ch.Pending != nil {
		t.Fatalf("change: %+v %v", ch, err)
	}
	if nft.on || len(nft.applied) != 0 || ufw.on {
		t.Fatalf("an unrelated change switched a firewall on: nft %v (%d), ufw %v", nft.on, len(nft.applied), ufw.on)
	}
	if cfg := storedConfig(t, f); cfg.Enabled || len(cfg.Rules) != 2 {
		t.Fatalf("stored: %+v", cfg)
	}
	// Switching it on again from the panel picks the platform's own and
	// waits for confirmation; a rollback leaves both off.
	p, err := f.Enable(ctx, fwAdmin, true, io.Discard)
	if err != nil || p == nil || !nft.on {
		t.Fatalf("enable: %+v %v (nft %v)", p, err, nft.on)
	}
	if err := f.Rollback(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if nft.on || ufw.on {
		t.Fatalf("after the rollback: nft %v ufw %v", nft.on, ufw.on)
	}
}

// While ufw runs as recorded, nothing changes: the status says on, a
// change waits for confirmation on ufw, and the helper's start rewrites
// the rules in this version's form.
func TestFirewallRunningAsRecordedStaysOn(t *testing.T) {
	f, ufw, nft := debianFirewall(t)
	ctx := context.Background()
	if st := f.Status(ctx); !st.Enabled || st.Backend != helperapi.FirewallUFW {
		t.Fatalf("status %+v", st)
	}
	f.Startup(ctx)
	if len(ufw.applied) != 1 || nft.on {
		t.Fatalf("start: ufw applies %d, nft %v", len(ufw.applied), nft.on)
	}
	ch, err := f.setRule(ctx, fwAdmin, helperapi.FirewallRule{Action: "allow", Protocol: "tcp", Ports: []string{"8080"}})
	if err != nil || ch.Pending == nil || len(ufw.applied) != 2 || nft.on {
		t.Fatalf("change: %+v %v; ufw applies %d, nft %v", ch, err, len(ufw.applied), nft.on)
	}
}

// The helper's start rewrites only rules that are in force and on record:
// it never switches on a backend that is not running, nor applies an
// empty configuration over rules it has no record of.
func TestFirewallReapplyOnlyRewritesRulesInForce(t *testing.T) {
	f, ufw, nft := debianFirewall(t)
	ctx := context.Background()
	// Cairn's own table on record, but not loaded (stopped outside).
	if err := f.save(fwConfig{Enabled: true, Backend: helperapi.FirewallNftables}); err != nil {
		t.Fatal(err)
	}
	ufw.on = false
	if err := f.Reapply(ctx); err != nil || nft.on || len(nft.applied) != 0 {
		t.Fatalf("table not loaded: %v; nft %v (%d)", err, nft.on, len(nft.applied))
	}
	// ufw running, with no record of Cairn's rules (the state is gone).
	ufw.on = true
	if err := os.Remove(f.stateDir("state.json")); err != nil {
		t.Fatal(err)
	}
	if err := f.Reapply(ctx); err != nil || len(ufw.applied) != 0 {
		t.Fatalf("no record: %v; ufw applies %d", err, len(ufw.applied))
	}
}

// An empty state file (a write cut short by a power cut, from a version
// that did not flush its writes) reads as no state: the firewall's
// actions keep working.
func TestFirewallEmptyStateReadsAsNone(t *testing.T) {
	f, ufw, _ := debianFirewall(t)
	ctx := context.Background()
	ufw.on = false
	if err := os.WriteFile(f.stateDir("state.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := f.load(); err != nil || cfg.Enabled || len(cfg.Rules) != 0 {
		t.Fatalf("load: %+v %v", cfg, err)
	}
	ch, err := f.setRule(ctx, fwAdmin, helperapi.FirewallRule{Action: "allow", Protocol: "tcp", Ports: []string{"8080"}})
	if err != nil || ch.Pending != nil {
		t.Fatalf("change: %+v %v", ch, err)
	}
	if st := f.Status(ctx); st.Enabled || len(st.Rules) != 3 {
		t.Fatalf("status %+v", st)
	}
	// Anything else that does not parse is still an error, not an empty
	// configuration that would drop every rule.
	if err := os.WriteFile(f.stateDir("state.json"), []byte(`{"enabled": tr`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.load(); err == nil {
		t.Fatalf("broken state: %v", err)
	}
}
