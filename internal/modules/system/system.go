// SPDX-License-Identifier: AGPL-3.0-only

// Package system serves the overview page: host information, live resource
// usage and the recent history.
package system

import (
	"net/http"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/metrics"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
)

// Module implements module.Module.
type Module struct {
	Metrics *metrics.Collector
}

// ID implements module.Module.
func (m *Module) ID() string { return "system" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{Nav: []module.NavItem{
		{Key: "overview", LabelKey: "common.nav.overview", Icon: "speedometer", Route: "overview", Permission: authz.SystemRead, Order: 10},
	}}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	r.Handle("GET /api/v1/system/overview", func(w http.ResponseWriter, _ *http.Request) error {
		return api.WriteJSON(w, http.StatusOK, m.Metrics.Overview())
	}, api.Require(authz.SystemRead), api.Doc("system", "Host information, live usage and recent history"), api.Returns(200, metrics.Overview{}))
}
