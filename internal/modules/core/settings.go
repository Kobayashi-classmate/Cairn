// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

type settingsResponse struct {
	DefaultLocale string `json:"defaultLocale"`
	// StepUpRequired: sensitive operations ask browser sessions to
	// re-authenticate (the step-up switch).
	StepUpRequired bool `json:"stepUpRequired"`
}

func (m *Module) getSettings(w http.ResponseWriter, _ *http.Request) error {
	return api.WriteJSON(w, http.StatusOK, settingsResponse{
		DefaultLocale: m.Settings.DefaultLocale(), StepUpRequired: m.Settings.StepUpRequired(),
	})
}

// The step-up switch, kept in the kv table: "off" turns re-authentication
// for sensitive operations off; anything else, or no row, means on.
const (
	stepUpKey = "security.step_up"
	stepUpOn  = "on"
	stepUpOff = "off"
)

// LoadStepUp reads the stored step-up switch into the settings; call it at
// startup, before serving requests. A missing row means on, the default
// of every new installation.
func (s *Settings) LoadStepUp(ctx context.Context, db *sql.DB) error {
	var v string
	err := db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, stepUpKey).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	s.stepUpOff.Store(v == stepUpOff)
	return nil
}

// StepUpRequired reports whether sensitive operations ask browser sessions
// for a recent re-authentication. It is the router's step-up switch
// (api.Router.SetStepUpRequired), read on every step-up route, so it only
// reads the cached value.
func (s *Settings) StepUpRequired() bool { return !s.stepUpOff.Load() }

// saveStepUp stores the switch, then updates the cached value. Saves are
// serialized so that the cache always ends up as the last stored value.
func (s *Settings) saveStepUp(ctx context.Context, db *sql.DB, on bool) error {
	s.stepUpMu.Lock()
	defer s.stepUpMu.Unlock()
	v := stepUpOff
	if on {
		v = stepUpOn
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO kv(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		stepUpKey, v); err != nil {
		return err
	}
	s.stepUpOff.Store(!on)
	return nil
}

// stepUpBody is the step-up switch as the API sets it. Enabled is a
// pointer so that a body without it is refused rather than read as off.
type stepUpBody struct {
	Enabled *bool `json:"enabled"`
}

type stepUpResponse struct {
	Enabled bool `json:"enabled"`
}

// setStepUp turns re-authentication for sensitive operations on or off.
// The route is a step-up route itself, so turning the switch off takes a
// fresh re-authentication, while turning it back on (it is off then) does
// not. API tokens can do neither.
func (m *Module) setStepUp(w http.ResponseWriter, r *http.Request) error {
	var req stepUpBody
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if req.Enabled == nil {
		return required("enabled")
	}
	on := *req.Enabled
	if err := m.Settings.saveStepUp(r.Context(), m.DB, on); err != nil {
		return err
	}
	m.audit(r, modutil.ActorOf(r), "settings.step_up", "ok", map[string]any{"enabled": on})
	return api.WriteJSON(w, http.StatusOK, stepUpResponse{Enabled: on})
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
