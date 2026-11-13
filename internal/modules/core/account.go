// SPDX-License-Identifier: AGPL-3.0-only

package core

import (
	"errors"
	"net/http"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
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
	keys := []string{auth.IPKey(api.ClientIP(r)), auth.UserKey(p.Username)}
	if wait, err := auth.LockedFor(ctx, m.DB, keys...); err != nil {
		return err
	} else if wait > 0 {
		return modutil.RateLimited(wait)
	}
	if err := auth.CheckPassword(ctx, m.DB, p.UserID, req.CurrentPassword); errors.Is(err, auth.ErrWrongPassword) {
		if err := auth.RecordFailure(ctx, m.DB, keys...); err != nil {
			return err
		}
		m.audit(r, modutil.ActorOf(r), "auth.change_password", "auth.wrong_password", nil)
		return modutil.MapError(err, nil)
	} else if err != nil {
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
