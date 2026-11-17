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
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"

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
	old := sshPorts
	t.Cleanup(func() { sshPorts = old })
	sshPorts = func(context.Context) []int { return []int{22} }
	return &Firewall{StateDir: fwStateDir, Default: def, PanelPort: 28443}
}

func sh(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// rules shows what the backend enforces now.
func rules(t *testing.T) string {
	switch os.Getenv("CAIRN_FW_BACKEND") {
	case helperapi.FirewallNftables:
		out, _ := exec.Command(nftBinary, "list", "ruleset").CombinedOutput()
		return string(out)
	case helperapi.FirewallUFW:
		out, _ := exec.Command(ufwBinary, "status", "verbose").CombinedOutput()
		before, _ := os.ReadFile("/etc/ufw/before.rules")
		return string(out) + "\n" + string(before)
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
	switch phase := os.Getenv("CAIRN_FW_PHASE"); phase {
	case "setup":
		plan, err := f.Plan(ctx)
		if err != nil || plan.Backend != backend || strings.Join(plan.Ports, " ") != "22/tcp 28443/tcp 80/tcp 443/tcp" {
			t.Fatalf("plan %+v %v", plan, err)
		}
		if err := f.Setup(ctx, os.Stdout); err != nil {
			t.Fatal(err)
		}
		st := f.Status(ctx)
		if !st.Enabled || !st.Loaded || st.Backend != backend || st.Pending != nil {
			t.Fatalf("status %+v", st)
		}
		if ufw {
			want(t, true, "Status: active", "Default: deny (incoming)", "22/tcp", "28443/tcp", "80,443/tcp")
		} else {
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
		client := os.Getenv("CAIRN_FW_CLIENT")
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
		want(t, false, os.Getenv("CAIRN_FW_CLIENT"))
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
		// firewalld does not run in a container: check the policy file.
		data, err := renderFirewalld(sampleRender())
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
	default:
		t.Skip("CAIRN_FW_PHASE not set")
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
