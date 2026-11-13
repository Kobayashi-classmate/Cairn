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
)

// Module implements module.Module.
type Module struct {
	DB *sql.DB
}

// ID implements module.Module.
func (m *Module) ID() string { return "auditlog" }

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	r.Handle("GET /api/v1/audit", m.list, api.Require(authz.AuditRead))
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
