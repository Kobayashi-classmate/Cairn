// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"net/http"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

type settingsResponse struct {
	DefaultLocale string `json:"defaultLocale"`
}

func (m *Module) getSettings(w http.ResponseWriter, _ *http.Request) error {
	return api.WriteJSON(w, http.StatusOK, settingsResponse{DefaultLocale: m.Settings.DefaultLocale()})
}

// setDefaultLocale changes the panel-wide default. The config file belongs to
// root, so the change goes through the helper.
func (m *Module) setDefaultLocale(w http.ResponseWriter, r *http.Request) error {
	var req localeRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if !i18n.IsSupported(req.Locale) {
		return localeError(req.Locale)
	}
	p, _ := api.PrincipalFrom(r.Context())
	caller := helperapi.Caller{UserID: p.UserID, Username: p.Username, Remote: api.ClientIP(r), Source: "web"}
	err := m.Helper.Call(r.Context(), helperapi.ActionSetDefaultLocale, caller, helperapi.SetDefaultLocaleRequest{Locale: req.Locale}, nil)
	if he, ok := helperapi.AsError(err); ok {
		m.audit(r, modutil.ActorOf(r), "settings.default_locale", he.Code, map[string]any{"locale": req.Locale})
		status := http.StatusBadGateway
		if he.Code == helperapi.CodeLocaleUnsupport {
			status = http.StatusBadRequest
		}
		return api.NewError(status, he.Code, he.Params)
	}
	if err != nil {
		return err
	}
	m.Settings.setDefaultLocale(req.Locale)
	m.audit(r, modutil.ActorOf(r), "settings.default_locale", "ok", map[string]any{"locale": req.Locale})
	w.WriteHeader(http.StatusNoContent)
	return nil
}
