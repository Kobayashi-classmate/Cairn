// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"net/http"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
)

// overviewTimeout bounds the overview: modules that ask the root helper
// and do not answer in time are listed as unavailable.
const overviewTimeout = 8 * time.Second

func (m *Module) overview(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	ov, errs := m.Modules.Summarize(r, p.Can, overviewTimeout)
	for id, err := range errs {
		m.Log.Warn("overview: module unavailable", "module", id, "err", err)
	}
	return api.WriteJSON(w, http.StatusOK, ov)
}

// Summarize implements module.Summarizer: whether the caller's own account
// has two-step sign-in.
func (m *Module) Summarize(r *http.Request, _ func(authz.Permission) bool) (module.Summary, error) {
	p, _ := api.PrincipalFrom(r.Context())
	c := module.Check{Code: "auth.totp_on", Status: module.StatusOK}
	if !p.TOTPEnabled {
		c = module.Check{Code: "auth.totp_off", Status: module.StatusWarn, Route: "settings"}
	}
	return module.Summary{Checks: []module.Check{c}}, nil
}
