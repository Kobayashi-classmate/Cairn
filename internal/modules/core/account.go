// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

type localeRequest struct {
	Locale string `json:"locale"`
}

func (m *Module) setMyLocale(w http.ResponseWriter, r *http.Request) error {
	var req localeRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if !i18n.IsSupported(req.Locale) {
		return localeError(req.Locale)
	}
	p, _ := api.PrincipalFrom(r.Context())
	if err := auth.SetLocale(r.Context(), m.DB, p.UserID, req.Locale); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type themeRequest struct {
	Theme string `json:"theme"` // light, dark or system
}

// setMyTheme stores the signed-in user's colour scheme; the UI applies it on
// every device the user signs in from.
func (m *Module) setMyTheme(w http.ResponseWriter, r *http.Request) error {
	var req themeRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	p, _ := api.PrincipalFrom(r.Context())
	if err := auth.SetTheme(r.Context(), m.DB, p.UserID, req.Theme); err != nil {
		return modutil.MapError(err, nil)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type terminalSchemeRequest struct {
	Scheme string `json:"scheme"` // one of auth.TerminalSchemes
}

// setMyTerminalScheme stores the colour scheme of the signed-in user's web
// terminal, for every device the user signs in from.
func (m *Module) setMyTerminalScheme(w http.ResponseWriter, r *http.Request) error {
	var req terminalSchemeRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	p, _ := api.PrincipalFrom(r.Context())
	if err := auth.SetTerminalScheme(r.Context(), m.DB, p.UserID, req.Scheme); err != nil {
		return modutil.MapError(err, nil)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// changePassword requires the current password and signs out every other
// session of the user.
func (m *Module) changePassword(w http.ResponseWriter, r *http.Request) error {
	var req changePasswordRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	p, _ := api.PrincipalFrom(ctx)
	// As for step-up, failures count for this session only.
	keys := []string{auth.SessionKey(p.SessionID)}
	if err := m.attempt(r, keys...); err != nil {
		return err
	}
	if err := auth.CheckPassword(ctx, m.DB, p.UserID, req.CurrentPassword); errors.Is(err, auth.ErrWrongPassword) {
		m.failed(r, p.Username, keys...)
		m.audit(r, modutil.ActorOf(r), "auth.change_password", "auth.wrong_password", nil)
		return modutil.MapError(err, nil)
	} else if err != nil {
		return modutil.MapError(err, nil)
	}
	if err := auth.ResetFailures(ctx, m.DB, keys...); err != nil {
		return err
	}
	if err := auth.SetPassword(ctx, m.DB, p.UserID, req.NewPassword); err != nil {
		return modutil.MapError(err, map[string]string{"password": "newPassword"})
	}
	if _, err := auth.DeleteOtherSessions(ctx, m.DB, p.UserID, p.SessionID); err != nil {
		return err
	}
	m.audit(r, modutil.ActorOf(r), "auth.change_password", "ok", nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type sessionsResponse struct {
	Sessions []*auth.Session `json:"sessions"`
}

func (m *Module) listSessions(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	list, err := auth.ListSessions(r.Context(), m.DB, p.UserID, p.SessionID)
	if err != nil {
		return err
	}
	if list == nil {
		list = []*auth.Session{}
	}
	return api.WriteJSON(w, http.StatusOK, sessionsResponse{Sessions: list})
}

func (m *Module) revokeSession(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	id := r.PathValue("id")
	ok, err := auth.DeleteSessionByID(r.Context(), m.DB, p.UserID, id)
	if err != nil {
		return err
	}
	if !ok {
		return api.ErrNotFound
	}
	m.audit(r, modutil.ActorOf(r), "auth.session_revoke", "ok", map[string]any{"count": 1})
	if id == p.SessionID {
		clearCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type revokedResponse struct {
	Revoked int64 `json:"revoked"`
}

func (m *Module) revokeOtherSessions(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	n, err := auth.DeleteOtherSessions(r.Context(), m.DB, p.UserID, p.SessionID)
	if err != nil {
		return err
	}
	m.audit(r, modutil.ActorOf(r), "auth.session_revoke", "ok", map[string]any{"count": n})
	return api.WriteJSON(w, http.StatusOK, revokedResponse{Revoked: n})
}

type totpBeginResponse struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

func (m *Module) beginTOTP(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	u, err := auth.UserByID(r.Context(), m.DB, p.UserID)
	if err != nil {
		return err
	}
	secret, uri, err := auth.BeginTOTP(r.Context(), m.DB, m.Box, u)
	if err != nil {
		return modutil.MapError(err, nil)
	}
	return api.WriteJSON(w, http.StatusOK, totpBeginResponse{Secret: secret, URI: uri})
}

type codeRequest struct {
	Code string `json:"code"`
}

type recoveryCodesResponse struct {
	RecoveryCodes []string `json:"recoveryCodes"`
}

func (m *Module) confirmTOTP(w http.ResponseWriter, r *http.Request) error {
	var req codeRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	p, _ := api.PrincipalFrom(r.Context())
	codes, err := auth.ConfirmTOTP(r.Context(), m.DB, m.Box, p.UserID, req.Code)
	if err != nil {
		return modutil.MapError(err, nil)
	}
	m.audit(r, modutil.ActorOf(r), "auth.totp_enable", "ok", nil)
	return api.WriteJSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
}

func (m *Module) disableTOTP(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	if err := auth.DisableTOTP(r.Context(), m.DB, p.UserID); err != nil {
		return err
	}
	m.audit(r, modutil.ActorOf(r), "auth.totp_disable", "ok", nil)
	m.Events.Publish(events.Event{Type: events.SecurityTOTPDisabled, Level: events.Warning,
		Params: map[string]any{"username": p.Username, "actor": p.Username}})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type tokensResponse struct {
	Tokens []*auth.Token `json:"tokens"`
}

func (m *Module) listTokens(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	list, err := auth.ListTokens(r.Context(), m.DB, p.UserID)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, tokensResponse{Tokens: list})
}

type createTokenRequest struct {
	Name          string   `json:"name"`
	Permissions   []string `json:"permissions"`
	ExpiresInDays int      `json:"expiresInDays"`
	AllowedIPs    []string `json:"allowedIps"`
}

type createTokenResponse struct {
	Token string      `json:"token"`
	Info  *auth.Token `json:"info"`
}

func (m *Module) createToken(w http.ResponseWriter, r *http.Request) error {
	var req createTokenRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	p, _ := api.PrincipalFrom(r.Context())
	u, err := auth.UserByID(r.Context(), m.DB, p.UserID)
	if err != nil {
		return err
	}
	token, info, err := auth.CreateToken(r.Context(), m.DB, u, auth.TokenRequest{
		Name: req.Name, Permissions: req.Permissions, ExpiresInDays: req.ExpiresInDays, AllowedIPs: req.AllowedIPs,
	})
	if err != nil {
		return modutil.MapError(err, nil)
	}
	m.audit(r, modutil.ActorOf(r), "auth.token_create", "ok", map[string]any{"name": info.Name})
	return api.WriteJSON(w, http.StatusCreated, createTokenResponse{Token: token, Info: info})
}

func (m *Module) deleteToken(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return api.ErrNotFound
	}
	p, _ := api.PrincipalFrom(r.Context())
	name, err := auth.DeleteToken(r.Context(), m.DB, p.UserID, id)
	if err != nil {
		return modutil.MapError(err, nil)
	}
	m.audit(r, modutil.ActorOf(r), "auth.token_revoke", "ok", map[string]any{"name": name})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	u, err := auth.UserByID(r.Context(), m.DB, p.UserID)
	if err != nil {
		return err
	}
	codes, err := auth.RegenerateRecoveryCodes(r.Context(), m.DB, u)
	if err != nil {
		return modutil.MapError(err, nil)
	}
	m.audit(r, modutil.ActorOf(r), "auth.recovery_codes", "ok", nil)
	return api.WriteJSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
}
