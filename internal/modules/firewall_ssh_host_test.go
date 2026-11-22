// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"context"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// fail makes one helper action answer with err.
func (f *fakeHelper) fail(action string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errs == nil {
		f.errs = map[string]error{}
	}
	f.errs[action] = err
}

func TestFirewallChangeAndConfirm(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	created := time.Now().Add(-time.Second)
	pending := helperapi.FirewallPending{ID: "x0123456789abcde", Expires: created.Unix() + helperapi.FirewallConfirmSeconds, Kind: "rule.add"}
	e.helper.respond(helperapi.ActionFirewallStatus, helperapi.FirewallStatus{Backend: "ufw", Enabled: true, Loaded: true, Pending: &pending})
	e.helper.respond(helperapi.ActionFirewallRuleSet, helperapi.FirewallChange{Pending: &pending,
		Rule: &helperapi.FirewallRule{ID: "r0123456789abcde", Action: "allow", Protocol: "tcp", Ports: []string{"8080"}}})

	view := admin.expect(admin.do("GET", "/api/v1/firewall", nil), 200, "")
	if view.body["clientIp"] != "127.0.0.1" || view.body["backend"] != "ufw" {
		t.Fatalf("view %v", view.body)
	}
	rule := map[string]any{"action": "allow", "protocol": "tcp", "ports": []string{"8080"}}
	admin.expect(admin.do("POST", "/api/v1/firewall/rules", rule), 403, "auth.step_up_required")
	admin.elevate("")
	admin.expect(admin.do("POST", "/api/v1/firewall/rules", map[string]any{"action": "deny", "protocol": "tcp", "ports": []string{"25"}}),
		400, helperapi.CodeFirewallDenyNeedsSrc)
	admin.expect(admin.do("POST", "/api/v1/firewall/rules", map[string]any{"action": "allow", "protocol": "tcp", "ports": []string{"70000"}}),
		400, "validation.invalid")
	res := admin.expect(admin.do("POST", "/api/v1/firewall/rules", rule), 200, "")
	if res.obj("pending")["id"] != pending.ID || res.obj("rule")["id"] != "r0123456789abcde" {
		t.Fatalf("change %v", res.body)
	}
	if q := sentAs[helperapi.FirewallRule](t, e, helperapi.ActionFirewallRuleSet); q.ID != "" || q.Ports[0] != "8080" {
		t.Fatalf("sent %+v", q)
	}

	// The answer closed the connection, so the confirmation comes over a
	// new one and is accepted.
	admin.expect(admin.do("POST", "/api/v1/firewall/confirm", map[string]any{"id": pending.ID}), 200, "")
	if !e.helper.called(helperapi.ActionFirewallConfirm) {
		t.Fatal("not confirmed in the helper")
	}
	admin.expect(admin.do("POST", "/api/v1/firewall/confirm", map[string]any{"id": "x000000000000000"}), 409, helperapi.CodeFirewallNoPending)

	// A connection older than the change does not count.
	future := helperapi.FirewallPending{ID: "x00000000000000f", Expires: time.Now().Add(time.Hour).Unix(), Kind: "ping"}
	e.helper.respond(helperapi.ActionFirewallStatus, helperapi.FirewallStatus{Enabled: true, Pending: &future})
	admin.expect(admin.do("POST", "/api/v1/firewall/confirm", map[string]any{"id": future.ID}), 409, helperapi.CodeFirewallConfirmRetry)

	// Helper refusals keep their codes.
	e.helper.fail(helperapi.ActionFirewallAddressSet, &helperapi.Error{Code: helperapi.CodeFirewallSelfBlock, Params: map[string]any{"address": "127.0.0.1"}})
	admin.expect(admin.do("POST", "/api/v1/firewall/addresses", map[string]any{"address": "127.0.0.1", "action": "deny"}), 400, helperapi.CodeFirewallSelfBlock)
	admin.expect(admin.do("DELETE", "/api/v1/firewall/rules/ssh", nil), 404, helperapi.CodeFirewallNotFound)

	// Enabling is a job.
	e.opsSucceed(helperapi.ActionFirewallEnable)
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/firewall/enable", nil), 202, ""))
	if q := sentAs[helperapi.FirewallEnableRequest](t, e, helperapi.ActionFirewallEnable); !q.Confirm {
		t.Fatal("enable without the rollback window")
	}

	// A rollback is reported once.
	got := e.published(events.FirewallRolledBack)
	e.helper.respond(helperapi.ActionFirewallStatus, helperapi.FirewallStatus{LastRollback: &helperapi.FirewallRollback{ID: "x1", At: time.Now().Unix(), Kind: "ping"}})
	e.firewall.Check(context.Background())
	e.firewall.Check(context.Background())
	if n := len(got()); n != 1 {
		t.Fatalf("rollback events %d", n)
	}
}

func TestFirewallForwards(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	pending := helperapi.FirewallPending{ID: "x0123456789abcde", Expires: time.Now().Unix() + helperapi.FirewallConfirmSeconds, Kind: "forward.add"}
	saved := helperapi.FirewallForward{ID: "f0123456789abcde", Protocol: "tcp", Port: 8090, ToAddress: "10.0.0.5", ToPort: 80}
	e.helper.respond(helperapi.ActionFirewallForwardSet, helperapi.FirewallChange{Pending: &pending, Forward: &saved})
	e.helper.respond(helperapi.ActionFirewallForwardDelete, helperapi.FirewallChange{Pending: &pending})

	fw := map[string]any{"protocol": "tcp", "port": 8090, "toAddress": " 10.0.0.5 ", "toPort": 80, "note": "app"}
	admin.expect(admin.do("POST", "/api/v1/firewall/forwards", fw), 403, "auth.step_up_required")
	admin.elevate("")
	for _, c := range []struct {
		body        map[string]any
		field, rule string
	}{
		{map[string]any{"protocol": "tcp", "port": 8090, "toPort": 8090}, "toPort", "same_port"},
		{map[string]any{"protocol": "tcp", "port": 8090, "toAddress": "::1", "toPort": 80}, "toAddress", "forward_target"},
		{map[string]any{"protocol": "sctp", "port": 8090, "toPort": 80}, "protocol", "oneof"},
	} {
		r := admin.expect(admin.do("POST", "/api/v1/firewall/forwards", c.body), 400, "validation.invalid")
		if r.params()["field"] != c.field || r.params()["rule"] != c.rule {
			t.Fatalf("%v: %v", c.body, r.params())
		}
	}
	res := admin.expect(admin.do("POST", "/api/v1/firewall/forwards", fw), 200, "")
	if res.obj("forward")["id"] != saved.ID || res.obj("pending")["id"] != pending.ID {
		t.Fatalf("add %v", res.body)
	}
	if q := sentAs[helperapi.FirewallForward](t, e, helperapi.ActionFirewallForwardSet); q.ID != "" || q.ToAddress != "10.0.0.5" || q.Port != 8090 {
		t.Fatalf("sent %+v", q)
	}
	admin.expect(admin.do("PUT", "/api/v1/firewall/forwards/"+saved.ID, map[string]any{"protocol": "udp", "port": 8090, "toPort": 81}), 200, "")
	if q := sentAs[helperapi.FirewallForward](t, e, helperapi.ActionFirewallForwardSet); q.ID != saved.ID || q.Protocol != "udp" {
		t.Fatalf("sent %+v", q)
	}
	admin.expect(admin.do("PUT", "/api/v1/firewall/forwards/r0123456789abcde", map[string]any{"protocol": "udp", "port": 8090, "toPort": 81}),
		404, helperapi.CodeFirewallNotFound)
	admin.expect(admin.do("DELETE", "/api/v1/firewall/forwards/"+saved.ID, nil), 200, "")
	if q := sentAs[helperapi.FirewallRef](t, e, helperapi.ActionFirewallForwardDelete); q.ID != saved.ID {
		t.Fatalf("sent %+v", q)
	}
	// The helper's refusals keep their codes.
	e.helper.fail(helperapi.ActionFirewallForwardSet, &helperapi.Error{Code: helperapi.CodeFirewallForwardTaken, Params: map[string]any{"port": 8090}})
	admin.expect(admin.do("POST", "/api/v1/firewall/forwards", fw), 409, helperapi.CodeFirewallForwardTaken)
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action LIKE 'firewall.forward_%'`).Scan(&n)
	if n != 4 {
		t.Fatalf("forward audits: %d", n)
	}

	// Rolling back at once needs no new connection.
	e.helper.respond(helperapi.ActionFirewallRollback, helperapi.Empty{})
	admin.expect(admin.do("POST", "/api/v1/firewall/rollback", map[string]any{"id": pending.ID}), 200, "")
	if q := sentAs[helperapi.FirewallRef](t, e, helperapi.ActionFirewallRollback); q.ID != pending.ID {
		t.Fatalf("sent %+v", q)
	}
	admin.expect(admin.do("POST", "/api/v1/firewall/rollback", map[string]any{"id": "bad"}), 409, helperapi.CodeFirewallNoPending)
	// A rollback the user asked for is not announced.
	got := e.published(events.FirewallRolledBack)
	e.helper.respond(helperapi.ActionFirewallStatus, helperapi.FirewallStatus{LastRollback: &helperapi.FirewallRollback{ID: "x2", At: time.Now().Unix(), Kind: "forward.add", Requested: true}})
	e.firewall.Check(context.Background())
	if n := len(got()); n != 0 {
		t.Fatalf("rollback events %d", n)
	}
}

func TestSSHModule(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionSSHStatus, helperapi.SSHStatus{Installed: true, Service: "ssh", Ports: []int{22}})
	admin.expect(admin.do("GET", "/api/v1/ssh", nil), 200, "")
	admin.elevate("")
	e.opsSucceed(helperapi.ActionSSHSettings, helperapi.ActionSSHPort, helperapi.ActionSSHPortFinish)
	admin.expect(admin.do("PUT", "/api/v1/ssh/settings", map[string]any{"passwordAuthentication": false, "permitRootLogin": "maybe"}), 400, "validation.invalid")
	admin.runJob(admin.expect(admin.do("PUT", "/api/v1/ssh/settings", map[string]any{"passwordAuthentication": false, "permitRootLogin": "no"}), 202, ""))
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/ssh/port", map[string]any{"port": 2222}), 202, ""))
	if q := sentAs[helperapi.SSHPortRequest](t, e, helperapi.ActionSSHPort); q.Port != 2222 || q.CloseAfterMinutes != helperapi.SSHCloseDefault {
		t.Fatalf("port %+v", q)
	}
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/ssh/port/finish", nil), 202, ""))

	e.helper.respond(helperapi.ActionSSHKeyAdd, helperapi.SSHKey{Type: "ssh-ed25519", Fingerprint: "SHA256:abc"})
	admin.expect(admin.do("POST", "/api/v1/ssh/keys", map[string]any{"user": "deploy", "key": "ssh-ed25519 AAAA x"}), 200, "")
	admin.expect(admin.do("DELETE", "/api/v1/ssh/keys?user=deploy&fingerprint=nope", nil), 400, "validation.invalid")
	admin.expect(admin.do("GET", "/api/v1/ssh/keys?user=../etc", nil), 400, "validation.invalid")

	// Logins: the first check only finds the end of the log; then each
	// accepted login is reported.
	got := e.published(events.SSHLogin)
	e.helper.respond(helperapi.ActionSSHLogins, helperapi.SSHLogins{Latest: "1000"})
	e.ssh.Check(context.Background())
	e.helper.respond(helperapi.ActionSSHLogins, helperapi.SSHLogins{Latest: "2000", Entries: []helperapi.SSHLogin{
		{Time: 1, User: "root", IP: "203.0.113.9", Method: "publickey", Result: "accepted"},
		{Time: 2, User: "admin", IP: "198.51.100.1", Method: "password", Result: "invalid_user"},
	}})
	e.ssh.Check(context.Background())
	if q := sentAs[helperapi.SSHLoginsRequest](t, e, helperapi.ActionSSHLogins); q.After != "1000" {
		t.Fatalf("logins %+v", q)
	}
	if ev := got(); len(ev) != 1 || ev[0].Params["user"] != "root" {
		t.Fatalf("events %+v", ev)
	}
	admin.expect(admin.do("GET", "/api/v1/ssh/logins?limit=9999", nil), 400, "validation.invalid")
}

func TestHostModule(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionHostStatus, helperapi.HostStatus{Hostname: "web1", Timezone: "UTC"})
	admin.expect(admin.do("GET", "/api/v1/host", nil), 200, "")
	admin.expect(admin.do("PUT", "/api/v1/host/hostname", map[string]any{"hostname": "web2"}), 403, "auth.step_up_required")
	admin.elevate("")
	admin.expect(admin.do("PUT", "/api/v1/host/hostname", map[string]any{"hostname": "Bad_Name"}), 400, helperapi.CodeHostHostnameInvalid)
	admin.expect(admin.do("PUT", "/api/v1/host/hostname", map[string]any{"hostname": "web2"}), 200, "")
	if q := sentAs[helperapi.HostnameRequest](t, e, helperapi.ActionHostHostname); q.Hostname != "web2" {
		t.Fatalf("hostname %+v", q)
	}
	admin.expect(admin.do("PUT", "/api/v1/host/swappiness", map[string]any{"value": 101}), 400, "validation.invalid")
	e.helper.respond(helperapi.ActionHostReboot, helperapi.RebootResult{At: 42})
	if r := admin.expect(admin.do("POST", "/api/v1/host/reboot", nil), 200, ""); r.body["at"] != float64(42) {
		t.Fatalf("reboot %v", r.body)
	}
	e.opsSucceed(helperapi.ActionHostSwap, helperapi.ActionHostUpdatesCheck, helperapi.ActionSysSourcesSwitch)
	admin.expect(admin.do("PUT", "/api/v1/host/swap", map[string]any{"sizeMb": 64}), 400, "validation.invalid")
	admin.runJob(admin.expect(admin.do("PUT", "/api/v1/host/swap", map[string]any{"sizeMb": 2048}), 202, ""))
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/host/updates/check", nil), 202, ""))
	admin.expect(admin.do("POST", "/api/v1/host/sources/switch", map[string]any{"role": "other", "candidate": "official"}), 400, "validation.invalid")
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/host/sources/switch", map[string]any{"role": "main", "candidate": "ftp.cn.debian.org"}), 202, ""))
	e.helper.respond(helperapi.ActionSysSourcesProbe, helperapi.SysSourcesProbe{})
	if r := admin.expect(admin.do("GET", "/api/v1/host/sources", nil), 200, ""); r.body["slots"] == nil {
		t.Fatalf("slots %v", r.body)
	}
	e.helper.fail(helperapi.ActionSysSourcesRestore, &helperapi.Error{Code: helperapi.CodeSysSrcBackupNotFound})
	admin.expect(admin.do("POST", "/api/v1/host/sources/restore", map[string]any{"id": "bad"}), 400, "validation.invalid")
}
