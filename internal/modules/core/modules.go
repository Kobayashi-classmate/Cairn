// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"net/http"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

type navResponse struct {
	Items []module.NavItem `json:"items"`
}

func (m *Module) nav(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	return api.WriteJSON(w, http.StatusOK, navResponse{Items: m.Modules.Nav(func(perm authz.Permission) bool { return p.Can(perm) })})
}

type modulesResponse struct {
	Modules []module.State `json:"modules"`
}

func (m *Module) listModules(w http.ResponseWriter, _ *http.Request) error {
	return api.WriteJSON(w, http.StatusOK, modulesResponse{Modules: m.Modules.States()})
}

type moduleRequest struct {
	Enabled bool `json:"enabled"`
}

type moduleResponse struct {
	Module module.State `json:"module"`
}

func (m *Module) setModule(w http.ResponseWriter, r *http.Request) error {
	var req moduleRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	id := r.PathValue("id")
	action := "module.disable"
	if req.Enabled {
		action = "module.enable"
	}
	if err := m.Modules.SetEnabled(r.Context(), id, req.Enabled); err != nil {
		m.audit(r, modutil.ActorOf(r), action, modutil.ErrorCode(err), map[string]any{"module": id})
		return err
	}
	m.audit(r, modutil.ActorOf(r), action, "ok", map[string]any{"module": id})
	for _, st := range m.Modules.States() {
		if st.ID == id {
			return api.WriteJSON(w, http.StatusOK, moduleResponse{Module: st})
		}
	}
	return api.ErrNotFound
}
