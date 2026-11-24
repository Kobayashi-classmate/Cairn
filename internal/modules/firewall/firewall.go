// SPDX-License-Identifier: AGPL-3.0-only

// Package firewall serves the firewall page: Cairn's port rules, trusted
// and blocked addresses and ping, enforced by the root helper through the
// system's firewall. Every change is rolled back after 60 seconds unless
// the browser confirms it over a new connection, which proves the panel is
// still reachable under the new rules.
package firewall

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Permissions of this module.
const (
	Read   authz.Permission = "firewall.read"
	Manage authz.Permission = "firewall.manage"
)

const checkEvery = time.Minute

// Module implements module.Module.
type Module struct {
	Jobs   *jobs.Manager
	Helper jobs.HelperClient
	DB     *sql.DB
	Events *events.Bus
	Log    *slog.Logger

	lastRollback int64
	// failedRollback is the change whose failed rollback was reported.
	failedRollback string
}

// ID implements module.Module.
func (m *Module) ID() string { return "firewall" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{
		Optional: true,
		Permissions: []module.Permission{
			{Name: Read, Roles: []authz.Role{authz.Operator, authz.Auditor}},
			{Name: Manage},
		},
		Nav: []module.NavItem{
			{Key: "firewall", LabelKey: "common.nav.firewall", Icon: "shield-checkmark", Route: "firewall", Permission: Read, Group: module.GroupSecurity, Order: 24},
		},
	}
}

// View is the firewall page's data.
type View struct {
	helperapi.FirewallStatus
	ClientIP string `json:"clientIp"`
}

type addressRequest struct {
	Address string `json:"address"`
	Action  string `json:"action"`
	Note    string `json:"note,omitempty"`
}

type pingRequest struct {
	Block bool `json:"block"`
}

type confirmRequest struct {
	ID string `json:"id"`
}

type jobResponse struct {
	Job jobs.Record `json:"job"`
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	doc := func(s string) api.RouteOption { return api.Doc("firewall", s) }
	change := api.Returns(200, helperapi.FirewallChange{})
	job := api.Returns(202, jobResponse{})
	r.Handle("GET /api/v1/firewall", m.status, api.Require(Read), doc("The firewall: backend, rules, addresses, a pending change"), api.Returns(200, View{}))
	r.Handle("POST /api/v1/firewall/rules", m.addRule, api.Require(Manage), api.StepUp(), api.Body(helperapi.FirewallRule{}),
		doc("Add a port rule (confirm it within 60 s)"), change)
	r.Handle("PUT /api/v1/firewall/rules/{id}", m.updateRule, api.Require(Manage), api.StepUp(), api.Body(helperapi.FirewallRule{}),
		doc("Change a rule; another feature's rule only takes new sources (confirm within 60 s)"), change)
	r.Handle("DELETE /api/v1/firewall/rules/{id}", m.deleteRule, api.Require(Manage), api.StepUp(), doc("Delete a rule (confirm within 60 s)"), change)
	r.Handle("POST /api/v1/firewall/addresses", m.addAddress, api.Require(Manage), api.StepUp(), api.Body(addressRequest{}),
		doc("Trust or block an address (confirm within 60 s)"), change)
	r.Handle("DELETE /api/v1/firewall/addresses/{id}", m.deleteAddress, api.Require(Manage), api.StepUp(),
		doc("Remove an address entry (confirm within 60 s)"), change)
	r.Handle("POST /api/v1/firewall/forwards", m.addForward, api.Require(Manage), api.StepUp(), api.Body(helperapi.FirewallForward{}),
		doc("Forward a port to another port of this host or of another IPv4 host (confirm within 60 s)"), change)
	r.Handle("PUT /api/v1/firewall/forwards/{id}", m.updateForward, api.Require(Manage), api.StepUp(), api.Body(helperapi.FirewallForward{}),
		doc("Change a port forward (confirm within 60 s)"), change)
	r.Handle("DELETE /api/v1/firewall/forwards/{id}", m.deleteForward, api.Require(Manage), api.StepUp(),
		doc("Delete a port forward (confirm within 60 s)"), change)
	r.Handle("PUT /api/v1/firewall/ping", m.ping, api.Require(Manage), api.StepUp(), api.Body(pingRequest{}),
		doc("Block or allow ping (confirm within 60 s)"), change)
	r.Handle("POST /api/v1/firewall/confirm", m.confirm, api.Require(Manage), api.Body(confirmRequest{}),
		doc("Keep a pending change; must arrive over a connection opened after the change"), api.Returns(200, struct{}{}))
	r.Handle("POST /api/v1/firewall/rollback", m.rollback, api.Require(Manage), api.Body(confirmRequest{}),
		doc("Undo a pending change now instead of waiting for its rollback"), api.Returns(200, struct{}{}))
	r.Handle("POST /api/v1/firewall/enable", m.enable, api.Require(Manage), api.StepUp(), api.NoBody(),
		doc("Switch the firewall on, installing it when needed (starts a job; confirm within 60 s)"), job)
	r.Handle("POST /api/v1/firewall/disable", m.disable, api.Require(Manage), api.StepUp(), api.NoBody(),
		doc("Switch the firewall off (starts a job)"), job)
}

func callerOf(r *http.Request) helperapi.Caller {
	who := modutil.ActorOf(r)
	return helperapi.Caller{UserID: who.UserID, Username: who.Username, Remote: api.ClientIP(r), Source: who.Source}
}

func helperError(err error) error {
	he, ok := helperapi.AsError(err)
	if !ok {
		return err
	}
	switch he.Code {
	case helperapi.CodeFirewallNotFound:
		return api.NewError(http.StatusNotFound, he.Code, he.Params)
	case "validation.invalid", helperapi.CodeFirewallDenyNeedsSrc, helperapi.CodeFirewallOverlap, helperapi.CodeFirewallSelfBlock:
		return api.NewError(http.StatusBadRequest, he.Code, he.Params)
	}
	return api.NewError(http.StatusConflict, he.Code, he.Params)
}

func (m *Module) helperStatus(ctx context.Context, c helperapi.Caller) (helperapi.FirewallStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var st helperapi.FirewallStatus
	err := m.Helper.Call(ctx, helperapi.ActionFirewallStatus, c, nil, &st)
	return st, err
}

func (m *Module) status(w http.ResponseWriter, r *http.Request) error {
	st, err := m.helperStatus(r.Context(), callerOf(r))
	if err != nil {
		return helperError(err)
	}
	return api.WriteJSON(w, http.StatusOK, View{FirewallStatus: st, ClientIP: api.ClientIP(r)})
}

// detach gives a request that changes the firewall a context of its own.
// The browser may go away while the helper works (a closed tab, a proxy's
// timeout); the change, its confirmation or its rollback is then finished
// and audited all the same, instead of ending wherever it happened to be.
func detach(r *http.Request, limit time.Duration) (*http.Request, context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), limit)
	return r.WithContext(ctx), ctx, cancel
}

// apply sends a change to the helper. The answer closes the connection:
// the browser must confirm over a new one.
func (m *Module) apply(w http.ResponseWriter, r *http.Request, action, auditAction string, req any, params map[string]any) error {
	r, ctx, cancel := detach(r, 10*time.Minute)
	defer cancel()
	var ch helperapi.FirewallChange
	if err := m.Helper.Call(ctx, action, callerOf(r), req, &ch); err != nil {
		modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), auditAction, modutil.ErrorCode(helperError(err)), params)
		return helperError(err)
	}
	if ch.Pending != nil {
		params["pending"] = ch.Pending.ID
		api.CloseConnection(w)
	}
	if ch.Rule != nil {
		params["rule"] = ch.Rule.ID
	}
	if ch.Address != nil {
		params["address"] = ch.Address.ID
	}
	if ch.Forward != nil {
		params["forward"] = ch.Forward.ID
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), auditAction, "ok", params)
	return api.WriteJSON(w, http.StatusOK, ch)
}

func validation(err error) error {
	var he *helperapi.Error
	if errors.As(err, &he) {
		return api.NewError(http.StatusBadRequest, he.Code, he.Params)
	}
	return err
}

func ruleParams(q helperapi.FirewallRule) map[string]any {
	p := map[string]any{"action": q.Action, "protocol": q.Protocol, "ports": strings.Join(q.Ports, ",")}
	if len(q.Sources) > 0 {
		p["sources"] = strings.Join(q.Sources, ",")
	}
	return p
}

func (m *Module) addRule(w http.ResponseWriter, r *http.Request) error {
	var q helperapi.FirewallRule
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	q.ID, q.Owner = "", ""
	if err := q.Validate(); err != nil {
		return validation(err)
	}
	return m.apply(w, r, helperapi.ActionFirewallRuleSet, "firewall.rule_add", q, ruleParams(q))
}

func (m *Module) updateRule(w http.ResponseWriter, r *http.Request) error {
	var q helperapi.FirewallRule
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	q.ID, q.Owner = r.PathValue("id"), ""
	if !helperapi.ValidFirewallRuleID(q.ID) {
		return api.NewError(http.StatusNotFound, helperapi.CodeFirewallNotFound, map[string]any{"id": q.ID})
	}
	if err := q.Validate(); err != nil {
		return validation(err)
	}
	params := ruleParams(q)
	params["rule"] = q.ID
	return m.apply(w, r, helperapi.ActionFirewallRuleSet, "firewall.rule_update", q, params)
}

func (m *Module) deleteRule(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if !helperapi.ValidFirewallRuleID(id) {
		return api.NewError(http.StatusNotFound, helperapi.CodeFirewallNotFound, map[string]any{"id": id})
	}
	return m.apply(w, r, helperapi.ActionFirewallRuleDelete, "firewall.rule_delete", helperapi.FirewallRef{ID: id}, map[string]any{"rule": id})
}

func (m *Module) addAddress(w http.ResponseWriter, r *http.Request) error {
	var q addressRequest
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	a := helperapi.FirewallAddress{Address: strings.TrimSpace(q.Address), Action: q.Action, Note: q.Note}
	if err := a.Validate(); err != nil {
		return validation(err)
	}
	return m.apply(w, r, helperapi.ActionFirewallAddressSet, "firewall.address_add", a, map[string]any{"address": a.Address, "action": a.Action})
}

func (m *Module) deleteAddress(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if !helperapi.ValidFirewallAddressID(id) {
		return api.NewError(http.StatusNotFound, helperapi.CodeFirewallNotFound, map[string]any{"id": id})
	}
	return m.apply(w, r, helperapi.ActionFirewallAddressDelete, "firewall.address_delete", helperapi.FirewallRef{ID: id}, map[string]any{"address": id})
}

func forwardParams(q helperapi.FirewallForward) map[string]any {
	p := map[string]any{"protocol": q.Protocol, "port": q.Port, "toPort": q.ToPort}
	if q.ToAddress != "" {
		p["toAddress"] = q.ToAddress
	}
	return p
}

func decodeForward(r *http.Request) (helperapi.FirewallForward, error) {
	var q helperapi.FirewallForward
	if err := api.DecodeJSON(r, &q); err != nil {
		return q, err
	}
	q.ToAddress = strings.TrimSpace(q.ToAddress)
	q.Note = strings.TrimSpace(q.Note)
	return q, nil
}

func (m *Module) addForward(w http.ResponseWriter, r *http.Request) error {
	q, err := decodeForward(r)
	if err != nil {
		return err
	}
	q.ID = ""
	if err := q.Validate(); err != nil {
		return validation(err)
	}
	return m.apply(w, r, helperapi.ActionFirewallForwardSet, "firewall.forward_add", q, forwardParams(q))
}

func (m *Module) updateForward(w http.ResponseWriter, r *http.Request) error {
	q, err := decodeForward(r)
	if err != nil {
		return err
	}
	q.ID = r.PathValue("id")
	if !helperapi.ValidFirewallForwardID(q.ID) {
		return api.NewError(http.StatusNotFound, helperapi.CodeFirewallNotFound, map[string]any{"id": q.ID})
	}
	if err := q.Validate(); err != nil {
		return validation(err)
	}
	params := forwardParams(q)
	params["forward"] = q.ID
	return m.apply(w, r, helperapi.ActionFirewallForwardSet, "firewall.forward_update", q, params)
}

func (m *Module) deleteForward(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if !helperapi.ValidFirewallForwardID(id) {
		return api.NewError(http.StatusNotFound, helperapi.CodeFirewallNotFound, map[string]any{"id": id})
	}
	return m.apply(w, r, helperapi.ActionFirewallForwardDelete, "firewall.forward_delete", helperapi.FirewallRef{ID: id}, map[string]any{"forward": id})
}

func (m *Module) ping(w http.ResponseWriter, r *http.Request) error {
	var q pingRequest
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	return m.apply(w, r, helperapi.ActionFirewallPing, "firewall.ping", helperapi.FirewallPingRequest{Block: q.Block}, map[string]any{"block": q.Block})
}

// confirm keeps a pending change, but only over a connection opened after
// the change: one that already existed would survive the new rules even
// if they lock the panel out.
func (m *Module) confirm(w http.ResponseWriter, r *http.Request) error {
	var q confirmRequest
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	if !helperapi.ValidFirewallTxID(q.ID) {
		return api.NewError(http.StatusConflict, helperapi.CodeFirewallNoPending, map[string]any{"id": q.ID})
	}
	st, err := m.helperStatus(r.Context(), callerOf(r))
	if err != nil {
		return helperError(err)
	}
	if st.Pending == nil || st.Pending.ID != q.ID {
		return api.NewError(http.StatusConflict, helperapi.CodeFirewallNoPending, map[string]any{"id": q.ID})
	}
	// The change's time is known to the second only (it was made somewhen
	// in that second): a connection is new for sure from the next second.
	created := time.Unix(st.Pending.Expires-helperapi.FirewallConfirmSeconds, 0)
	if started := api.ConnStarted(r); started.IsZero() || started.Before(created.Add(time.Second)) {
		api.CloseConnection(w)
		return api.NewError(http.StatusConflict, helperapi.CodeFirewallConfirmRetry, nil)
	}
	r, ctx, cancel := detach(r, 3*time.Minute)
	defer cancel()
	if err := m.Helper.Call(ctx, helperapi.ActionFirewallConfirm, callerOf(r), helperapi.FirewallRef{ID: q.ID}, nil); err != nil {
		return helperError(err)
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "firewall.confirm", "ok", map[string]any{"pending": q.ID, "kind": st.Pending.Kind})
	return api.WriteJSON(w, http.StatusOK, struct{}{})
}

// rollback undoes a pending change at once. It needs no new connection:
// it brings back rules under which the caller could already connect.
func (m *Module) rollback(w http.ResponseWriter, r *http.Request) error {
	var q confirmRequest
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	if !helperapi.ValidFirewallTxID(q.ID) {
		return api.NewError(http.StatusConflict, helperapi.CodeFirewallNoPending, map[string]any{"id": q.ID})
	}
	r, ctx, cancel := detach(r, 10*time.Minute)
	defer cancel()
	if err := m.Helper.Call(ctx, helperapi.ActionFirewallRollback, callerOf(r), helperapi.FirewallRef{ID: q.ID}, nil); err != nil {
		modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "firewall.rollback", modutil.ErrorCode(helperError(err)), map[string]any{"pending": q.ID})
		return helperError(err)
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "firewall.rollback", "ok", map[string]any{"pending": q.ID})
	return api.WriteJSON(w, http.StatusOK, struct{}{})
}

func (m *Module) startJob(w http.ResponseWriter, r *http.Request, kind string, fn jobs.Func) error {
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: kind, Params: map[string]any{}, Permission: Read, ManagePermission: Manage, Locks: []string{"firewall"},
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, fn)
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, kind, "ok", map[string]any{"job": job.ID})
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) enable(w http.ResponseWriter, r *http.Request) error {
	return m.startJob(w, r, "firewall.enable", func(_ context.Context, j *jobs.Job) error {
		var res helperapi.FirewallChange
		if err := j.RunOp(helperapi.ActionFirewallEnable, helperapi.FirewallEnableRequest{Confirm: true}, &res); err != nil {
			return err
		}
		// The page confirms this change: it finds it by its ID.
		if res.Pending != nil {
			j.SetParam("pending", res.Pending.ID)
		}
		return nil
	})
}

func (m *Module) disable(w http.ResponseWriter, r *http.Request) error {
	return m.startJob(w, r, "firewall.disable", func(_ context.Context, j *jobs.Job) error {
		return j.RunOp(helperapi.ActionFirewallDisable, nil, nil)
	})
}

// Run reports rolled back changes, and rollbacks that failed, until ctx ends.
func (m *Module) Run(ctx context.Context) {
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	for {
		m.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Check publishes firewall.rolled_back once for a change that was rolled
// back since the last check, and firewall.rollback_failed once for a
// change that could not be undone: its rules are still in force, which
// whoever is locked out by them must learn some other way than the panel.
func (m *Module) Check(ctx context.Context) {
	st, err := m.helperStatus(ctx, helperapi.Caller{Source: "system", Username: "cairn"})
	if err != nil {
		return
	}
	if p := st.Pending; p != nil && p.RollbackFailed != nil && p.ID != m.failedRollback {
		m.failedRollback = p.ID
		m.Events.Publish(events.Event{Type: events.FirewallRollbackFailed, Level: events.Critical,
			Params:    map[string]any{"kind": p.Kind, "detail": p.RollbackFailed.Detail, "attempts": p.RollbackFailed.Attempts},
			DedupeKey: "firewall.rollback_failed:" + p.ID})
	}
	if st.LastRollback == nil {
		return
	}
	at := st.LastRollback.At
	if m.lastRollback == 0 {
		// After a restart, report only what happened in the last minutes.
		m.lastRollback = time.Now().Add(-10 * time.Minute).Unix()
	}
	if at <= m.lastRollback {
		return
	}
	m.lastRollback = at
	if st.LastRollback.Requested {
		return // the user asked for it: nothing to tell
	}
	m.Events.Publish(events.Event{Type: events.FirewallRolledBack, Level: events.Warning,
		Params: map[string]any{"kind": st.LastRollback.Kind}, DedupeKey: "firewall.rollback:" + st.LastRollback.ID})
}

// Summarize implements module.Summarizer: whether the firewall is on.
func (m *Module) Summarize(r *http.Request, can func(authz.Permission) bool) (module.Summary, error) {
	if !can(Read) {
		return module.Summary{}, nil
	}
	st, err := m.helperStatus(r.Context(), callerOf(r))
	if err != nil {
		return module.Summary{}, err
	}
	c := module.Check{Route: "firewall"}
	switch {
	case st.Pending != nil && st.Pending.RollbackFailed != nil:
		// Whatever else is true, rules nobody confirmed are in force.
		c.Code, c.Status, c.Params = "firewall.rollback_failed", module.StatusBad, map[string]any{"kind": st.Pending.Kind}
	case !st.Systemd || st.Backend == "":
		c.Code, c.Status = "firewall.unavailable", module.StatusWarn
	case st.Conflict != "":
		c.Code, c.Status, c.Params = "firewall.conflict", module.StatusBad, map[string]any{"conflict": st.Conflict}
	case st.Enabled && st.Loaded:
		c.Code, c.Status, c.Params = "firewall.on", module.StatusOK, map[string]any{"backend": st.Backend, "rules": len(st.Rules)}
	case st.Enabled:
		c.Code, c.Status, c.Params = "firewall.not_loaded", module.StatusBad, map[string]any{"backend": st.Backend}
	default:
		c.Code, c.Status, c.Params = "firewall.off", module.StatusWarn, map[string]any{"backend": st.Backend}
	}
	return module.Summary{Checks: []module.Check{c}}, nil
}
