// SPDX-License-Identifier: AGPL-3.0-only

// Package auditlog serves the audit trail to users with audit.read. Entries
// are returned structured; the UI renders them in the viewer's language.
package auditlog

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/audit"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
)

// Module implements module.Module.
type Module struct {
	DB *sql.DB
}

// ID implements module.Module.
func (m *Module) ID() string { return "auditlog" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{Nav: []module.NavItem{
		{Key: "audit", LabelKey: "common.nav.audit", Icon: "document-text", Route: "audit", Permission: authz.AuditRead, Group: module.GroupSystem, Order: 90},
	}}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	r.Handle("GET /api/v1/audit", m.list, api.Require(authz.AuditRead),
		api.Doc("audit", "Audit trail, newest first (query: limit, before, user, action prefix, result ok|failed, since and until in Unix seconds)"),
		api.Returns(200, listResponse{}))
	r.Handle("GET /api/v1/audit/{id}", m.get, api.Require(authz.AuditRead), api.Doc("audit", "One audit entry"), api.Returns(200, audit.Entry{}))
}

type listResponse struct {
	Entries    []audit.Entry `json:"entries"`
	NextBefore *int64        `json:"nextBefore"`
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	f := audit.Filter{Username: q.Get("user"), ActionPrefix: q.Get("action")}
	if v := q.Get("limit"); v != "" {
		f.Limit, _ = strconv.Atoi(v)
	}
	if v := q.Get("before"); v != "" {
		f.Before, _ = strconv.ParseInt(v, 10, 64)
	}
	switch v := q.Get("result"); v {
	case "", "ok", "failed":
		f.Result = v
	default:
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "result", "rule": "oneof"})
	}
	for _, p := range []struct {
		name string
		dst  *int64
	}{{"since", &f.Since}, {"until", &f.Until}} {
		if v := q.Get(p.name); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": p.name, "rule": "integer"})
			}
			*p.dst = n
		}
	}
	entries, next, err := audit.List(r.Context(), m.DB, f)
	if err != nil {
		return err
	}
	resp := listResponse{Entries: entries}
	if next > 0 {
		resp.NextBefore = &next
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return api.ErrNotFound
	}
	e, ok, err := audit.Get(r.Context(), m.DB, id)
	if err != nil {
		return err
	}
	if !ok {
		return api.ErrNotFound
	}
	return api.WriteJSON(w, http.StatusOK, e)
}
