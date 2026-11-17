// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// fakeBackend records what it was asked to enforce.
type fakeBackend struct {
	on      bool
	applied []fwRender
	fail    bool
}

func (b *fakeBackend) installed() bool                          { return true }
func (b *fakeBackend) packages() []string                       { return nil }
func (b *fakeBackend) active(context.Context) bool              { return b.on }
func (b *fakeBackend) loaded(context.Context) bool              { return b.on }
func (b *fakeBackend) external(context.Context) []string        { return nil }
func (b *fakeBackend) disable(context.Context, io.Writer) error { b.on = false; return nil }
func (b *fakeBackend) apply(_ context.Context, _ io.Writer, r fwRender) error {
	if b.fail {
		return &helperapi.Error{Code: helperapi.CodeFirewallApplyFailed}
	}
	b.on = true
	b.applied = append(b.applied, r)
	return nil
}

func (b *fakeBackend) last() fwRender { return b.applied[len(b.applied)-1] }

func newTestFirewall(t *testing.T) (*Firewall, *fakeBackend) {
	t.Helper()
	old, oldSystemd := sshPorts, systemdRunning
	t.Cleanup(func() { sshPorts, systemdRunning = old, oldSystemd })
	sshPorts = func(context.Context) []int { return []int{22} }
	systemdRunning = func() bool { return false }
	fake := &fakeBackend{}
	off := &fakeBackend{}
	f := &Firewall{StateDir: t.TempDir(), Default: "ufw", PanelPort: 28443,
		backends: map[string]fwBackend{helperapi.FirewallUFW: fake, helperapi.FirewallFirewalld: off, helperapi.FirewallNftables: &fakeBackend{}}}
	return f, fake
}

var fwAdmin = helperapi.Caller{Source: "web", Remote: "203.0.113.5"}

func TestFirewallConfirmAndRollback(t *testing.T) {
	f, b := newTestFirewall(t)
	ctx := context.Background()

	// Off: changes are only stored.
	ch, err := f.setRule(ctx, fwAdmin, helperapi.FirewallRule{Action: "allow", Protocol: "tcp", Ports: []string{"8080"}})
	if err != nil || ch.Pending != nil || !helperapi.ValidFirewallRuleID(ch.Rule.ID) {
		t.Fatalf("stored %+v %v", ch, err)
	}
	if len(b.applied) != 0 {
		t.Fatal("applied while off")
	}

	// Enable with confirmation, then confirm.
	p, err := f.Enable(ctx, fwAdmin, true, io.Discard)
	if err != nil || p == nil || p.Kind != "enable" {
		t.Fatalf("enable %+v %v", p, err)
	}
	r := b.last()
	if r.Rules[0].Owner != helperapi.OwnerSSH || r.Rules[1].Owner != helperapi.OwnerPanel || r.Rules[2].Ports[0] != "8080" {
		t.Fatalf("render %+v", r.Rules)
	}
	if _, err := f.setRule(ctx, fwAdmin, helperapi.FirewallRule{Action: "allow", Protocol: "tcp", Ports: []string{"9000"}}); !hasCode(err, helperapi.CodeFirewallPending) {
		t.Fatalf("second change while pending: %v", err)
	}
	if err := f.Confirm(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.Confirm(ctx, p.ID); !hasCode(err, helperapi.CodeFirewallNoPending) {
		t.Fatalf("confirm twice: %v", err)
	}

	// A change that is not confirmed goes away again; a managed rule
	// added meanwhile stays.
	ch, err = f.setAddress(ctx, fwAdmin, helperapi.FirewallAddress{Address: "198.51.100.7", Action: "deny"})
	if err != nil || ch.Pending == nil {
		t.Fatalf("address %+v %v", ch, err)
	}
	if err := f.SetManaged(ctx, io.Discard, "db:mysql", "tcp", []string{"3306"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Rollback(ctx, ch.Pending.ID); err != nil {
		t.Fatal(err)
	}
	st := f.Status(ctx)
	if len(st.Addresses) != 0 || st.Pending != nil || st.LastRollback == nil || st.LastRollback.Kind != "address.add" {
		t.Fatalf("after rollback %+v", st)
	}
	if !slices.ContainsFunc(st.Rules, func(r helperapi.FirewallRule) bool { return r.Owner == "db:mysql" }) {
		t.Fatalf("managed rule lost: %+v", st.Rules)
	}
	if last := b.last(); len(last.Addresses) != 0 {
		t.Fatalf("rollback did not re-apply: %+v", last)
	}
	// A rollback after the confirmation is a no-op.
	if err := f.Rollback(ctx, "x000000000000000"); err != nil {
		t.Fatal(err)
	}

	// Managed rules cannot be deleted, only narrowed.
	var managed helperapi.FirewallRule
	for _, r := range st.Rules {
		if r.Owner == "db:mysql" {
			managed = r
		}
	}
	if _, err := f.deleteRule(ctx, fwAdmin, helperapi.FirewallRef{ID: managed.ID}); !hasCode(err, helperapi.CodeFirewallManaged) {
		t.Fatalf("delete managed: %v", err)
	}
	ch, err = f.setRule(ctx, fwAdmin, helperapi.FirewallRule{ID: managed.ID, Action: "allow", Protocol: "udp", Ports: []string{"1"}, Sources: []string{"10.1.2.3/8"}})
	if err != nil || ch.Rule.Protocol != "tcp" || ch.Rule.Ports[0] != "3306" || ch.Rule.Sources[0] != "10.0.0.0/8" {
		t.Fatalf("narrow managed %+v %v", ch.Rule, err)
	}
	if err := f.Confirm(ctx, ch.Pending.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.SetManaged(ctx, io.Discard, "db:mysql", "", nil); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(f.Status(ctx).Rules, func(r helperapi.FirewallRule) bool { return r.Owner == "db:mysql" }) {
		t.Fatal("managed rule not removed")
	}
}

func TestFirewallTimerRollsBack(t *testing.T) {
	f, b := newTestFirewall(t)
	ctx := context.Background()
	b.on = true
	ch, err := f.pingForTest(ctx, true)
	if err != nil || ch.Pending == nil {
		t.Fatalf("%+v %v", ch, err)
	}
	// Fire the in-process timer at once.
	f.timers[ch.Pending.ID].Reset(time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for f.Status(ctx).Pending != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if st := f.Status(ctx); st.Pending != nil || st.BlockPing {
		t.Fatalf("not rolled back: %+v", st)
	}
}

// pingForTest changes ping through the registered code path.
func (f *Firewall) pingForTest(ctx context.Context, block bool) (helperapi.FirewallChange, error) {
	return f.change(ctx, fwAdmin, "ping", func(cfg *fwConfig) error {
		cfg.BlockPing = block
		return nil
	})
}

func TestFirewallChecks(t *testing.T) {
	f, b := newTestFirewall(t)
	ctx := context.Background()
	b.on = true
	confirm := func(ch helperapi.FirewallChange, err error) error {
		if err == nil && ch.Pending != nil {
			if cerr := f.Confirm(ctx, ch.Pending.ID); cerr != nil {
				t.Fatal(cerr)
			}
		}
		return err
	}
	for _, c := range []struct {
		name string
		do   func() error
		code string
	}{
		{"block yourself", func() error {
			return confirm(f.setAddress(ctx, fwAdmin, helperapi.FirewallAddress{Address: "203.0.113.0/24", Action: "deny"}))
		}, helperapi.CodeFirewallSelfBlock},
		{"deny your own SSH", func() error {
			return confirm(f.setRule(ctx, fwAdmin, helperapi.FirewallRule{Action: "deny", Protocol: "both", Ports: []string{"20-30"}, Sources: []string{"203.0.113.5"}}))
		}, helperapi.CodeFirewallSelfBlock},
		{"deny another port for yourself is fine", func() error {
			return confirm(f.setRule(ctx, fwAdmin, helperapi.FirewallRule{Action: "deny", Protocol: "tcp", Ports: []string{"8080"}, Sources: []string{"203.0.113.5"}}))
		}, ""},
		{"trusted network", func() error {
			return confirm(f.setAddress(ctx, fwAdmin, helperapi.FirewallAddress{Address: "10.0.0.0/8", Action: "allow"}))
		}, ""},
		{"blocked inside trusted", func() error {
			return confirm(f.setAddress(ctx, fwAdmin, helperapi.FirewallAddress{Address: "10.2.3.4", Action: "deny"}))
		}, helperapi.CodeFirewallOverlap},
		{"deny rule source inside trusted", func() error {
			return confirm(f.setRule(ctx, fwAdmin, helperapi.FirewallRule{Action: "deny", Protocol: "tcp", Ports: []string{"25"}, Sources: []string{"10.9.0.0/16"}}))
		}, helperapi.CodeFirewallOverlap},
		{"same trusted twice", func() error {
			return confirm(f.setAddress(ctx, fwAdmin, helperapi.FirewallAddress{Address: "10.0.0.0/8", Action: "allow"}))
		}, helperapi.CodeFirewallOverlap},
		{"deny without sources", func() error {
			return helperapi.FirewallRule{Action: "deny", Protocol: "tcp", Ports: []string{"25"}}.Validate()
		}, helperapi.CodeFirewallDenyNeedsSrc},
	} {
		if err := c.do(); c.code == "" && err != nil || c.code != "" && !hasCode(err, c.code) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// A failed apply leaves nothing pending and puts the old rules back.
	b.fail = true
	if _, err := f.setRule(ctx, fwAdmin, helperapi.FirewallRule{Action: "allow", Protocol: "tcp", Ports: []string{"81"}}); !hasCode(err, helperapi.CodeFirewallApplyFailed) {
		t.Fatalf("failed apply: %v", err)
	}
	if st := f.Status(ctx); st.Pending != nil || slices.ContainsFunc(st.Rules, func(r helperapi.FirewallRule) bool { return r.Ports[0] == "81" }) {
		t.Fatalf("failed apply kept: %+v", st)
	}
}

func hasCode(err error, code string) bool {
	he, ok := helperapi.AsError(err)
	return ok && he.Code == code
}

func sampleRender() fwRender {
	return fwRender{
		Rules: []helperapi.FirewallRule{
			{ID: "ssh", Action: "allow", Protocol: "tcp", Ports: []string{"22"}, Owner: "ssh"},
			{ID: "panel", Action: "allow", Protocol: "tcp", Ports: []string{"28443"}, Owner: "panel"},
			{ID: "r000000000000001", Action: "allow", Protocol: "tcp", Ports: []string{"80", "443"}},
			{ID: "r000000000000002", Action: "allow", Protocol: "both", Ports: []string{"8000-8100"}, Sources: []string{"10.0.0.0/8", "2001:db8::/32"}},
			{ID: "r000000000000003", Action: "deny", Protocol: "tcp", Ports: []string{"25"}, Sources: []string{"192.0.2.1"}},
		},
		Addresses: []helperapi.FirewallAddress{
			{ID: "a000000000000001", Address: "198.51.100.0/24", Action: "deny"},
			{ID: "a000000000000002", Address: "2001:db8:1::1", Action: "allow"},
		},
		BlockPing: true,
	}
}

func TestRenderNft(t *testing.T) {
	out := renderNft(sampleRender())
	for _, want := range []string{
		"table inet cairn\ndelete table inet cairn\ntable inet cairn {",
		"set deny4 {\n\t\ttype ipv4_addr\n\t\tflags interval\n\t\tauto-merge\n\t\telements = { 198.51.100.0/24 }",
		"set allow6 {\n\t\ttype ipv6_addr\n\t\tflags interval\n\t\tauto-merge\n\t\telements = { 2001:db8:1::1 }",
		"type filter hook input priority filter - 10; policy drop;",
		`ip saddr { 192.0.2.1 } tcp dport { 25 } drop comment "cairn:r000000000000003"`,
		"icmp type echo-request drop",
		"tcp dport { 22 } accept",
		"tcp dport { 80, 443 } accept",
		`ip saddr { 10.0.0.0/8 } meta l4proto { 6, 17 } th dport { 8000-8100 } accept`,
		`ip6 saddr { 2001:db8::/32 } meta l4proto { 6, 17 } th dport { 8000-8100 } accept`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// Deny rules come before ICMP and the allow rules.
	if strings.Index(out, "dport { 25 } drop") > strings.Index(out, "icmp type echo-request") ||
		strings.Index(out, "icmp type echo-request") > strings.Index(out, "dport { 22 } accept") {
		t.Errorf("order:\n%s", out)
	}
}

func TestRenderFirewalld(t *testing.T) {
	out, err := renderFirewalld(sampleRender())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		`<policy priority="-100" target="CONTINUE">`,
		`<ingress-zone name="ANY"></ingress-zone>`,
		`<egress-zone name="HOST"></egress-zone>`,
		`<icmp-block name="echo-request"></icmp-block>`,
		`<port port="22" protocol="tcp"></port>`,
		`<port port="443" protocol="tcp"></port>`,
		"<rule family=\"ipv4\" priority=\"-300\">\n    <source address=\"198.51.100.0/24\"></source>\n    <drop></drop>",
		"<rule family=\"ipv6\" priority=\"-200\">\n    <source address=\"2001:db8:1::1\"></source>\n    <accept></accept>",
		"<rule family=\"ipv4\" priority=\"-100\">\n    <source address=\"192.0.2.1\"></source>\n    <port port=\"25\" protocol=\"tcp\"></port>\n    <drop></drop>",
		"<rule family=\"ipv6\">\n    <source address=\"2001:db8::/32\"></source>\n    <port port=\"8000-8100\" protocol=\"udp\"></port>\n    <accept></accept>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	// Duplicates are dropped.
	r := sampleRender()
	r.Rules = append(r.Rules, helperapi.FirewallRule{ID: "r000000000000009", Action: "allow", Protocol: "tcp", Ports: []string{"28443"}})
	out, _ = renderFirewalld(r)
	if strings.Count(string(out), `port="28443"`) != 1 {
		t.Errorf("duplicate port:\n%s", out)
	}
}

func TestUFWCommandsAndTuples(t *testing.T) {
	cmds := ufwCommands(sampleRender())
	var lines []string
	for _, c := range cmds {
		if !strings.HasPrefix(c.tag, "cairn:") || c.args[len(c.args)-1] != c.tag {
			t.Fatalf("untagged %+v", c)
		}
		lines = append(lines, strings.Join(c.args[:len(c.args)-2], " "))
	}
	got := strings.Join(lines, "\n")
	want := strings.Join([]string{
		"prepend deny from 198.51.100.0/24",
		"prepend allow from 2001:db8:1::1",
		"allow proto tcp from any to any port 22",
		"allow proto tcp from any to any port 28443",
		"allow proto tcp from any to any port 80,443",
		"allow proto tcp from 10.0.0.0/8 to any port 8000:8100",
		"allow proto tcp from 2001:db8::/32 to any port 8000:8100",
		"allow proto udp from 10.0.0.0/8 to any port 8000:8100",
		"allow proto udp from 2001:db8::/32 to any port 8000:8100",
		"prepend deny proto tcp from 192.0.2.1 to any port 25",
	}, "\n")
	if got != want {
		t.Fatalf("commands:\n%s\nwant:\n%s", got, want)
	}
	// Long port lists are split into groups of 15.
	ports := []string{}
	for i := 1; i <= 14; i++ {
		ports = append(ports, "100"+string(rune('0'+i%10)))
	}
	if g := ufwPortGroups(append(ports, "2000-2010")); len(g) != 2 {
		t.Fatalf("groups %v", g)
	}

	tag := hex.EncodeToString([]byte("cairn:r1:abcd"))
	tuples := parseUFWTuples([]byte("### tuple ### allow tcp 3306 0.0.0.0/0 any 10.0.0.0/8 in comment="+tag+"\n"+
		"-A ufw-user-input -p tcp --dport 3306 -s 10.0.0.0/8 -j ACCEPT\n"+
		"### tuple ### deny any any 0.0.0.0/0 any 1.2.3.4 in\n"+
		"### tuple ### allow tcp 22 0.0.0.0/0 any 0.0.0.0/0 OpenSSH - in\n"), false)
	if len(tuples) != 3 || tuples[0].comment != "cairn:r1:abcd" {
		t.Fatalf("tuples %+v", tuples)
	}
	if a := strings.Join(tuples[0].deleteArgs(), " "); a != "--force delete allow proto tcp from 10.0.0.0/8 to any port 3306" {
		t.Fatalf("delete %s", a)
	}
	if a := strings.Join(tuples[1].deleteArgs(), " "); a != "--force delete deny from 1.2.3.4 to any" {
		t.Fatalf("delete %s", a)
	}
	if s := tuples[1].String(); s != "deny from 1.2.3.4" {
		t.Fatalf("string %s", s)
	}
}

func TestUFWPing(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "etc/ufw")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	before := "-A ufw-before-input -p icmp --icmp-type echo-request -j ACCEPT\n-A ufw-before-forward -p icmp --icmp-type echo-request -j ACCEPT\n"
	before6 := "-A ufw6-before-input -p icmpv6 --icmpv6-type echo-request -j ACCEPT\n-A ufw6-before-output -p icmpv6 --icmpv6-type echo-request -j ACCEPT\n"
	_ = os.WriteFile(filepath.Join(dir, "before.rules"), []byte(before), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "before6.rules"), []byte(before6), 0o600)
	u := &ufwBackend{root: root}
	if changed, err := u.setPing(true); err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "before.rules"))
	b6, _ := os.ReadFile(filepath.Join(dir, "before6.rules"))
	if !strings.Contains(string(b), "input -p icmp --icmp-type echo-request -j DROP") || !strings.Contains(string(b), "forward -p icmp --icmp-type echo-request -j ACCEPT") ||
		!strings.Contains(string(b6), "ufw6-before-input -p icmpv6 --icmpv6-type echo-request -j DROP") || !strings.Contains(string(b6), "output -p icmpv6 --icmpv6-type echo-request -j ACCEPT") {
		t.Fatalf("ping lines:\n%s\n%s", b, b6)
	}
	if changed, _ := u.setPing(true); changed {
		t.Fatal("changed twice")
	}
}

func TestParseSSHDPorts(t *testing.T) {
	if got := parseSSHDPorts("port 2222\nport 22\naddressfamily any\nlistenaddress [::]:22\n"); !slices.Equal(got, []int{22, 2222}) {
		t.Fatalf("%v", got)
	}
}
