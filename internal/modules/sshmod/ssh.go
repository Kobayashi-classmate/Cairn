// SPDX-License-Identifier: AGPL-3.0-only

// Package sshmod serves the SSH page: login settings, the port (moved in
// two steps), authorized keys and the login log. Successful logins become
// notifications.
package sshmod

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
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
	Read   authz.Permission = "ssh.read"
	Manage authz.Permission = "ssh.manage"
)

const checkEvery = time.Minute

// Module implements module.Module.
type Module struct {
	Jobs   *jobs.Manager
	Helper jobs.HelperClient
	DB     *sql.DB
	Events *events.Bus
	Log    *slog.Logger

	cursor string // the newest login already reported
}

// ID implements module.Module.
func (m *Module) ID() string { return "ssh" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{
		Optional: true,
		Permissions: []module.Permission{
			{Name: Read, Roles: []authz.Role{authz.Operator, authz.Auditor}},
			{Name: Manage},
		},
		Nav: []module.NavItem{
			{Key: "ssh", LabelKey: "common.nav.ssh", Icon: "key", Route: "ssh", Permission: Read, Order: 25},
		},
	}
}

type jobResponse struct {
	Job jobs.Record `json:"job"`
}

type keyResponse struct {
	Key helperapi.SSHKey `json:"key"`
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	doc := func(s string) api.RouteOption { return api.Doc("ssh", s) }
	job := api.Returns(202, jobResponse{})
	r.Handle("GET /api/v1/ssh", m.status, api.Require(Read), doc("The SSH server: ports, login settings, users"), api.Returns(200, helperapi.SSHStatus{}))
	r.Handle("PUT /api/v1/ssh/settings", m.settings, api.Require(Manage), api.StepUp(), api.Body(helperapi.SSHSettingsRequest{}),
		doc("Password and root login (starts a job)"), job)
	r.Handle("POST /api/v1/ssh/port", m.port, api.Require(Manage), api.StepUp(), api.Body(helperapi.SSHPortRequest{}),
		doc("Listen on a new port too; the old ones close later (starts a job)"), job)
	r.Handle("POST /api/v1/ssh/port/finish", m.portStep(helperapi.ActionSSHPortFinish, "ssh.port_finish"), api.Require(Manage), api.StepUp(), api.NoBody(),
		doc("Close the old ports now (starts a job)"), job)
	r.Handle("POST /api/v1/ssh/port/cancel", m.portStep(helperapi.ActionSSHPortCancel, "ssh.port_cancel"), api.Require(Manage), api.StepUp(), api.NoBody(),
		doc("Keep the old ports and drop the new one (starts a job)"), job)
	r.Handle("GET /api/v1/ssh/keys", m.keys, api.Require(Read), doc("A user's authorized keys: ?user="), api.Returns(200, helperapi.SSHKeys{}))
	r.Handle("POST /api/v1/ssh/keys", m.addKey, api.Require(Manage), api.StepUp(), api.Body(helperapi.SSHKeyAddRequest{}),
		doc("Authorize a public key"), api.Returns(200, keyResponse{}))
	r.Handle("DELETE /api/v1/ssh/keys", m.deleteKey, api.Require(Manage), api.StepUp(),
		doc("Remove a key: ?user=&fingerprint="), api.Returns(200, struct{}{}))
	r.Handle("GET /api/v1/ssh/logins", m.logins, api.Require(Read), doc("Login attempts, newest first: ?limit=&before="),
		api.Returns(200, helperapi.SSHLogins{}))
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
	case helperapi.CodeSSHUserNotFound:
		return api.NewError(http.StatusNotFound, he.Code, he.Params)
	case "validation.invalid", helperapi.CodeSSHKeyInvalid, helperapi.CodeSSHPortInvalid:
		return api.NewError(http.StatusBadRequest, he.Code, he.Params)
	}
	return api.NewError(http.StatusConflict, he.Code, he.Params)
}

func validation(err error) error {
	var he *helperapi.Error
	if errors.As(err, &he) {
		return api.NewError(http.StatusBadRequest, he.Code, he.Params)
	}
	return err
}

func (m *Module) call(r *http.Request, action string, req, resp any) error {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := m.Helper.Call(ctx, action, callerOf(r), req, resp); err != nil {
		return helperError(err)
	}
	return nil
}

func (m *Module) status(w http.ResponseWriter, r *http.Request) error {
	var st helperapi.SSHStatus
	if err := m.call(r, helperapi.ActionSSHStatus, nil, &st); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, st)
}

func (m *Module) startJob(w http.ResponseWriter, r *http.Request, kind string, params map[string]any, action string, req any) error {
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: kind, Params: params, Permission: Read, ManagePermission: Manage, Locks: []string{"ssh"},
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(_ context.Context, j *jobs.Job) error { return j.RunOp(action, req, nil) })
	if err != nil {
		return err
	}
	audit := map[string]any{"job": job.ID}
	for k, v := range params {
		audit[k] = v
	}
	modutil.Audit(r, m.DB, m.Log, who, kind, "ok", audit)
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func (m *Module) settings(w http.ResponseWriter, r *http.Request) error {
	var q helperapi.SSHSettingsRequest
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	if err := q.Validate(); err != nil {
		return validation(err)
	}
	return m.startJob(w, r, "ssh.settings", map[string]any{"passwordAuthentication": q.PasswordAuthentication, "permitRootLogin": q.PermitRootLogin},
		helperapi.ActionSSHSettings, q)
}

func (m *Module) port(w http.ResponseWriter, r *http.Request) error {
	var q helperapi.SSHPortRequest
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	if q.CloseAfterMinutes == 0 {
		q.CloseAfterMinutes = helperapi.SSHCloseDefault
	}
	if err := q.Validate(); err != nil {
		return validation(err)
	}
	return m.startJob(w, r, "ssh.port", map[string]any{"port": q.Port, "closeAfterMinutes": q.CloseAfterMinutes}, helperapi.ActionSSHPort, q)
}

func (m *Module) portStep(action, kind string) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		return m.startJob(w, r, kind, map[string]any{}, action, nil)
	}
}

func (m *Module) keys(w http.ResponseWriter, r *http.Request) error {
	q := helperapi.SSHKeysRequest{User: r.URL.Query().Get("user")}
	if err := q.Validate(); err != nil {
		return validation(err)
	}
	var keys helperapi.SSHKeys
	if err := m.call(r, helperapi.ActionSSHKeys, q, &keys); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, keys)
}

func (m *Module) addKey(w http.ResponseWriter, r *http.Request) error {
	var q helperapi.SSHKeyAddRequest
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	if err := q.Validate(); err != nil {
		return validation(err)
	}
	var key helperapi.SSHKey
	if err := m.call(r, helperapi.ActionSSHKeyAdd, q, &key); err != nil {
		modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "ssh.key_add", modutil.ErrorCode(err), map[string]any{"user": q.User})
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "ssh.key_add", "ok", map[string]any{"user": q.User, "fingerprint": key.Fingerprint})
	return api.WriteJSON(w, http.StatusOK, keyResponse{Key: key})
}

func (m *Module) deleteKey(w http.ResponseWriter, r *http.Request) error {
	q := helperapi.SSHKeyDeleteRequest{User: r.URL.Query().Get("user"), Fingerprint: r.URL.Query().Get("fingerprint")}
	if err := q.Validate(); err != nil {
		return validation(err)
	}
	if err := m.call(r, helperapi.ActionSSHKeyDelete, q, nil); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "ssh.key_delete", "ok", map[string]any{"user": q.User, "fingerprint": q.Fingerprint})
	return api.WriteJSON(w, http.StatusOK, struct{}{})
}

func (m *Module) logins(w http.ResponseWriter, r *http.Request) error {
	q := helperapi.SSHLoginsRequest{Before: r.URL.Query().Get("before")}
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "limit", "rule": "int"})
		}
		q.Limit = n
	}
	if err := q.Validate(); err != nil {
		return validation(err)
	}
	var out helperapi.SSHLogins
	if err := m.call(r, helperapi.ActionSSHLogins, q, &out); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, out)
}

// Run reports successful logins until ctx ends.
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

// Check publishes ssh.login for every login since the last check. The
// first check after a start only finds where the log ends.
func (m *Module) Check(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	caller := helperapi.Caller{Source: "system", Username: "cairn"}
	var out helperapi.SSHLogins
	if m.cursor == "" {
		if err := m.Helper.Call(ctx, helperapi.ActionSSHLogins, caller, helperapi.SSHLoginsRequest{Limit: 1}, &out); err == nil {
			m.cursor = out.Latest
			if m.cursor == "" {
				m.cursor = strconv.FormatInt(time.Now().UnixMicro(), 10)
			}
		}
		return
	}
	if err := m.Helper.Call(ctx, helperapi.ActionSSHLogins, caller, helperapi.SSHLoginsRequest{After: m.cursor, Limit: helperapi.SSHLoginsMax}, &out); err != nil {
		return
	}
	if out.Latest != "" {
		m.cursor = out.Latest
	}
	for _, l := range out.Entries {
		if l.Result != helperapi.LoginAccepted {
			continue
		}
		m.Events.Publish(events.Event{Type: events.SSHLogin, Level: events.Info,
			Params:    map[string]any{"user": l.User, "ip": l.IP, "method": l.Method},
			DedupeKey: "ssh.login:" + l.User + ":" + l.IP + ":" + strconv.FormatInt(l.Time, 10), TS: l.Time})
	}
}
