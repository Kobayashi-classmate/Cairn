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

// First-login wizard states, kept in the kv table.
const (
	wizardKey     = "setup.wizard"
	wizardPending = "pending" // not finished: shown after sign-in to administrators
	wizardDone    = "done"
	wizardSkipped = "skipped"
)

type wizardBody struct {
	State string `json:"state"` // pending | done | skipped
}

func (m *Module) getWizard(w http.ResponseWriter, r *http.Request) error {
	state := wizardPending
	var v string
	if err := m.DB.QueryRowContext(r.Context(), `SELECT value FROM kv WHERE key = ?`, wizardKey).Scan(&v); err == nil && (v == wizardDone || v == wizardSkipped) {
		state = v
	}
	return api.WriteJSON(w, http.StatusOK, wizardBody{State: state})
}

// putWizard records that the wizard was finished or skipped (or reopens
// it with "pending").
func (m *Module) putWizard(w http.ResponseWriter, r *http.Request) error {
	var req wizardBody
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if req.State != wizardPending && req.State != wizardDone && req.State != wizardSkipped {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "state", "rule": "oneof"})
	}
	if _, err := m.DB.ExecContext(r.Context(), `INSERT INTO kv(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		wizardKey, req.State); err != nil {
		return err
	}
	m.audit(r, modutil.ActorOf(r), "setup.wizard", "ok", map[string]any{"state": req.State})
	return api.WriteJSON(w, http.StatusOK, req)
}
