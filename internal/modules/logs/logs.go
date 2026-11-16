// SPDX-License-Identifier: AGPL-3.0-only

// Package logs is the system journal browser: journalctl, through the
// root helper, with filters by unit, priority, time and text, and a cursor
// to follow new entries.
package logs

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Read lets a role read the system journal.
const Read authz.Permission = "logs.read"

// Module implements module.Module.
type Module struct {
	Helper jobs.HelperClient
	Log    *slog.Logger
}

// ID implements module.Module.
func (m *Module) ID() string { return "logs" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{
		Optional:    true,
		Permissions: []module.Permission{{Name: Read, Roles: []authz.Role{authz.Operator, authz.Auditor}}},
		Nav: []module.NavItem{
			{Key: "logs", LabelKey: "common.nav.logs", Icon: "reader", Route: "logs", Permission: Read, Order: 23},
		},
	}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	doc := func(s string) api.RouteOption { return api.Doc("logs", s) }
	r.Handle("GET /api/v1/logs/journal", m.journal, api.Require(Read),
		doc("Journal entries: ?unit=&priority=0-7&since=&until=&grep=&boot=true&limit=; ?after=<cursor> follows"), api.Returns(200, helperapi.Journal{}))
	r.Handle("GET /api/v1/logs/units", m.units, api.Require(Read), doc("Service units, for the unit filter"), api.Returns(200, helperapi.Units{}))
}

func callerOf(r *http.Request) helperapi.Caller {
	who := modutil.ActorOf(r)
	return helperapi.Caller{UserID: who.UserID, Username: who.Username, Remote: api.ClientIP(r), Source: who.Source}
}

func (m *Module) call(r *http.Request, action string, req, resp any) error {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err := m.Helper.Call(ctx, action, callerOf(r), req, resp)
	if he, ok := helperapi.AsError(err); ok {
		status := http.StatusConflict
		if he.Code == helperapi.CodeLogNoJournal {
			status = http.StatusNotFound
		}
		return api.NewError(status, he.Code, he.Params)
	}
	return err
}

func (m *Module) journal(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	req := helperapi.JournalRequest{Unit: q.Get("unit"), Since: q.Get("since"), Until: q.Get("until"), Grep: q.Get("grep"),
		Boot: q.Get("boot") == "true", After: q.Get("after")}
	for field, dst := range map[string]*int{"priority": &req.Priority, "limit": &req.Limit} {
		if v := q.Get(field); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": field, "rule": "integer"})
			}
			*dst = n
		}
	}
	if err := req.Validate(); err != nil {
		var he *helperapi.Error
		if errors.As(err, &he) {
			return api.NewError(http.StatusBadRequest, he.Code, he.Params)
		}
		return err
	}
	var resp helperapi.Journal
	if err := m.call(r, helperapi.ActionLogJournal, req, &resp); err != nil {
		return err
	}
	if resp.Entries == nil {
		resp.Entries = []helperapi.JournalEntry{}
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) units(w http.ResponseWriter, r *http.Request) error {
	var resp helperapi.Units
	if err := m.call(r, helperapi.ActionLogUnits, nil, &resp); err != nil {
		return err
	}
	if resp.Units == nil {
		resp.Units = []string{}
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}
