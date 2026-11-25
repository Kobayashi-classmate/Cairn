// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// reapplyFirewall is a Debian system whose firewall (nftables) is on with
// the sample rules and forwards, and IPv4 forwarding off. The commands run
// are recorded, and fail while *fail is set.
func reapplyFirewall(t *testing.T) (*Firewall, fwConfig, *[]string, *bool) {
	t.Helper()
	oldRun, oldQuery, oldSystemd, oldSSH := runCommand, queryCommand, systemdRunning, sshDetect
	t.Cleanup(func() { runCommand, queryCommand, systemdRunning, sshDetect = oldRun, oldQuery, oldSystemd, oldSSH })
	systemdRunning = func() bool { return false }
	sshDetect = func(context.Context, string) sshEndpoints { return sshEndpoints{Ports: []int{22}, Installed: true} }
	f := &Firewall{StateDir: t.TempDir(), Default: "none", PanelPort: 28443}
	ipForwardRoot(t, f, "0")
	nft := filepath.Join(f.Root, nftBinary)
	if err := os.MkdirAll(filepath.Dir(nft), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nft, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	calls, fail := []string{}, false
	queryCommand = func(context.Context, string, ...string) (string, error) { return "", nil }
	runCommand = func(_ context.Context, _ io.Writer, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if fail {
			return errors.New("broken for the test")
		}
		return nil
	}
	s := sampleRender()
	cfg := fwConfig{Enabled: true, Backend: helperapi.FirewallNftables, BlockPing: s.BlockPing, Rules: s.Rules[2:],
		Addresses: s.Addresses, Forwards: sampleForwards, SSHSeen: []int{22}}
	if err := f.save(cfg); err != nil {
		t.Fatal(err)
	}
	return f, cfg, &calls, &fail
}

// rendered is what this version writes for cfg on f's system.
func rendered(f *Firewall, cfg fwConfig) string {
	return renderNft(fwForwarding{f: f}.withForwarding(f.render(cfg, []int{22})))
}

// writeRules puts a rules file in place as an earlier version wrote it:
// the forwards' translations were not limited to this host then.
func writeRules(t *testing.T, f *Firewall, cfg fwConfig) (string, string) {
	t.Helper()
	p := filepath.Join(f.Root, nftRulesFile)
	old := strings.ReplaceAll(rendered(f, cfg), "fib daddr type local ", "")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, old
}

func stateJSON(t *testing.T, f *Firewall) string {
	t.Helper()
	data, err := os.ReadFile(f.stateDir("state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Rules written by an earlier version give way to this version's when the
// helper starts; rules already in this form are left alone.
func TestFirewallReapplyRewritesOldRules(t *testing.T) {
	f, cfg, calls, _ := reapplyFirewall(t)
	ctx := context.Background()
	p, _ := writeRules(t, f, cfg)
	want := rendered(f, cfg)
	state := stateJSON(t, f)
	if err := f.Reapply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*calls, "\n"); got != nftBinary+" -c -f "+p+"\n"+nftBinary+" -f "+p {
		t.Fatalf("commands:\n%s", got)
	}
	if got := readTrim(t, p); got != strings.TrimSpace(want) || !strings.Contains(got, "fib daddr type local tcp dport 8090") {
		t.Fatalf("rules:\n%s", got)
	}
	// The configuration is the one confirmed before: nothing waits for a
	// confirmation, no rollback timer runs.
	if stateJSON(t, f) != state || len(f.timers) != 0 {
		t.Fatalf("state changed:\n%s\ntimers %v", stateJSON(t, f), f.timers)
	}
	if readTrim(t, filepath.Join(f.Root, ipForwardProc)) != "1" {
		t.Fatal("forwarding off with a forward to another host")
	}

	// The next start finds the rules in this form: the firewall is not touched.
	*calls = nil
	if err := f.Reapply(ctx); err != nil || len(*calls) != 0 {
		t.Fatalf("second start: %v %q", err, *calls)
	}
}

// A change that waits for confirmation (and its rollback timer) is left
// alone, and so is a firewall that is off.
func TestFirewallReapplyLeavesPendingAndOff(t *testing.T) {
	f, cfg, calls, _ := reapplyFirewall(t)
	ctx := context.Background()
	p, old := writeRules(t, f, cfg)
	pending := cfg
	pending.Pending = &fwPending{FirewallPending: helperapi.FirewallPending{ID: "x000000000000001", Kind: "rule.add",
		Expires: time.Now().Add(time.Minute).Unix()}, Previous: cfg.clone(), Backend: helperapi.FirewallNftables}
	if err := f.save(pending); err != nil {
		t.Fatal(err)
	}
	if err := f.Reapply(ctx); err != nil || len(*calls) != 0 || readTrim(t, p) != strings.TrimSpace(old) {
		t.Fatalf("pending: %v %q", err, *calls)
	}
	off := cfg
	off.Enabled = false
	if err := f.save(off); err != nil {
		t.Fatal(err)
	}
	if err := f.Reapply(ctx); err != nil || len(*calls) != 0 || readTrim(t, p) != strings.TrimSpace(old) {
		t.Fatalf("off: %v %q", err, *calls)
	}
}

// When the rules cannot be rewritten, the helper logs it and carries on:
// the configuration and the rules in place stay as they were, and the next
// start tries again.
func TestFirewallStartupCarriesOnWhenReapplyFails(t *testing.T) {
	f, cfg, calls, fail := reapplyFirewall(t)
	ctx := context.Background()
	var log bytes.Buffer
	f.Log = slog.New(slog.NewTextHandler(&log, nil))
	p, old := writeRules(t, f, cfg)
	state := stateJSON(t, f)
	*fail = true
	f.Startup(ctx)
	if !strings.Contains(log.String(), "rewriting the firewall's rules in this version's form failed") || !strings.Contains(log.String(), "broken for the test") {
		t.Fatalf("log:\n%s", log.String())
	}
	if readTrim(t, p) != strings.TrimSpace(old) || stateJSON(t, f) != state || len(f.timers) != 0 {
		t.Fatalf("after the failure:\n%s", readTrim(t, p))
	}
	if st := f.Status(ctx); !st.Enabled || st.Pending != nil {
		t.Fatalf("status %+v", st)
	}
	*fail, *calls = false, nil
	f.Startup(ctx)
	if !strings.Contains(readTrim(t, p), "fib daddr type local") || len(*calls) != 2 {
		t.Fatalf("next start: %q", *calls)
	}
}

// A backend that cannot tell what is in place is never re-applied.
func TestFirewallReapplyNeedsABackendThatCanTell(t *testing.T) {
	f, b := newTestFirewall(t)
	ctx := context.Background()
	if _, err := f.Enable(ctx, fwSetup, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	n := len(b.applied)
	if err := f.Reapply(ctx); err != nil || len(b.applied) != n {
		t.Fatalf("%v: %d applies, want %d", err, len(b.applied), n)
	}
}

// ufw is current when its tagged rules, Cairn's lines in before.rules and
// the forwards' table are what the render puts there.
func TestUFWCurrent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "etc/ufw")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	before := "*filter\n:ufw-before-input - [0:0]\n:ufw-before-forward - [0:0]\n# End required lines\n" +
		"\n-A ufw-before-input -p icmp --icmp-type echo-request -j ACCEPT\nCOMMIT\n"
	if err := os.WriteFile(filepath.Join(dir, "before.rules"), []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	r := sampleRender()
	r.BlockPing = false
	var user strings.Builder
	for _, c := range ufwCommands(r) {
		user.WriteString("### tuple ### allow tcp 22 0.0.0.0/0 any 0.0.0.0/0 in comment=" + hex.EncodeToString([]byte(c.tag)) + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "user.rules"), []byte(user.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	u := &ufwBackend{root: root}
	ctx := context.Background()
	if !u.current(ctx, r) {
		t.Fatal("not current")
	}
	ping := r
	ping.BlockPing = true
	if u.current(ctx, ping) {
		t.Fatal("ping line")
	}
	r.Forwards = sampleForwards
	if u.current(ctx, r) {
		t.Fatal("forwards missing")
	}
	if _, err := u.setForwardRules(r.Forwards); err != nil {
		t.Fatal(err)
	}
	if u.current(ctx, r) {
		t.Fatal("forwards' table missing")
	}
	p := filepath.Join(root, forwardTableFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(renderForwardTable(r)), 0o600); err != nil {
		t.Fatal(err)
	}
	if !u.current(ctx, r) {
		t.Fatal("not current with the forwards")
	}
	// An address no longer blocked: its ufw rule and the table's copy go.
	r.Addresses = r.Addresses[1:]
	if u.current(ctx, r) {
		t.Fatal("blocked address")
	}
}
