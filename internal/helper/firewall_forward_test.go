// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"io"
	"os"
	"path/filepath"
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
		`tcp dport 8090 redirect to :80 comment "cairn:f000000000000001"`,
		`meta nfproto ipv4 tcp dport 5353 dnat ip to 10.0.0.5:53 comment "cairn:f000000000000002"`,
		`meta nfproto ipv4 udp dport 5353 dnat ip to 10.0.0.5:53 comment "cairn:f000000000000002"`,
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
}

func TestRenderFirewalldForwards(t *testing.T) {
	r := sampleRender()
	r.Forwards = sampleForwards
	main, err := renderFirewalld(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(main), `<forward-port port="8090" protocol="tcp" to-port="80"></forward-port>`) || strings.Contains(string(main), "to-addr") {
		t.Fatalf("main policy:\n%s", main)
	}
	fwd, err := renderFirewalldForward(r.Forwards)
	if err != nil {
		t.Fatal(err)
	}
	s := string(fwd)
	for _, want := range []string{
		`<policy priority="-100" target="CONTINUE">`,
		`<ingress-zone name="ANY"></ingress-zone>`,
		`<egress-zone name="ANY"></egress-zone>`,
		`<forward-port port="5353" protocol="tcp" to-port="53" to-addr="10.0.0.5"></forward-port>`,
		`<forward-port port="5353" protocol="udp" to-port="53" to-addr="10.0.0.5"></forward-port>`,
		"<rule family=\"ipv4\">\n    <destination address=\"10.0.0.5\"></destination>\n    <masquerade></masquerade>\n  </rule>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Count(s, "<masquerade>") != 1 {
		t.Errorf("one masquerade rule per address:\n%s", s)
	}
	if out, _ := renderFirewalldForward(sampleForwards[:1]); out != nil {
		t.Errorf("a policy without remote forwards:\n%s", out)
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
	if out := renderUFWForwardNAT(sampleForwards); !strings.HasPrefix(out, "#!/usr/sbin/nft -f") ||
		!strings.Contains(out, "table inet cairn_forward\ndelete table inet cairn_forward\ntable inet cairn_forward {") ||
		!strings.Contains(out, "tcp dport 8090 redirect to :80") {
		t.Fatalf("NAT table:\n%s", out)
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
	if _, err := f.Enable(ctx, fwAdmin, false, io.Discard); err != nil {
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
	if _, err := f.Enable(ctx, fwAdmin, false, io.Discard); err != nil {
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
