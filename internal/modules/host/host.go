// SPDX-License-Identifier: AGPL-3.0-only

// Package host serves the system management page: host name, time zone
// and NTP, swap, reboot, system updates (separate from the panel's own
// update) and switching the system's package sources.
package host

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Permissions of this module.
const (
	Read   authz.Permission = "host.read"
	Manage authz.Permission = "host.manage"
)

// Module implements module.Module.
type Module struct {
	Jobs   *jobs.Manager
	Helper jobs.HelperClient
	DB     *sql.DB
	Log    *slog.Logger
}

// ID implements module.Module.
func (m *Module) ID() string { return "host" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{
		Optional: true,
		Permissions: []module.Permission{
			{Name: Read, Roles: []authz.Role{authz.Operator, authz.Auditor}},
			{Name: Manage},
		},
		Nav: []module.NavItem{
			{Key: "host", LabelKey: "common.nav.host", Icon: "server", Route: "host", Permission: Read, Group: module.GroupSystem, Order: 26},
		},
	}
}

type jobResponse struct {
	Job jobs.Record `json:"job"`
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	doc := func(s string) api.RouteOption { return api.Doc("host", s) }
	job := api.Returns(202, jobResponse{})
	ok := api.Returns(200, struct{}{})
	r.Handle("GET /api/v1/host", m.status, api.Require(Read), doc("Host name, time, swap, kernel, updates"), api.Returns(200, helperapi.HostStatus{}))
	r.Handle("GET /api/v1/host/timezones", m.timezones, api.Require(Read), doc("The time zones the system knows"), api.Returns(200, helperapi.Timezones{}))
	r.Handle("PUT /api/v1/host/hostname", m.sync(helperapi.ActionHostHostname, "host.hostname", func() any { return &helperapi.HostnameRequest{} }),
		api.Require(Manage), api.StepUp(), api.Body(helperapi.HostnameRequest{}), doc("Set the host name"), ok)
	r.Handle("PUT /api/v1/host/timezone", m.sync(helperapi.ActionHostTimezone, "host.timezone", func() any { return &helperapi.TimezoneRequest{} }),
		api.Require(Manage), api.Body(helperapi.TimezoneRequest{}), doc("Set the time zone"), ok)
	r.Handle("PUT /api/v1/host/ntp", m.sync(helperapi.ActionHostNTP, "host.ntp", func() any { return &helperapi.NTPRequest{} }),
		api.Require(Manage), api.Body(helperapi.NTPRequest{}), doc("Switch clock synchronization"), ok)
	r.Handle("PUT /api/v1/host/swappiness", m.sync(helperapi.ActionHostSwappiness, "host.swappiness", func() any { return &helperapi.SwappinessRequest{} }),
		api.Require(Manage), api.Body(helperapi.SwappinessRequest{}), doc("Set vm.swappiness"), ok)
	r.Handle("POST /api/v1/host/reboot", m.reboot, api.Require(Manage), api.StepUp(), api.NoBody(), doc("Reboot in a few seconds"),
		api.Returns(200, helperapi.RebootResult{}))
	r.Handle("PUT /api/v1/host/swap", m.swap, api.Require(Manage), api.StepUp(), api.Body(helperapi.SwapRequest{}),
		doc("Size the swap file /swapfile; 0 removes it (starts a job)"), job)
	r.Handle("POST /api/v1/host/updates/check", m.job("host.updates_check", helperapi.ActionHostUpdatesCheck, nil), api.Require(Manage), api.NoBody(),
		doc("Refresh the package indexes and list the updates (starts a job)"), job)
	r.Handle("POST /api/v1/host/updates/install", m.install, api.Require(Manage), api.StepUp(), api.Body(helperapi.UpdatesInstallRequest{}),
		doc("Install all updates or the security ones (starts a job)"), job)
	r.Handle("PUT /api/v1/host/updates/auto", m.auto, api.Require(Manage), api.Body(helperapi.AutoUpdatesRequest{}),
		doc("Switch automatic security updates (starts a job)"), job)
	r.Handle("GET /api/v1/host/sources", m.sources, api.Require(Read), doc("Probe the system sources in use against the official ones"),
		api.Returns(200, helperapi.SysSourcesProbe{}))
	r.Handle("POST /api/v1/host/sources/switch", m.switchSource, api.Require(Manage), api.StepUp(), api.Body(helperapi.SysSourceSwitchRequest{}),
		doc("Move a role to an eligible candidate, after a backup (starts a job)"), job)
	r.Handle("GET /api/v1/host/sources/backups", m.backups, api.Require(Read), doc("Backups taken before switching"),
		api.Returns(200, helperapi.SysSourceBackups{}))
	r.Handle("POST /api/v1/host/sources/restore", m.restore, api.Require(Manage), api.StepUp(), api.Body(helperapi.SysSourceRef{}),
		doc("Put a backup back (starts a job)"), job)
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
	case "validation.invalid", helperapi.CodeHostHostnameInvalid, helperapi.CodeHostTimezoneInvalid:
		return api.NewError(http.StatusBadRequest, he.Code, he.Params)
	case helperapi.CodeSysSrcBackupNotFound:
		return api.NewError(http.StatusNotFound, he.Code, he.Params)
	}
	return api.NewError(http.StatusConflict, he.Code, he.Params)
}

type validator interface{ Validate() error }

func validation(v any) error {
	val, ok := v.(validator)
	if !ok {
		return nil
	}
	err := val.Validate()
	var he *helperapi.Error
	if errors.As(err, &he) {
		return api.NewError(http.StatusBadRequest, he.Code, he.Params)
	}
	return err
}

func (m *Module) call(ctx context.Context, r *http.Request, action string, req, resp any, limit time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	if err := m.Helper.Call(ctx, action, callerOf(r), req, resp); err != nil {
		return helperError(err)
	}
	return nil
}

func (m *Module) status(w http.ResponseWriter, r *http.Request) error {
	var st helperapi.HostStatus
	if err := m.call(r.Context(), r, helperapi.ActionHostStatus, nil, &st, 30*time.Second); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, st)
}

func (m *Module) timezones(w http.ResponseWriter, r *http.Request) error {
	var tz helperapi.Timezones
	if err := m.call(r.Context(), r, helperapi.ActionHostTimezones, nil, &tz, 30*time.Second); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, tz)
}

// sync handles a small synchronous change: decode, check, call, audit.
func (m *Module) sync(action, auditAction string, newReq func() any) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		req := newReq()
		if err := api.DecodeJSON(r, req); err != nil {
			return err
		}
		if err := validation(req); err != nil {
			return err
		}
		params := map[string]any{}
		switch q := req.(type) {
		case *helperapi.HostnameRequest:
			params["hostname"] = q.Hostname
		case *helperapi.TimezoneRequest:
			params["timezone"] = q.Timezone
		case *helperapi.NTPRequest:
			params["enabled"] = q.Enabled
		case *helperapi.SwappinessRequest:
			params["value"] = q.Value
		}
		if err := m.call(r.Context(), r, action, req, nil, 60*time.Second); err != nil {
			modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), auditAction, modutil.ErrorCode(err), params)
			return err
		}
		modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), auditAction, "ok", params)
		return api.WriteJSON(w, http.StatusOK, struct{}{})
	}
}

func (m *Module) reboot(w http.ResponseWriter, r *http.Request) error {
	var res helperapi.RebootResult
	if err := m.call(r.Context(), r, helperapi.ActionHostReboot, nil, &res, 30*time.Second); err != nil {
		modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "host.reboot", modutil.ErrorCode(err), nil)
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "host.reboot", "ok", map[string]any{"at": res.At})
	return api.WriteJSON(w, http.StatusOK, res)
}

func (m *Module) start(w http.ResponseWriter, r *http.Request, kind string, params map[string]any, action string, req any) error {
	who := modutil.ActorOf(r)
	if params == nil {
		params = map[string]any{}
	}
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: kind, Params: params, Permission: Read, ManagePermission: Manage, Locks: []string{"host"},
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

func (m *Module) job(kind, action string, req any) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error { return m.start(w, r, kind, nil, action, req) }
}

func (m *Module) swap(w http.ResponseWriter, r *http.Request) error {
	var q helperapi.SwapRequest
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	if err := validation(q); err != nil {
		return err
	}
	return m.start(w, r, "host.swap", map[string]any{"sizeMb": q.SizeMB}, helperapi.ActionHostSwap, q)
}

func (m *Module) install(w http.ResponseWriter, r *http.Request) error {
	var q helperapi.UpdatesInstallRequest
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	return m.start(w, r, "host.updates_install", map[string]any{"securityOnly": q.SecurityOnly}, helperapi.ActionHostUpdatesInstall, q)
}

func (m *Module) auto(w http.ResponseWriter, r *http.Request) error {
	var q helperapi.AutoUpdatesRequest
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	return m.start(w, r, "host.updates_auto", map[string]any{"enabled": q.Enabled}, helperapi.ActionHostUpdatesAuto, q)
}

func (m *Module) sources(w http.ResponseWriter, r *http.Request) error {
	var p helperapi.SysSourcesProbe
	if err := m.call(r.Context(), r, helperapi.ActionSysSourcesProbe, nil, &p, 2*time.Minute); err != nil {
		return err
	}
	if p.Slots == nil {
		p.Slots = []helperapi.SourceSlot{}
	}
	return api.WriteJSON(w, http.StatusOK, p)
}

func (m *Module) switchSource(w http.ResponseWriter, r *http.Request) error {
	var q helperapi.SysSourceSwitchRequest
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	if err := validation(q); err != nil {
		return err
	}
	return m.start(w, r, "host.sources_switch", map[string]any{"role": q.Role, "candidate": q.Candidate}, helperapi.ActionSysSourcesSwitch, q)
}

func (m *Module) backups(w http.ResponseWriter, r *http.Request) error {
	var b helperapi.SysSourceBackups
	if err := m.call(r.Context(), r, helperapi.ActionSysSourcesBackups, nil, &b, 30*time.Second); err != nil {
		return err
	}
	if b.Backups == nil {
		b.Backups = []helperapi.SysSourceBackup{}
	}
	return api.WriteJSON(w, http.StatusOK, b)
}

func (m *Module) restore(w http.ResponseWriter, r *http.Request) error {
	var q helperapi.SysSourceRef
	if err := api.DecodeJSON(r, &q); err != nil {
		return err
	}
	if err := validation(q); err != nil {
		return err
	}
	return m.start(w, r, "host.sources_restore", map[string]any{"backup": q.ID}, helperapi.ActionSysSourcesRestore, q)
}

// Summarize implements module.Summarizer: updates waiting to be installed
// and a reboot the system asks for.
func (m *Module) Summarize(r *http.Request, can func(authz.Permission) bool) (module.Summary, error) {
	if !can(Read) {
		return module.Summary{}, nil
	}
	var st helperapi.HostStatus
	if err := m.call(r.Context(), r, helperapi.ActionHostStatus, nil, &st, 30*time.Second); err != nil {
		return module.Summary{}, err
	}
	var sum module.Summary
	if n := len(st.Updates.Packages); n > 0 {
		security := 0
		for _, p := range st.Updates.Packages {
			if p.Security {
				security++
			}
		}
		sum.Notices = append(sum.Notices, module.Check{Code: "host.updates", Status: module.StatusWarn, Route: "host",
			Params: map[string]any{"count": n, "security": security}})
	}
	if st.RebootRequired {
		sum.Notices = append(sum.Notices, module.Check{Code: "host.reboot", Status: module.StatusWarn, Route: "host"})
	}
	return sum, nil
}
