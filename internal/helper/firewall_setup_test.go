// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// newSetupFirewall is a system before the install-time setup: nothing
// stored, the platform's firewall (ufw) installed but off, and a root
// directory of its own to read sshd and /proc from.
func newSetupFirewall(t *testing.T, sshd *sshdStub) (*Firewall, *fakeBackend) {
	t.Helper()
	f, fake := newTestFirewall(t)
	f.Root = t.TempDir()
	sshDetect = detectSSH
	sshd.use(t, f.Root, false)
	return f, fake
}

func TestFirewallPlan(t *testing.T) {
	ctx := context.Background()
	sshd22 := "port 22\nlistenaddress 0.0.0.0:22\nlistenaddress [::]:22\n"
	t.Run("what stays open and what closes", func(t *testing.T) {
		f, _ := newSetupFirewall(t, &sshdStub{out: sshd22})
		writeProcNet(t, f.Root, "tcp",
			fakeSocket{local: "0.0.0.0:22", remote: "0.0.0.0:0", state: tcpListen, inode: 1},
			fakeSocket{local: "0.0.0.0:25", remote: "0.0.0.0:0", state: tcpListen, inode: 2},
			fakeSocket{local: "0.0.0.0:80", remote: "0.0.0.0:0", state: tcpListen, inode: 3},
			fakeSocket{local: "127.0.0.1:3306", remote: "0.0.0.0:0", state: tcpListen, inode: 4},
			fakeSocket{local: "[::]:28443", remote: "[::]:0", state: tcpListen, inode: 5},
			fakeSocket{local: "[::]:9100", remote: "[::]:0", state: tcpListen, inode: 6})
		writeProcNet(t, f.Root, "udp", fakeSocket{local: "0.0.0.0:53", remote: "0.0.0.0:0", state: 7, inode: 7})
		writeProcesses(t, f.Root,
			fakeProcess{pid: 60, parent: 1, name: "sshd", sockets: []uint64{1}},
			fakeProcess{pid: 61, parent: 1, name: "master", sockets: []uint64{2}},
			fakeProcess{pid: 62, parent: 1, name: "nginx", sockets: []uint64{3}},
			fakeProcess{pid: 63, parent: 1, name: "node exporter", sockets: []uint64{6}},
			fakeProcess{pid: 64, parent: 1, name: "named", sockets: []uint64{7}})
		plan, err := f.Plan(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Backend != helperapi.FirewallUFW || plan.Active || strings.Join(plan.Ports, " ") != "22/tcp 28443/tcp 80/tcp 443/tcp" {
			t.Fatalf("plan: %+v", plan)
		}
		if got := strings.Join(plan.Closing, " "); got != "25/tcp(master) 9100/tcp(node_exporter) 53/udp(named)" {
			t.Fatalf("closing: %s", got)
		}
		if !plan.Unattended || plan.Reason != "" || plan.SSHUnread || !slices.Equal(plan.SSH, []int{22}) || len(plan.Session) != 0 {
			t.Fatalf("plan: %+v", plan)
		}
		// Asking changes nothing.
		if _, err := os.Stat(f.stateDir("state.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the plan wrote the state: %v", err)
		}
	})
	t.Run("the SSH connection the installer runs in", func(t *testing.T) {
		// sshd's configuration says 22; the session came in on 2222 (told
		// by the installer) and on 2200 (found in the process tree).
		f, _ := newSetupFirewall(t, &sshdStub{out: sshd22})
		writeProcNet(t, f.Root, "tcp", fakeSocket{local: "192.0.2.10:2200", remote: "203.0.113.9:50000", state: tcpEstablished, inode: 9})
		writeProcesses(t, f.Root,
			fakeProcess{pid: 70, parent: 1, name: "tinysshd", sockets: []uint64{9}},
			fakeProcess{pid: os.Getpid(), parent: 70, name: "cairn-helper"})
		plan, err := f.Plan(ctx, []int{2222})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(plan.Session, []int{2200, 2222}) || !slices.Equal(plan.SSH, []int{22, 2200, 2222}) ||
			!strings.HasPrefix(strings.Join(plan.Ports, " "), "22/tcp 2200/tcp 2222/tcp 28443/tcp ") || !plan.Unattended {
			t.Fatalf("plan: %+v", plan)
		}
	})
	t.Run("sshd's configuration cannot be read", func(t *testing.T) {
		f, _ := newSetupFirewall(t, &sshdStub{err: errors.New("exit status 255")})
		plan, err := f.Plan(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Unattended || plan.Reason != "ssh_unread" || !plan.SSHUnread || !slices.Equal(plan.SSH, []int{22}) {
			t.Fatalf("plan: %+v", plan)
		}
		// In an SSH session its port is known for certain.
		if plan, _ = f.Plan(ctx, []int{2222}); !plan.Unattended || !plan.SSHUnread || !slices.Equal(plan.SSH, []int{22, 2222}) {
			t.Fatalf("plan in a session: %+v", plan)
		}
	})
	t.Run("no SSH server found", func(t *testing.T) {
		f, _ := newSetupFirewall(t, &sshdStub{})
		plan, err := f.Plan(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Unattended || plan.Reason != "ssh_unknown" || len(plan.SSH) != 0 || strings.Join(plan.Ports, " ") != "28443/tcp 80/tcp 443/tcp" {
			t.Fatalf("plan: %+v", plan)
		}
	})
	t.Run("another SSH server than sshd", func(t *testing.T) {
		f, _ := newSetupFirewall(t, &sshdStub{})
		writeProcNet(t, f.Root, "tcp", fakeSocket{local: "0.0.0.0:22", remote: "0.0.0.0:0", state: tcpListen, inode: 1})
		writeProcesses(t, f.Root, fakeProcess{pid: 60, parent: 1, name: "dropbear", sockets: []uint64{1}})
		plan, err := f.Plan(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !plan.Unattended || !slices.Equal(plan.SSH, []int{22}) || len(plan.Closing) != 0 {
			t.Fatalf("plan: %+v", plan)
		}
	})
	t.Run("a firewall already runs", func(t *testing.T) {
		f, fake := newSetupFirewall(t, &sshdStub{out: sshd22})
		fake.on = true
		writeProcNet(t, f.Root, "tcp", fakeSocket{local: "0.0.0.0:25", remote: "0.0.0.0:0", state: tcpListen, inode: 2})
		plan, err := f.Plan(ctx, nil)
		if err != nil || !plan.Active || len(plan.Closing) != 0 {
			t.Fatalf("plan: %+v %v", plan, err)
		}
	})
}

func TestFirewallSetup(t *testing.T) {
	ctx := context.Background()
	sshd22 := "port 22\nlistenaddress 0.0.0.0:22\n"
	sshRule := func(t *testing.T, fake *fakeBackend) string {
		t.Helper()
		r := fake.last().Rules[0]
		if r.Owner != helperapi.OwnerSSH {
			t.Fatalf("first rule: %+v", r)
		}
		return strings.Join(r.Ports, ",")
	}
	t.Run("at once", func(t *testing.T) {
		f, fake := newSetupFirewall(t, &sshdStub{out: sshd22})
		var log strings.Builder
		p, err := f.Setup(ctx, &log, FirewallSetup{SSHPorts: []int{2222}})
		if err != nil || p != nil {
			t.Fatalf("setup: %+v %v", p, err)
		}
		cfg, _ := f.load()
		if !cfg.Enabled || cfg.Pending != nil || len(f.timers) != 0 || !slices.Equal(cfg.SSHKeep, []int{2222}) || !slices.Equal(cfg.SSHSeen, []int{22}) {
			t.Fatalf("state: %+v, timers %d", cfg, len(f.timers))
		}
		if got := sshRule(t, fake); got != "22,2222" || !fake.on {
			t.Fatalf("SSH rule: %s", got)
		}
		if r := fake.last().Rules; len(r) != 3 || strings.Join(r[2].Ports, ",") != "80,443" {
			t.Fatalf("rules: %+v", r)
		}
		if !strings.Contains(log.String(), "  22,2222 tcp\n") {
			t.Fatalf("log:\n%s", log.String())
		}
		// The session's port stays in the rule through later changes...
		ch, err := f.setRule(ctx, fwAdmin, rule9999)
		if err != nil || sshRule(t, fake) != "22,2222" {
			t.Fatalf("change: %v, SSH rule %s", err, sshRule(t, fake))
		}
		if err := f.Confirm(ctx, ch.Pending.ID); err != nil {
			t.Fatal(err)
		}
		// ...until the detection knows it by itself.
		sshDetect = func(context.Context, string) sshEndpoints {
			return sshEndpoints{Ports: []int{22, 2222}, Installed: true}
		}
		if err := f.Refresh(ctx, io.Discard); err != nil {
			t.Fatal(err)
		}
		if cfg, _ := f.load(); len(cfg.SSHKeep) != 0 || !slices.Equal(cfg.SSHSeen, []int{22, 2222}) {
			t.Fatalf("state once detected: keep %v, seen %v", cfg.SSHKeep, cfg.SSHSeen)
		}
		// Running the setup again keeps the rules Cairn has.
		if _, err := f.Setup(ctx, io.Discard, FirewallSetup{}); err != nil {
			t.Fatal(err)
		}
		if cfg, _ := f.load(); len(cfg.Rules) != 2 {
			t.Fatalf("rules after a second setup: %+v", cfg.Rules)
		}
	})
	t.Run("with confirmation", func(t *testing.T) {
		f, fake := newSetupFirewall(t, &sshdStub{out: sshd22})
		p, err := f.Setup(ctx, io.Discard, FirewallSetup{Confirm: true})
		if err != nil || p == nil || p.Kind != "enable" {
			t.Fatalf("setup: %+v %v", p, err)
		}
		if left := time.Unix(p.Expires, 0).Sub(f.clock()); left < fwSetupConfirm-2*time.Second || left > fwSetupConfirm {
			t.Fatalf("expires in %v", left)
		}
		if cfg, _ := f.load(); cfg.Pending == nil || len(f.timers) != 1 || !fake.on {
			t.Fatalf("state: %+v, timers %d", cfg.Pending, len(f.timers))
		}
		if err := f.Confirm(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
		if cfg, _ := f.load(); !cfg.Enabled || cfg.Pending != nil || len(f.timers) != 0 || !fake.on {
			t.Fatalf("after the confirmation: %+v", cfg)
		}
	})
	t.Run("not confirmed", func(t *testing.T) {
		f, fake := newSetupFirewall(t, &sshdStub{out: sshd22})
		p, err := f.Setup(ctx, io.Discard, FirewallSetup{Confirm: true})
		if err != nil || p == nil {
			t.Fatalf("setup: %+v %v", p, err)
		}
		// The timer, or "no" at the installer's prompt.
		if err := f.RollbackNow(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
		if cfg, _ := f.load(); cfg.Enabled || cfg.Pending != nil || fake.on {
			t.Fatalf("after the rollback: %+v, backend on %v", cfg, fake.on)
		}
		if err := f.Confirm(ctx, p.ID); !hasCode(err, helperapi.CodeFirewallNoPending) {
			t.Fatalf("an answer after the rollback: %v", err)
		}
	})
	t.Run("the apply fails", func(t *testing.T) {
		f, fake := newSetupFirewall(t, &sshdStub{out: sshd22})
		fake.failNext = 1
		if _, err := f.Setup(ctx, io.Discard, FirewallSetup{}); !hasCode(err, helperapi.CodeFirewallApplyFailed) {
			t.Fatalf("setup: %v", err)
		}
		if cfg, _ := f.load(); cfg.Enabled || cfg.Pending != nil || len(f.timers) != 0 || fake.on {
			t.Fatalf("after the failure: %+v", cfg)
		}
	})
	t.Run("warnings in the log", func(t *testing.T) {
		f, _ := newSetupFirewall(t, &sshdStub{err: errors.New("exit status 255")})
		var log strings.Builder
		if _, err := f.Setup(ctx, &log, FirewallSetup{}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(log.String(), "sshd's configuration cannot be read") {
			t.Fatalf("log:\n%s", log.String())
		}
		g, _ := newSetupFirewall(t, &sshdStub{})
		log.Reset()
		if _, err := g.Setup(ctx, &log, FirewallSetup{}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(log.String(), "no SSH server was found") {
			t.Fatalf("log:\n%s", log.String())
		}
	})
	t.Run("only root's setup gets its options", func(t *testing.T) {
		f, fake := newSetupFirewall(t, &sshdStub{out: sshd22})
		p, err := f.enable(ctx, fwAdmin, fwEnable{window: time.Hour, seedWeb: true, keepSSH: []int{2222}}, io.Discard)
		if err != nil || p == nil {
			t.Fatalf("enable: %+v %v", p, err)
		}
		if left := time.Unix(p.Expires, 0).Sub(f.clock()); left > fwConfirm {
			t.Fatalf("expires in %v", left)
		}
		if cfg, _ := f.load(); len(cfg.SSHKeep) != 0 || len(cfg.Rules) != 0 || sshRule(t, fake) != "22" {
			t.Fatalf("state: %+v", cfg)
		}
	})
}
