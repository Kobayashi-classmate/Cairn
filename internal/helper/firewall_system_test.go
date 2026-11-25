// SPDX-License-Identifier: AGPL-3.0-only

//go:build fwsystem

package helper

// System tests of the firewall backends, run inside throwaway containers
// by test/firewall/run.sh (never on a development machine): each phase
// changes the real firewall; the script checks from another container
// what is reachable in between.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

const fwStateDir = "/var/lib/cairn-fwtest"

func systemFirewall(t *testing.T) *Firewall {
	t.Helper()
	backend := os.Getenv("CAIRN_FW_BACKEND")
	def := backend
	if backend == helperapi.FirewallNftables {
		def = "none"
	}
	old := sshDetect
	t.Cleanup(func() { sshDetect = old })
	sshDetect = func(context.Context, string) sshEndpoints { return sshEndpoints{Ports: []int{22}, Installed: true} }
	f := &Firewall{StateDir: fwStateDir, Default: def, PanelPort: 28443}
	// Each phase is a process of its own, as the rollback timer's is: the
	// minute a change waits passes on the firewall's clock.
	fakeClock(t, f)
	return f
}

// breakTool makes a backend's command fail until the returned function is
// called.
func breakTool(t *testing.T, bin string) func() {
	t.Helper()
	if err := os.Rename(bin, bin+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'broken for the test' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Rename(bin+".real", bin); err != nil {
			t.Fatal(err)
		}
	}
}

func sh(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// rules shows what the backend enforces now, with the port forwards' NAT
// table of ufw and firewalld.
func rules(t *testing.T) string {
	nat, _ := exec.Command(nftBinary, "list", "table", "inet", forwardTable).CombinedOutput()
	switch os.Getenv("CAIRN_FW_BACKEND") {
	case helperapi.FirewallNftables:
		out, _ := exec.Command(nftBinary, "list", "ruleset").CombinedOutput()
		return string(out)
	case helperapi.FirewallUFW:
		out, _ := exec.Command(ufwBinary, "status", "verbose").CombinedOutput()
		before, _ := os.ReadFile("/etc/ufw/before.rules")
		return string(out) + "\n" + string(before) + "\n" + string(nat)
	case helperapi.FirewallFirewalld:
		out, _ := exec.Command(firewallCmd, "--info-policy=cairn").CombinedOutput()
		return string(out) + "\n" + string(nat)
	}
	return ""
}

func want(t *testing.T, has bool, parts ...string) {
	t.Helper()
	r := rules(t)
	for _, p := range parts {
		if strings.Contains(r, p) != has {
			t.Fatalf("rules contain %q = %v, want %v:\n%s", p, !has, has, r)
		}
	}
}

func savePending(t *testing.T, p *helperapi.FirewallPending) {
	data, _ := json.Marshal(p)
	if err := os.WriteFile(fwStateDir+"/test-pending.json", data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func loadPending(t *testing.T) helperapi.FirewallPending {
	var p helperapi.FirewallPending
	data, err := os.ReadFile(fwStateDir + "/test-pending.json")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(data, &p)
	return p
}

func TestFirewallSystem(t *testing.T) {
	f := systemFirewall(t)
	ctx := context.Background()
	admin := helperapi.Caller{Source: "web", Remote: "192.0.2.200"}
	backend := os.Getenv("CAIRN_FW_BACKEND")
	ufw := backend == helperapi.FirewallUFW
	firewalld := backend == helperapi.FirewallFirewalld
	client := os.Getenv("CAIRN_FW_CLIENT")
	switch phase := os.Getenv("CAIRN_FW_PHASE"); phase {
	case "setup":
		plan, err := f.Plan(ctx, nil)
		if err != nil || plan.Backend != backend || strings.Join(plan.Ports, " ") != "22/tcp 28443/tcp 80/tcp 443/tcp" {
			t.Fatalf("plan %+v %v", plan, err)
		}
		if p, err := f.Setup(ctx, os.Stdout, FirewallSetup{}); err != nil || p != nil {
			t.Fatalf("%+v %v", p, err)
		}
		st := f.Status(ctx)
		if !st.Enabled || !st.Loaded || st.Backend != backend || st.Pending != nil {
			t.Fatalf("status %+v", st)
		}
		switch {
		case ufw:
			want(t, true, "Status: active", "Default: deny (incoming)", "22/tcp", "28443/tcp", "80,443/tcp")
		case firewalld:
			want(t, true, "ports: 22/tcp 28443/tcp 80/tcp 443/tcp")
		default:
			want(t, true, "table inet cairn", "policy drop", "tcp dport 22 accept", "tcp dport { 80, 443 } accept")
		}
	case "allow8081":
		ch, err := f.setRule(ctx, admin, helperapi.FirewallRule{Action: "allow", Protocol: "tcp", Ports: []string{"8081"}, Note: "test"})
		if err != nil || ch.Pending == nil {
			t.Fatalf("%+v %v", ch, err)
		}
		if err := f.Confirm(ctx, ch.Pending.ID); err != nil {
			t.Fatal(err)
		}
		if ufw {
			want(t, true, "8081/tcp")
		} else {
			want(t, true, "tcp dport 8081 accept")
		}
	case "blockclient":
		ch, err := f.setAddress(ctx, admin, helperapi.FirewallAddress{Address: client, Action: "deny", Note: "test"})
		if err != nil || ch.Pending == nil {
			t.Fatalf("%+v %v", ch, err)
		}
		savePending(t, ch.Pending)
		if ufw {
			want(t, true, "DENY IN     "+client)
		} else {
			want(t, true, "elements = { "+client+" }")
		}
	case "rollback":
		p := loadPending(t)
		// A separate process runs the rollback, as the timer does.
		if err := f.Rollback(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
		st := f.Status(ctx)
		if st.Pending != nil || len(st.Addresses) != 0 || st.LastRollback == nil {
			t.Fatalf("status %+v", st)
		}
		want(t, false, client)
	case "unconfirmed":
		// A change nobody confirms; the process that made it goes away with
		// its timer, as a machine that is restarted does.
		ch, err := f.setRule(ctx, admin, helperapi.FirewallRule{Action: "allow", Protocol: "tcp", Ports: []string{"8082"}, Note: "test"})
		if err != nil || ch.Pending == nil {
			t.Fatalf("%+v %v", ch, err)
		}
		if ufw {
			want(t, true, "8082/tcp")
		} else {
			want(t, true, "tcp dport 8082 accept")
		}
	case "recover":
		// What runs at boot: the change expired meanwhile and is undone.
		if st := f.Status(ctx); st.Pending == nil {
			t.Fatalf("nothing pending: %+v", st)
		}
		fwSleep(2 * time.Minute)
		if err := f.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		st := f.Status(ctx)
		if st.Pending != nil || st.LastRollback == nil || st.LastRollback.Kind != "rule.add" {
			t.Fatalf("status %+v", st)
		}
		want(t, false, "8082")
	case "rollback-fails":
		// The backend's command fails while a change is rolled back: the
		// change stays pending with the failure on record, and a later
		// rollback that works ends it.
		ch, err := f.setRule(ctx, admin, helperapi.FirewallRule{Action: "allow", Protocol: "tcp", Ports: []string{"8084"}, Note: "test"})
		if err != nil || ch.Pending == nil {
			t.Fatalf("%+v %v", ch, err)
		}
		tool := nftBinary
		if ufw {
			tool = ufwBinary
		}
		repair := breakTool(t, tool)
		rerr := f.Rollback(ctx, ch.Pending.ID)
		repair()
		if !hasCode(rerr, helperapi.CodeFirewallRollbackFailed) {
			t.Fatalf("rollback with a broken %s: %v", tool, rerr)
		}
		cfg, err := f.load()
		if err != nil || cfg.Pending == nil || cfg.Pending.RollbackFailed == nil || cfg.Pending.RollbackFailed.Attempts != 1+len(fwRollbackRetry) || cfg.LastRollback != nil && cfg.LastRollback.ID == ch.Pending.ID {
			t.Fatalf("state after the failed rollback: %+v %v", cfg.Pending, err)
		}
		want(t, true, "8084")
		if err := f.RollbackNow(ctx, ch.Pending.ID); err != nil {
			t.Fatal(err)
		}
		if st := f.Status(ctx); st.Pending != nil || st.LastRollback == nil || st.LastRollback.ID != ch.Pending.ID {
			t.Fatalf("status %+v", st)
		}
		want(t, false, "8084")
	case "managed":
		if err := f.SetManaged(ctx, os.Stdout, "db:mysql", "tcp", []string{"3306"}); err != nil {
			t.Fatal(err)
		}
		if ufw {
			want(t, true, "3306/tcp")
		} else {
			want(t, true, "tcp dport 3306 accept")
		}
		if err := f.SetManaged(ctx, os.Stdout, "db:mysql", "", nil); err != nil {
			t.Fatal(err)
		}
		if ufw {
			want(t, false, "3306/tcp")
		} else {
			want(t, false, "3306")
		}
	case "ping":
		ch, err := f.change(ctx, admin, "ping", func(cfg *fwConfig) error { cfg.BlockPing = true; return nil })
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Confirm(ctx, ch.Pending.ID); err != nil {
			t.Fatal(err)
		}
		if ufw {
			want(t, true, "-A ufw-before-input -p icmp --icmp-type echo-request -j DROP")
		} else {
			want(t, true, "icmp type echo-request drop")
		}
	case "forward":
		// 8090 to this host's port 80, 8091 to port 80 of another host.
		target := os.Getenv("CAIRN_FW_TARGET")
		for _, fw := range []helperapi.FirewallForward{
			{Protocol: "tcp", Port: 8090, ToPort: 80, Note: "test"},
			{Protocol: "tcp", Port: 8091, ToAddress: target, ToPort: 80, Note: "test"},
		} {
			ch, err := f.setForward(ctx, admin, fw)
			if err != nil || ch.Pending == nil {
				t.Fatalf("%+v %v", ch, err)
			}
			if err := f.Confirm(ctx, ch.Pending.ID); err != nil {
				t.Fatal(err)
			}
		}
		if st := f.Status(ctx); len(st.Forwards) != 2 || !st.IPForward {
			t.Fatalf("status %+v", st)
		}
		// Only what is addressed to this host is forwarded, and not for
		// blocked addresses.
		want(t, true, "redirect to :80", "dnat ip to "+target+":80", "ct status dnat masquerade",
			"fib daddr type local tcp dport 8090", "ip saddr @deny4 return")
		switch {
		case ufw:
			want(t, true, "-A ufw-before-input -p tcp --dport 80 -m conntrack --ctstate DNAT -j ACCEPT",
				"-A ufw-before-forward -p tcp -d "+target+" --dport 80 -m conntrack --ctstate DNAT -j ACCEPT")
		case firewalld:
			want(t, false, "toport=")
		default:
			// Forwarding was on before Cairn: Cairn does not filter it.
			want(t, true, "ct status dnat tcp dport 80 accept")
			want(t, false, "hook forward")
		}
	case "reapply":
		// The forwards as 2b97189 wrote them (not limited to this host) are
		// rewritten when the helper starts; once in this form, they are left
		// alone.
		file := nftRulesFile
		if ufw || firewalld {
			file = forwardTableFile
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(strings.ReplaceAll(string(data), "fib daddr type local ", "")), 0o600); err != nil {
			t.Fatal(err)
		}
		sh(t, nftBinary, "-f", file)
		want(t, false, "fib daddr type local")
		f.Log = slog.New(slog.NewTextHandler(os.Stdout, nil))
		f.Startup(ctx)
		want(t, true, "fib daddr type local tcp dport 8090", "dnat ip to "+os.Getenv("CAIRN_FW_TARGET")+":80")
		before, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		f.Startup(ctx)
		if after, err := os.Stat(file); err != nil || !after.ModTime().Equal(before.ModTime()) {
			t.Fatalf("rewritten again: %v", err)
		}
		if st := f.Status(ctx); st.Pending != nil || len(st.Forwards) != 2 {
			t.Fatalf("status %+v", st)
		}
	case "forward-guard":
		// In a container forwarding is on from the start (its /proc/sys is
		// read-only), so Cairn never switched it on: the mark stands in for
		// that, and goes again so that removing the forwards later does not
		// try to switch forwarding off. The server's own interface is a veth
		// here, which the forward chain would let through as a container's.
		if err := os.WriteFile(f.stateDir(ipForwardMark), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		fwVirtualKinds = []string{"bridge"}
		if err := f.Refresh(ctx, os.Stdout); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(f.stateDir(ipForwardMark)); err != nil {
			t.Fatal(err)
		}
		want(t, true, "hook forward", "ct status dnat accept", "meta nfproto ipv4 drop")
	case "forward-block":
		// The client is blocked: none of the forwards takes its connections.
		ch, err := f.setAddress(ctx, admin, helperapi.FirewallAddress{Address: client, Action: "deny", Note: "test"})
		if err != nil || ch.Pending == nil {
			t.Fatalf("%+v %v", ch, err)
		}
		savePending(t, ch.Pending)
		want(t, true, "elements = { "+client+" }")
	case "forward-deny":
		// A deny rule for the port of the forward to the other host.
		ch, err := f.setRule(ctx, admin, helperapi.FirewallRule{Action: "deny", Protocol: "tcp", Ports: []string{"8091"}, Sources: []string{client}, Note: "test"})
		if err != nil || ch.Pending == nil {
			t.Fatalf("%+v %v", ch, err)
		}
		savePending(t, ch.Pending)
		want(t, true, "ip saddr "+client+" tcp dport 8091 return")
	case "forward-off":
		for _, fw := range f.Status(ctx).Forwards {
			ch, err := f.deleteForward(ctx, admin, helperapi.FirewallRef{ID: fw.ID})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Confirm(ctx, ch.Pending.ID); err != nil {
				t.Fatal(err)
			}
		}
		if ufw || firewalld {
			if out, err := exec.Command(nftBinary, "list", "table", "inet", forwardTable).CombinedOutput(); err == nil {
				t.Fatalf("NAT table still there:\n%s", out)
			}
		}
		if ufw {
			want(t, false, ufwForwardBegin)
		} else {
			want(t, false, "redirect", "dnat")
		}
	case "disable":
		if err := f.Disable(ctx, os.Stdout); err != nil {
			t.Fatal(err)
		}
		if st := f.Status(ctx); st.Enabled || st.Loaded {
			t.Fatalf("status %+v", st)
		}
		if ufw {
			want(t, true, "Status: inactive")
		} else {
			want(t, false, "table inet cairn")
		}
	case "firewalld-offline":
		// Before firewalld runs: check the policy file, and that this
		// system's nft takes the port forwards' NAT table.
		r := sampleRender()
		r.Forwards = sampleForwards
		data, err := renderFirewalld(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll("/etc/firewalld/policies", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(firewalldPolicyFile, data, 0o640); err != nil {
			t.Fatal(err)
		}
		sh(t, firewallOfflineCmd, "--check-config")
		info := sh(t, firewallOfflineCmd, "--info-policy=cairn")
		for _, w := range []string{"priority: -100", "ingress-zones: ANY", "egress-zones: HOST", "ports: 22/tcp 28443/tcp 80/tcp 443/tcp",
			"icmp-blocks: echo-request", `rule priority="-300" family="ipv4" source address="198.51.100.0/24" drop`,
			`rule family="ipv6" source address="2001:db8::/32" port port="8000-8100" protocol="udp" accept`} {
			if !strings.Contains(info, w) {
				t.Fatalf("missing %q:\n%s", w, info)
			}
		}
		if strings.Contains(info, "toport=") {
			t.Fatalf("forward-port in the policy:\n%s", info)
		}
		nat := t.TempDir() + "/forward.nft"
		if err := os.WriteFile(nat, []byte(renderForwardTable(r)), 0o600); err != nil {
			t.Fatal(err)
		}
		sh(t, nftBinary, "-c", "-f", nat)
	default:
		t.Skip("CAIRN_FW_PHASE not set")
	}
}

// TestSSHDetectSystem checks what the detection of the SSH ports finds on
// the system it runs on, which test/firewall/sshd.sh sets up with a real
// sshd: CAIRN_SSH_EXPECT lists the ports, CAIRN_SSH_UNREAD says whether
// sshd's configuration is unreadable.
func TestSSHDetectSystem(t *testing.T) {
	expect, ok := os.LookupEnv("CAIRN_SSH_EXPECT")
	if !ok {
		t.Skip("CAIRN_SSH_EXPECT not set")
	}
	d := detectSSH(context.Background(), "")
	got := strings.Join(portStrings(d.Ports), " ")
	if unread := os.Getenv("CAIRN_SSH_UNREAD") == "yes"; got != expect || (d.Unread != "") != unread {
		t.Fatalf("found ports %q (unread: %q, sshd installed: %v), want %q (unread: %v)", got, d.Unread, d.Installed, expect, unread)
	}
}

// TestFirewallListen accepts connections on the given ports until killed,
// for the reachability checks.
func TestFirewallListen(t *testing.T) {
	ports := os.Getenv("CAIRN_FW_LISTEN")
	if ports == "" {
		t.Skip("CAIRN_FW_LISTEN not set")
	}
	done := make(chan struct{})
	for _, p := range strings.Split(ports, ",") {
		l, err := net.Listen("tcp", ":"+p)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				_, _ = io.WriteString(c, "ok\n")
				_ = c.Close()
			}
		}()
	}
	<-done
}
