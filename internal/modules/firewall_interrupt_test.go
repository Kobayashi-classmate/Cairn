// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// slowHelper holds the call of one action until release is closed, the way
// the root helper is busy while it applies rules, and tells what became of
// the call's context meanwhile.
type slowHelper struct {
	*fakeHelper
	action           string
	started, release chan struct{}
	ended            chan error
}

func (h *slowHelper) Call(ctx context.Context, action string, c helperapi.Caller, req, resp any) error {
	if action != h.action {
		return h.fakeHelper.Call(ctx, action, c, req, resp)
	}
	close(h.started)
	<-h.release
	err := ctx.Err()
	if err == nil {
		// The helper's client gives up on an ended context; otherwise the
		// helper answers.
		err = h.fakeHelper.Call(ctx, action, c, req, resp)
	}
	h.ended <- ctx.Err()
	return err
}

// A browser that goes away while the helper works on a firewall change (a
// closed tab, a reload, a proxy's timeout) does not end the helper call:
// the change, or its rollback, is finished and audited.
func TestFirewallRequestsOutliveTheBrowser(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	admin.elevate("")
	pending := helperapi.FirewallPending{ID: "x0123456789abcde", Expires: time.Now().Unix() + helperapi.FirewallConfirmSeconds, Kind: "rule.add"}
	e.helper.respond(helperapi.ActionFirewallRuleSet, helperapi.FirewallChange{Pending: &pending,
		Rule: &helperapi.FirewallRule{ID: "r0123456789abcde", Action: "allow", Protocol: "tcp", Ports: []string{"8080"}}})
	e.helper.respond(helperapi.ActionFirewallRollback, helperapi.Empty{})
	t.Cleanup(func() { e.firewall.Helper = e.helper })
	audits := func(action string) (n int) {
		_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = ? AND result = 'ok'`, action).Scan(&n)
		return n
	}
	for _, c := range []struct {
		name, path, action, audit string
		body                      map[string]any
	}{
		{"a change", "/api/v1/firewall/rules", helperapi.ActionFirewallRuleSet, "firewall.rule_add",
			map[string]any{"action": "allow", "protocol": "tcp", "ports": []string{"8080"}}},
		{"roll back now", "/api/v1/firewall/rollback", helperapi.ActionFirewallRollback, "firewall.rollback", map[string]any{"id": pending.ID}},
	} {
		slow := &slowHelper{fakeHelper: e.helper, action: c.action, started: make(chan struct{}), release: make(chan struct{}), ended: make(chan error, 1)}
		e.firewall.Helper = slow
		browser, disconnect := context.WithCancel(context.Background())
		body, _ := json.Marshal(c.body)
		req, _ := http.NewRequestWithContext(browser, http.MethodPost, e.srv.URL+c.path, bytes.NewReader(body))
		admin.authorize(req)
		gone := make(chan struct{})
		go func() {
			if res, err := admin.hc.Do(req); err == nil {
				_ = res.Body.Close()
			}
			close(gone)
		}()
		select {
		case <-slow.started:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the helper was not called", c.name)
		}
		disconnect() // the tab is closed: the connection goes
		<-gone
		// The server notices a closed connection while the handler runs.
		time.Sleep(200 * time.Millisecond)
		close(slow.release)
		if err := <-slow.ended; err != nil {
			t.Fatalf("%s: the helper call ended with the browser's request: %v", c.name, err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for audits(c.audit) != 1 {
			if time.Now().After(deadline) {
				t.Fatalf("%s: no audit entry after the browser went away", c.name)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// A rollback that failed is reported once, as critical, and shows on the
// overview; a rollback that worked is still reported the old way.
func TestFirewallFailedRollbackIsReported(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	failed := e.published(events.FirewallRollbackFailed)
	rolled := e.published(events.FirewallRolledBack)
	st := helperapi.FirewallStatus{Backend: "ufw", Installed: true, Enabled: true, Loaded: true, Systemd: true,
		Pending: &helperapi.FirewallPending{ID: "x0123456789abcde", Expires: time.Now().Unix() - 30, Kind: "rule.add",
			RollbackFailed: &helperapi.FirewallRollbackFailure{At: time.Now().Unix(), Attempts: 3, Detail: "ufw: could not write"}}}
	e.helper.respond(helperapi.ActionFirewallStatus, st)
	e.firewall.Check(context.Background())
	e.firewall.Check(context.Background())
	got := failed()
	if len(got) != 1 || got[0].Level != events.Critical || got[0].Params["kind"] != "rule.add" || got[0].Params["detail"] != "ufw: could not write" {
		t.Fatalf("events: %+v", got)
	}
	if len(rolled()) != 0 {
		t.Fatalf("reported as rolled back: %+v", rolled())
	}
	// The page gets the failure with the pending change.
	view := admin.expect(admin.do("GET", "/api/v1/firewall", nil), 200, "")
	if f, _ := view.obj("pending")["rollbackFailed"].(map[string]any); f["attempts"] != float64(3) || f["detail"] != "ufw: could not write" {
		t.Fatalf("view: %v", view.body["pending"])
	}
	ov := admin.expect(admin.do("GET", "/api/v1/overview", nil), 200, "")
	checks := codesOf(ov.body["checks"])
	if !slices.Contains(checks, "firewall.rollback_failed") || slices.Contains(checks, "firewall.on") {
		t.Fatalf("overview checks: %v", checks)
	}
	if c := findCode(ov.body["checks"], "firewall.rollback_failed"); c["status"] != "bad" || c["route"] != "firewall" {
		t.Fatalf("overview check: %v", c)
	}

	// The retry worked: the change is rolled back, and reported as that.
	st.Pending = nil
	st.LastRollback = &helperapi.FirewallRollback{ID: "x0123456789abcde", At: time.Now().Unix(), Kind: "rule.add"}
	e.helper.respond(helperapi.ActionFirewallStatus, st)
	e.firewall.Check(context.Background())
	if len(failed()) != 1 || len(rolled()) != 1 {
		t.Fatalf("after the retry: failed %d, rolled back %d", len(failed()), len(rolled()))
	}
	// Another change that cannot be undone is another event.
	st.Pending = &helperapi.FirewallPending{ID: "x00000000000000b", Kind: "ping", RollbackFailed: &helperapi.FirewallRollbackFailure{Attempts: 1, Detail: "d"}}
	e.helper.respond(helperapi.ActionFirewallStatus, st)
	e.firewall.Check(context.Background())
	if len(failed()) != 2 {
		t.Fatalf("second failure: %d events", len(failed()))
	}
}
