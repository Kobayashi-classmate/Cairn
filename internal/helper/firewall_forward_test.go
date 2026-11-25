// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

var sampleForwards = []helperapi.FirewallForward{
	{ID: "f000000000000001", Protocol: "tcp", Port: 8090, ToPort: 80},
	{ID: "f000000000000002", Protocol: "both", Port: 5353, ToAddress: "10.0.0.5", ToPort: 53},
}

func TestRenderNftForwards(t *testing.T) {
	r := sampleRender()
	r.Forwards = sampleForwards
	out := renderNft(r)
	for _, want := range []string{
		`ct status dnat tcp dport 80 accept comment "cairn:f000000000000001"`,
		"chain forward_prerouting {\n\t\ttype nat hook prerouting priority dstnat; policy accept;",
		`fib daddr type local tcp dport 8090 redirect to :80 comment "cairn:f000000000000001"`,
		`meta nfproto ipv4 fib daddr type local tcp dport 5353 dnat ip to 10.0.0.5:53 comment "cairn:f000000000000002"`,
		`meta nfproto ipv4 fib daddr type local udp dport 5353 dnat ip to 10.0.0.5:53 comment "cairn:f000000000000002"`,
		"chain forward_postrouting {\n\t\ttype nat hook postrouting priority srcnat; policy accept;",
		`ip daddr 10.0.0.5 udp dport 53 ct status dnat masquerade comment "cairn:f000000000000002"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// The forwarded port is let in after the allow rules, inside the input chain.
	input := out[:strings.Index(out, "chain forward_prerouting")]
	if strings.Index(input, "ct status dnat") < strings.Index(input, "dport { 80, 443 } accept") {
		t.Errorf("order:\n%s", out)
	}
	// Remote forwards only: there is no input line, and no NAT without forwards.
	if strings.Contains(renderNft(sampleRender()), "nat hook") {
		t.Error("NAT chains without forwards")
	}
	local := renderNft(fwRender{Forwards: sampleForwards[:1]})
	if strings.Contains(local, "postrouting") {
		t.Errorf("masquerading for a local forward:\n%s", local)
	}
	// Forwarding that Cairn did not switch on is not filtered.
	if strings.Contains(out, "hook forward") {
		t.Errorf("forward chain:\n%s", out)
	}
}

func TestRenderForwardTable(t *testing.T) {
	r := sampleRender()
	r.Forwards = sampleForwards
	// ufw and firewalld: the same NAT chains in a table of their own, with
	// a copy of the blocked addresses.
	out := renderForwardTable(r)
	for _, want := range []string{
		"#!/usr/sbin/nft -f\n",
		"table inet cairn_forward\ndelete table inet cairn_forward\ntable inet cairn_forward {",
		"set deny4 {\n\t\ttype ipv4_addr\n\t\tflags interval\n\t\tauto-merge\n\t\telements = { 198.51.100.0/24 }",
		"set deny6 {\n\t\ttype ipv6_addr\n",
		"\t\tip saddr @deny4 return\n\t\tip6 saddr @deny6 return\n",
		`fib daddr type local tcp dport 8090 redirect to :80 comment "cairn:f000000000000001"`,
		`meta nfproto ipv4 fib daddr type local udp dport 5353 dnat ip to 10.0.0.5:53 comment "cairn:f000000000000002"`,
		`ip daddr 10.0.0.5 tcp dport 53 ct status dnat masquerade comment "cairn:f000000000000002"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// The input rules and the forward chain stay with ufw and firewalld,
	// as do the trusted addresses.
	r.OwnForwarding = true
	if out := renderForwardTable(r); strings.Contains(out, "hook input") || strings.Contains(out, "hook forward") || strings.Contains(out, "allow") {
		t.Errorf("filter rules in the NAT table:\n%s", out)
	}

	// firewalld's policy has no forward-port entries any more: firewalld
	// would translate routed connections as well, for blocked addresses too.
	main, err := renderFirewalld(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(main), "forward-port") || strings.Contains(string(main), "8090") {
		t.Fatalf("main policy:\n%s", main)
	}
}

func TestUFWForwardBlock(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "etc/ufw")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	head := "*filter\n:ufw-before-input - [0:0]\n:ufw-before-forward - [0:0]\n# End required lines\n"
	tail := "\n-A ufw-before-input -i lo -j ACCEPT\nCOMMIT\n"
	head6 := strings.ReplaceAll(head, "ufw-", "ufw6-")
	_ = os.WriteFile(filepath.Join(dir, "before.rules"), []byte(head+tail), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "before6.rules"), []byte(head6+tail), 0o600)
	u := &ufwBackend{root: root}
	if changed, err := u.setForwardRules(sampleForwards); err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "before.rules"))
	b6, _ := os.ReadFile(filepath.Join(dir, "before6.rules"))
	want := head + ufwForwardBegin + "\n" +
		"-A ufw-before-input -p tcp --dport 80 -m conntrack --ctstate DNAT -j ACCEPT\n" +
		"-A ufw-before-forward -p tcp -d 10.0.0.5 --dport 53 -m conntrack --ctstate DNAT -j ACCEPT\n" +
		"-A ufw-before-forward -p udp -d 10.0.0.5 --dport 53 -m conntrack --ctstate DNAT -j ACCEPT\n" +
		ufwForwardEnd + "\n" + tail
	if string(b) != want {
		t.Fatalf("before.rules:\n%s\nwant:\n%s", b, want)
	}
	// IPv6: only the forward to this host.
	if !strings.Contains(string(b6), "-A ufw6-before-input -p tcp --dport 80 -m conntrack --ctstate DNAT -j ACCEPT\n"+ufwForwardEnd) {
		t.Fatalf("before6.rules:\n%s", b6)
	}
	if changed, _ := u.setForwardRules(sampleForwards); changed {
		t.Fatal("changed twice")
	}
	// Gone again: the files are as they were.
	if changed, err := u.setForwardRules(nil); err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "before.rules"))
	if string(b) != head+tail {
		t.Fatalf("after removal:\n%s", b)
	}
	// A before.rules without ufw's marker is not changed.
	_ = os.WriteFile(filepath.Join(dir, "before.rules"), []byte("*filter\nCOMMIT\n"), 0o600)
	if _, err := u.setForwardRules(sampleForwards); !hasCode(err, helperapi.CodeFirewallUnsupported) {
		t.Fatalf("no marker: %v", err)
	}
}

func TestForwardChecks(t *testing.T) {
	keep := []int{22, 28443}
	for _, c := range []struct {
		forwards []helperapi.FirewallForward
		code     string
	}{
		{sampleForwards, ""},
		// The SSH port would be taken from sshd; UDP 22 is fine.
		{[]helperapi.FirewallForward{{ID: "f1", Protocol: "tcp", Port: 22, ToPort: 2222}}, helperapi.CodeFirewallProtected},
		{[]helperapi.FirewallForward{{ID: "f1", Protocol: "both", Port: 28443, ToPort: 443}}, helperapi.CodeFirewallProtected},
		{[]helperapi.FirewallForward{{ID: "f1", Protocol: "udp", Port: 22, ToPort: 53}}, ""},
		// One forward per port and protocol.
		{[]helperapi.FirewallForward{{ID: "f1", Protocol: "tcp", Port: 80, ToPort: 8080}, {ID: "f2", Protocol: "both", Port: 80, ToPort: 8081}}, helperapi.CodeFirewallForwardTaken},
		{[]helperapi.FirewallForward{{ID: "f1", Protocol: "tcp", Port: 80, ToPort: 8080}, {ID: "f2", Protocol: "udp", Port: 80, ToPort: 8081}}, ""},
	} {
		err := checkForwards(c.forwards, keep)
		if (c.code == "" && err != nil) || (c.code != "" && !hasCode(err, c.code)) {
			t.Errorf("%+v: %v, want %q", c.forwards, err, c.code)
		}
	}
	for _, c := range []struct {
		fw   helperapi.FirewallForward
		rule string
	}{
		{helperapi.FirewallForward{Protocol: "tcp", Port: 80, ToPort: 80}, "same_port"},
		{helperapi.FirewallForward{Protocol: "tcp", Port: 80, ToAddress: "127.0.0.1", ToPort: 80}, "forward_target"},
		{helperapi.FirewallForward{Protocol: "tcp", Port: 80, ToAddress: "2001:db8::1", ToPort: 80}, "forward_target"},
		{helperapi.FirewallForward{Protocol: "tcp", Port: 80, ToAddress: "10.0.0.5/24", ToPort: 80}, "forward_target"},
		{helperapi.FirewallForward{Protocol: "icmp", Port: 80, ToPort: 81}, "oneof"},
		{helperapi.FirewallForward{Protocol: "tcp", Port: 0, ToPort: 81}, "port"},
		{helperapi.FirewallForward{Protocol: "tcp", Port: 80, ToPort: 70000}, "port"},
	} {
		err := c.fw.Validate()
		if he, ok := helperapi.AsError(err); !ok || he.Params["rule"] != c.rule {
			t.Errorf("%+v: %v, want %s", c.fw, err, c.rule)
		}
	}
	if err := (helperapi.FirewallForward{Protocol: "tcp", Port: 80, ToAddress: "10.0.0.5", ToPort: 80}).Validate(); err != nil {
		t.Errorf("same port on another host: %v", err)
	}
}

// ipForwardRoot gives the firewall a root with a kernel setting file.
func ipForwardRoot(t *testing.T, f *Firewall, value string) string {
	t.Helper()
	f.Root = t.TempDir()
	proc := filepath.Join(f.Root, ipForwardProc)
	if err := os.MkdirAll(filepath.Dir(proc), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proc, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return proc
}

func readTrim(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func TestFirewallForwardsAndIPForwarding(t *testing.T) {
	f, b := newTestFirewall(t)
	ctx := context.Background()
	proc := ipForwardRoot(t, f, "0")
	conf := filepath.Join(f.Root, ipForwardConf)
	if _, err := f.Enable(ctx, fwSetup, false, io.Discard); err != nil {
		t.Fatal(err)
	}

	// A forward to this host needs no IP forwarding.
	ch, err := f.setForward(ctx, fwAdmin, helperapi.FirewallForward{Protocol: "tcp", Port: 8090, ToPort: 80})
	if err != nil || ch.Pending == nil || !helperapi.ValidFirewallForwardID(ch.Forward.ID) {
		t.Fatalf("add %+v %v", ch, err)
	}
	if got := b.last().Forwards; len(got) != 1 || got[0].Port != 8090 {
		t.Fatalf("render %+v", got)
	}
	if readTrim(t, proc) != "0" || fileExists(conf) {
		t.Fatal("forwarding switched on for a local forward")
	}
	if err := f.Confirm(ctx, ch.Pending.ID); err != nil {
		t.Fatal(err)
	}

	// A forward to another host switches it on, also across reboots.
	ch, err = f.setForward(ctx, fwAdmin, helperapi.FirewallForward{Protocol: "tcp", Port: 8091, ToAddress: "10.0.0.5", ToPort: 80})
	if err != nil {
		t.Fatal(err)
	}
	if readTrim(t, proc) != "1" || !strings.Contains(readTrim(t, conf), "net.ipv4.ip_forward = 1") {
		t.Fatal("forwarding not switched on")
	}
	// Not confirmed: the rollback switches it off again.
	if err := f.Rollback(ctx, ch.Pending.ID); err != nil {
		t.Fatal(err)
	}
	if readTrim(t, proc) != "0" || fileExists(conf) || len(b.last().Forwards) != 1 {
		t.Fatalf("after rollback: %s %v %+v", readTrim(t, proc), fileExists(conf), b.last().Forwards)
	}

	// The same port twice, and the panel port, are refused.
	if _, err := f.setForward(ctx, fwAdmin, helperapi.FirewallForward{Protocol: "both", Port: 8090, ToPort: 81}); !hasCode(err, helperapi.CodeFirewallForwardTaken) {
		t.Fatalf("taken: %v", err)
	}
	if _, err := f.setForward(ctx, fwAdmin, helperapi.FirewallForward{Protocol: "tcp", Port: 28443, ToPort: 81}); !hasCode(err, helperapi.CodeFirewallProtected) {
		t.Fatalf("panel port: %v", err)
	}

	// Change and delete.
	id := b.last().Forwards[0].ID
	ch, err = f.setForward(ctx, fwAdmin, helperapi.FirewallForward{ID: id, Protocol: "udp", Port: 8090, ToPort: 81})
	if err != nil || b.last().Forwards[0].Protocol != "udp" {
		t.Fatalf("update %+v %v", ch, err)
	}
	_ = f.Confirm(ctx, ch.Pending.ID)
	ch, err = f.deleteForward(ctx, fwAdmin, helperapi.FirewallRef{ID: id})
	if err != nil || len(b.last().Forwards) != 0 {
		t.Fatalf("delete %+v %v", ch, err)
	}
	_ = f.Confirm(ctx, ch.Pending.ID)
	if _, err := f.deleteForward(ctx, fwAdmin, helperapi.FirewallRef{ID: id}); !hasCode(err, helperapi.CodeFirewallNotFound) {
		t.Fatalf("gone: %v", err)
	}
	if st := f.Status(ctx); st.Forwards == nil || len(st.Forwards) != 0 || st.IPForward {
		t.Fatalf("status %+v", st)
	}
}

func TestIPForwardKeepsOthersOn(t *testing.T) {
	f, _ := newTestFirewall(t)
	// Already on (Docker, say): Cairn leaves it on when its forwards go.
	proc := ipForwardRoot(t, f, "1")
	if err := f.ipForward(io.Discard, true); err != nil {
		t.Fatal(err)
	}
	if err := f.ipForward(io.Discard, false); err != nil {
		t.Fatal(err)
	}
	if readTrim(t, proc) != "1" {
		t.Fatal("switched off what Cairn did not switch on")
	}
	// Switched on by Cairn, but a container bridge appeared since: it stays on.
	_ = os.WriteFile(proc, []byte("0\n"), 0o600)
	if err := f.ipForward(io.Discard, true); err != nil || readTrim(t, proc) != "1" {
		t.Fatalf("on: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(f.Root, "/sys/class/net/docker0"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := f.ipForward(io.Discard, false); err != nil || readTrim(t, proc) != "1" {
		t.Fatalf("docker: %v %s", err, readTrim(t, proc))
	}
	if fileExists(f.stateDir(ipForwardMark)) {
		t.Fatal("mark kept")
	}
}

func TestFirewallRollbackNow(t *testing.T) {
	f, b := newTestFirewall(t)
	ctx := context.Background()
	if _, err := f.Enable(ctx, fwSetup, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	ch, err := f.setForward(ctx, fwAdmin, helperapi.FirewallForward{Protocol: "tcp", Port: 8090, ToPort: 80})
	if err != nil || ch.Pending == nil {
		t.Fatalf("%+v %v", ch, err)
	}
	if err := f.RollbackNow(ctx, ch.Pending.ID); err != nil {
		t.Fatal(err)
	}
	st := f.Status(ctx)
	if st.Pending != nil || len(st.Forwards) != 0 || len(b.last().Forwards) != 0 || st.LastRollback == nil || !st.LastRollback.Requested {
		t.Fatalf("after rollback %+v", st)
	}
	if _, ok := f.timers[ch.Pending.ID]; ok {
		t.Fatal("timer left")
	}
	// Nothing pending (any more): refused, unlike the timer's quiet no-op.
	if err := f.RollbackNow(ctx, ch.Pending.ID); !hasCode(err, helperapi.CodeFirewallNoPending) {
		t.Fatalf("again: %v", err)
	}
	if err := f.Rollback(ctx, ch.Pending.ID); err != nil {
		t.Fatalf("timer after: %v", err)
	}
}

// prerouting returns the NAT chain that translates destinations.
func prerouting(t *testing.T, table string) string {
	t.Helper()
	i := strings.Index(table, "chain forward_prerouting {")
	if i < 0 {
		t.Fatalf("no prerouting chain:\n%s", table)
	}
	return table[i : i+strings.Index(table[i:], "\t}\n")]
}

// Routed traffic (a container's connection to another host, a neighbor
// using this host as its gateway) keeps its destination: only connections
// addressed to this host are forwarded, by every backend.
func TestForwardNATOnlyForThisHost(t *testing.T) {
	r := fwRender{
		Rules:     []helperapi.FirewallRule{{ID: "ssh", Action: "allow", Protocol: "tcp", Ports: []string{"22"}, Owner: "ssh"}},
		Addresses: []helperapi.FirewallAddress{{Address: "198.51.100.7", Action: "deny"}},
		Forwards: []helperapi.FirewallForward{
			{ID: "f000000000000001", Protocol: "tcp", Port: 8443, ToPort: 443},
			{ID: "f000000000000002", Protocol: "tcp", Port: 443, ToAddress: "10.0.0.5", ToPort: 443},
		},
	}
	for name, table := range map[string]string{"nftables": renderNft(r), "ufw and firewalld": renderForwardTable(r)} {
		pre := prerouting(t, table)
		n := 0
		for _, l := range strings.Split(pre, "\n") {
			if strings.Contains(l, "redirect") || strings.Contains(l, "dnat") {
				n++
				if !strings.Contains(l, "fib daddr type local ") {
					t.Errorf("%s: translates routed traffic too: %q", name, l)
				}
			}
		}
		if n != 2 {
			t.Errorf("%s: %d translations:\n%s", name, n, pre)
		}
		// Blocked addresses are not forwarded: without the translation their
		// connections meet the blocked addresses' drop rule.
		if !strings.Contains(pre, "ip saddr @deny4 return\n\t\tip6 saddr @deny6 return\n\t\tfib daddr type local tcp dport 8443 redirect") {
			t.Errorf("%s: blocked addresses are forwarded:\n%s", name, pre)
		}
	}
}

// A deny rule keeps its sources from the forwards on a port it denies and
// from the forwards to this host whose target port it denies; other deny
// rules, allow rules and trusted addresses leave the forwards alone.
func TestForwardNATLeavesDeniedSources(t *testing.T) {
	deny := func(id, proto string, ports, sources []string) helperapi.FirewallRule {
		return helperapi.FirewallRule{ID: id, Action: "deny", Protocol: proto, Ports: ports, Sources: sources}
	}
	r := fwRender{
		Rules: []helperapi.FirewallRule{
			deny("r000000000000001", "tcp", []string{"8090"}, []string{"192.0.2.1", "2001:db8::/32"}),
			// The target port of the forward to this host.
			deny("r000000000000002", "tcp", []string{"70-90"}, []string{"192.0.2.2"}),
			deny("r000000000000003", "udp", []string{"5353"}, []string{"192.0.2.3"}),
			// The target port of a forward to another host is that host's.
			deny("r000000000000004", "tcp", []string{"53"}, []string{"192.0.2.4"}),
			deny("r000000000000005", "tcp", []string{"25"}, []string{"192.0.2.5"}),
			// UDP 8090 is not forwarded.
			deny("r000000000000006", "udp", []string{"8090"}, []string{"192.0.2.6"}),
			deny("r000000000000007", "both", []string{"5353", "8090"}, []string{"192.0.2.7"}),
			{ID: "r000000000000008", Action: "allow", Protocol: "tcp", Ports: []string{"8090"}, Sources: []string{"192.0.2.8"}},
		},
		Addresses: []helperapi.FirewallAddress{{Address: "192.0.2.9", Action: "allow"}},
		Forwards:  sampleForwards,
	}
	for name, table := range map[string]string{"nftables": renderNft(r), "ufw and firewalld": renderForwardTable(r)} {
		pre := prerouting(t, table)
		firstNAT := strings.Index(pre, "fib daddr type local")
		for _, want := range []string{
			`ip saddr { 192.0.2.1 } tcp dport { 8090 } return comment "cairn:r000000000000001"`,
			`ip6 saddr { 2001:db8::/32 } tcp dport { 8090 } return comment "cairn:r000000000000001"`,
			`ip saddr { 192.0.2.2 } tcp dport { 8090 } return comment "cairn:r000000000000002"`,
			`ip saddr { 192.0.2.3 } udp dport { 5353 } return comment "cairn:r000000000000003"`,
			`ip saddr { 192.0.2.7 } tcp dport { 8090, 5353 } return comment "cairn:r000000000000007"`,
			`ip saddr { 192.0.2.7 } udp dport { 5353 } return comment "cairn:r000000000000007"`,
		} {
			if i := strings.Index(pre, want); i < 0 || i > firstNAT {
				t.Errorf("%s: missing %q before the translations in\n%s", name, want, pre)
			}
		}
		for _, other := range []string{"192.0.2.4", "192.0.2.5", "192.0.2.6", "192.0.2.8", "192.0.2.9"} {
			if strings.Contains(pre, other) {
				t.Errorf("%s: %s keeps others from the forwards:\n%s", name, other, pre)
			}
		}
	}
	// No forwards, no NAT chains.
	r.Forwards = nil
	if out := renderNft(r); strings.Contains(out, "nat hook") {
		t.Errorf("NAT without forwards:\n%s", out)
	}
}

// The forward chain is there while Cairn switched forwarding on: it lets
// the forwarded connections, their replies and container and VM networks
// through and drops other routed IPv4 packets, without a drop policy that
// would drop Docker's traffic too.
func TestNftForwardChain(t *testing.T) {
	r := sampleRender()
	r.Forwards = sampleForwards
	r.OwnForwarding = true
	out := renderNft(r)
	i := strings.Index(out, "\tchain forward {\n")
	if i < 0 {
		t.Fatalf("no forward chain:\n%s", out)
	}
	chain := out[i : i+strings.Index(out[i:], "\t}\n")]
	want := "\tchain forward {\n" +
		"\t\ttype filter hook forward priority filter - 10; policy accept;\n" +
		"\t\tct state established,related accept\n" +
		"\t\tct status dnat accept\n" +
		"\t\tmeta iifkind { \"bridge\", \"veth\", \"vxlan\", \"geneve\", \"openvswitch\" } accept\n" +
		"\t\tmeta oifkind { \"bridge\", \"veth\", \"vxlan\", \"geneve\", \"openvswitch\" } accept\n" +
		"\t\tmeta nfproto ipv4 drop\n"
	if chain != want {
		t.Fatalf("forward chain:\n%s\nwant:\n%s", chain, want)
	}
	// Without it the table is what it was.
	r.OwnForwarding = false
	if out := renderNft(r); strings.Contains(out, "hook forward") {
		t.Errorf("forward chain:\n%s", out)
	}
}

// The forward chain comes with forwarding that Cairn switches on, never with
// forwarding something else (Docker, libvirt) switched on before.
func TestForwardChainOnlyWhenCairnForwards(t *testing.T) {
	remote := []helperapi.FirewallForward{{ID: "f000000000000001", Protocol: "tcp", Port: 8091, ToAddress: "10.0.0.5", ToPort: 80}}
	local := []helperapi.FirewallForward{{ID: "f000000000000002", Protocol: "tcp", Port: 8090, ToPort: 80}}
	for _, c := range []struct {
		name     string
		before   string
		mark     bool
		forwards []helperapi.FirewallForward
		own      bool
		after    string
	}{
		{"off: Cairn switches it on", "0", false, remote, true, "1"},
		{"switched on by Cairn earlier", "1", true, remote, true, "1"},
		{"switched on by something else", "1", false, remote, false, "1"},
		{"forwards to this host only", "0", false, local, false, "0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, b := newTestFirewall(t)
			proc := ipForwardRoot(t, f, c.before)
			if c.mark {
				if err := os.MkdirAll(f.stateDir(), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.stateDir(ipForwardMark), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.backend(helperapi.FirewallUFW).apply(context.Background(), io.Discard, fwRender{Forwards: c.forwards}); err != nil {
				t.Fatal(err)
			}
			if got := b.last().OwnForwarding; got != c.own {
				t.Fatalf("own forwarding %v, want %v", got, c.own)
			}
			if got := strings.Contains(renderNft(b.last()), "hook forward"); got != c.own {
				t.Fatalf("forward chain %v, want %v", got, c.own)
			}
			if got := readTrim(t, proc); got != c.after {
				t.Fatalf("forwarding %s, want %s", got, c.after)
			}
		})
	}
}

// fakeTools makes the firewall commands answer without running anything:
// firewalld runs, the NAT table is loaded once loaded; the commands run
// are recorded.
func fakeTools(t *testing.T, root string) *[]string {
	t.Helper()
	oldRun, oldQuery, oldSystemd := runCommand, queryCommand, systemdRunning
	t.Cleanup(func() { runCommand, queryCommand, systemdRunning = oldRun, oldQuery, oldSystemd })
	systemdRunning = func() bool { return true }
	for _, p := range []string{firewallCmd, nftBinary} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var calls []string
	loaded := false
	queryCommand = func(_ context.Context, name string, args ...string) (string, error) {
		switch {
		case name == firewallCmd && args[0] == "--state":
			return "running\n", nil
		case name == firewallCmd && args[0] == "--version":
			return "1.3.4\n", nil
		case name == nftBinary && !loaded:
			return "", os.ErrNotExist
		}
		return "", nil
	}
	runCommand = func(_ context.Context, _ io.Writer, name string, args ...string) error {
		call := strings.TrimSpace(name + " " + strings.Join(args, " "))
		calls = append(calls, call)
		switch call {
		case nftBinary + " -f " + filepath.Join(root, forwardTableFile):
			loaded = true
		case nftBinary + " delete table inet " + forwardTable:
			loaded = false
		}
		return nil
	}
	return &calls
}

// firewalld forwards through Cairn's NAT table: its policy loses the
// forward-port entries of earlier versions, the table is loaded and kept
// loaded at boot, and it goes when firewalld is switched off.
func TestFirewalldForwardsInNATTable(t *testing.T) {
	root := t.TempDir()
	calls := fakeTools(t, root)
	old := filepath.Join(root, firewalldForwardPolicyFile)
	if err := os.MkdirAll(filepath.Dir(old), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("<policy><forward-port port=\"8091\" protocol=\"tcp\" to-port=\"80\" to-addr=\"10.0.0.5\"/></policy>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &firewalldBackend{root: root}
	r := sampleRender()
	r.Forwards = sampleForwards
	if d.current(context.Background(), r) {
		t.Fatal("current before the apply")
	}
	if err := d.apply(context.Background(), io.Discard, r); err != nil {
		t.Fatal(err)
	}
	if fileExists(old) {
		t.Error("the forwards' firewalld policy is still there")
	}
	if !d.current(context.Background(), r) {
		t.Error("not current after the apply")
	}
	if other := (fwRender{Rules: r.Rules, Forwards: r.Forwards}); d.current(context.Background(), other) {
		t.Error("current for other rules")
	}
	main, err := os.ReadFile(filepath.Join(root, firewalldPolicyFile))
	if err != nil || strings.Contains(string(main), "forward-port") {
		t.Fatalf("policy %v:\n%s", err, main)
	}
	table, err := os.ReadFile(filepath.Join(root, forwardTableFile))
	if err != nil || !strings.Contains(string(table), "fib daddr type local tcp dport 8090 redirect to :80") {
		t.Fatalf("NAT table %v:\n%s", err, table)
	}
	got := strings.Join(*calls, "\n")
	file := filepath.Join(root, forwardTableFile)
	want := strings.Join([]string{firewallCmd + " --check-config", firewallCmd + " --reload",
		nftBinary + " -c -f " + file, nftBinary + " -f " + file, "systemctl enable " + forwardTableUnit}, "\n")
	if got != want {
		t.Fatalf("commands:\n%s\nwant:\n%s", got, want)
	}

	// The forwards go: so does the table.
	*calls = nil
	r.Forwards = nil
	if err := d.apply(context.Background(), io.Discard, r); err != nil {
		t.Fatal(err)
	}
	if fileExists(file) || !slices.Contains(*calls, nftBinary+" delete table inet "+forwardTable) {
		t.Fatalf("table left: %q", *calls)
	}

	// Switched off with forwards: the table goes with firewalld.
	r.Forwards = sampleForwards
	if err := d.apply(context.Background(), io.Discard, r); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if err := d.disable(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	want = strings.Join([]string{"systemctl disable --now firewalld", "systemctl disable " + forwardTableUnit,
		nftBinary + " delete table inet " + forwardTable}, "\n")
	if got := strings.Join(*calls, "\n"); got != want || fileExists(file) {
		t.Fatalf("disable:\n%s\nwant:\n%s", got, want)
	}
}

// ufw loads the same table, with the blocked addresses ufw has in its own
// rules, so that their connections are not forwarded past those rules. A
// table that does not load leaves the one before in place.
func TestUFWForwardTableHasBlockedAddresses(t *testing.T) {
	root := t.TempDir()
	fakeTools(t, root)
	r := sampleRender()
	r.Forwards = sampleForwards
	if err := setForwardTable(context.Background(), io.Discard, root, r); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, forwardTableFile)
	table, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(table), "elements = { 198.51.100.0/24 }") || !strings.Contains(string(table), "ip saddr @deny4 return") {
		t.Fatalf("NAT table %v:\n%s", err, table)
	}
	// The deny rule for port 25 has nothing to do with the forwards.
	if strings.Contains(string(table), "192.0.2.1") {
		t.Fatalf("unrelated deny rule:\n%s", table)
	}
	runCommand = func(context.Context, io.Writer, string, ...string) error { return os.ErrPermission }
	r.Addresses = nil
	if err := setForwardTable(context.Background(), io.Discard, root, r); err == nil {
		t.Fatal("no error")
	}
	if again, _ := os.ReadFile(p); string(again) != string(table) {
		t.Fatalf("table changed:\n%s", again)
	}
}
