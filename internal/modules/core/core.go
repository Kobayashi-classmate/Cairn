// SPDX-License-Identifier: AGPL-3.0-only

// Package core is the always-on module: instance metadata, first-run setup,
// sign-in (with an optional TOTP second step), step-up re-authentication,
// the user's own account, and panel settings.
package core

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/buildinfo"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
	"github.com/Kobayashi-classmate/Cairn/internal/secrets"
)

// Settings holds runtime-adjustable values read from the config file.
type Settings struct {
	mu            sync.RWMutex
	defaultLocale string
}

// NewSettings returns settings initialized from the config.
func NewSettings(defaultLocale string) *Settings {
	return &Settings{defaultLocale: defaultLocale}
}

// DefaultLocale returns the panel default language.
func (s *Settings) DefaultLocale() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.defaultLocale
}

func (s *Settings) setDefaultLocale(l string) {
	s.mu.Lock()
	s.defaultLocale = l
	s.mu.Unlock()
}

// Helper is the subset of the helper client the module uses.
type Helper interface {
	Call(ctx context.Context, action string, caller helperapi.Caller, req, resp any) error
}

// Module implements module.Module.
type Module struct {
	DB       *sql.DB
	Settings *Settings
	Helper   Helper
	Box      *secrets.Box
	Log      *slog.Logger
}

// ID implements module.Module.
func (m *Module) ID() string { return "core" }

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	self := api.Require(authz.Self)

	r.Handle("GET /api/v1/meta", m.meta, api.Public())
	r.Handle("POST /api/v1/setup", m.setup, api.Public())
	r.Handle("POST /api/v1/auth/login", m.login, api.Public())
	r.Handle("POST /api/v1/auth/login/mfa", m.loginMFA, api.Public())
	r.Handle("POST /api/v1/auth/logout", m.logout, self)
	r.Handle("GET /api/v1/auth/me", m.me, self)
	r.Handle("POST /api/v1/auth/elevate", m.elevate, self)

	r.Handle("PUT /api/v1/me/locale", m.setMyLocale, self)
	r.Handle("PUT /api/v1/me/password", m.changePassword, self)
	r.Handle("GET /api/v1/me/sessions", m.listSessions, self)
	r.Handle("DELETE /api/v1/me/sessions/{id}", m.revokeSession, self)
	r.Handle("DELETE /api/v1/me/sessions", m.revokeOtherSessions, self)
	r.Handle("POST /api/v1/me/totp/begin", m.beginTOTP, self, api.StepUp())
	r.Handle("POST /api/v1/me/totp/confirm", m.confirmTOTP, self)
	r.Handle("DELETE /api/v1/me/totp", m.disableTOTP, self, api.StepUp())
	r.Handle("POST /api/v1/me/recovery-codes", m.regenerateRecoveryCodes, self, api.StepUp())

	r.Handle("GET /api/v1/settings", m.getSettings, api.Require(authz.SettingsRead))
	r.Handle("PUT /api/v1/settings/default-locale", m.setDefaultLocale, api.Require(authz.SettingsManage))
}

// Authenticator resolves the session cookie to a principal.
func Authenticator(db *sql.DB) api.Authenticator {
	return func(r *http.Request) (*api.Principal, error) {
		c, err := r.Cookie(auth.CookieName)
		if err != nil || c.Value == "" {
			return nil, api.ErrUnauthenticated
		}
		s, u, err := auth.LookupSession(r.Context(), db, c.Value)
		if errors.Is(err, auth.ErrSessionInvalid) {
			return nil, api.NewError(http.StatusUnauthorized, "auth.session_expired", nil)
		}
		if err != nil {
			return nil, err
		}
		return &api.Principal{
			UserID: u.ID, Username: u.Username, Locale: u.Locale, Role: u.Role, TOTPEnabled: u.TOTPEnabled,
			SessionID: s.ID, Token: c.Value, ElevatedUntil: s.ElevatedUntil,
		}, nil
	}
}

func (m *Module) audit(r *http.Request, who modutil.Actor, action, result string, params map[string]any) {
	modutil.Audit(r, m.DB, m.Log, who, action, result, params)
}

type metaResponse struct {
	Version       string   `json:"version"`
	SetupRequired bool     `json:"setupRequired"`
	DefaultLocale string   `json:"defaultLocale"`
	Locales       []string `json:"locales"`
}

func (m *Module) meta(w http.ResponseWriter, r *http.Request) error {
	n, err := auth.CountUsers(r.Context(), m.DB)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, metaResponse{
		Version:       buildinfo.Version,
		SetupRequired: n == 0,
		DefaultLocale: m.Settings.DefaultLocale(),
		Locales:       i18n.Supported,
	})
}

type userResponse struct {
	User        *auth.User `json:"user"`
	Permissions []string   `json:"permissions"`
}

func userBody(u *auth.User) userResponse {
	return userResponse{User: u, Permissions: authz.PermissionsOf(u.Role)}
}

type setupRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
	Locale   string `json:"locale"`
}

func (m *Module) setup(w http.ResponseWriter, r *http.Request) error {
	var req setupRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if req.Locale != "" && !i18n.IsSupported(req.Locale) {
		return localeError(req.Locale)
	}
	u, err := auth.CompleteSetup(r.Context(), m.DB, req.Token, req.Username, req.Password, req.Locale)
	switch {
	case errors.Is(err, auth.ErrSetupDone):
		return api.NewError(http.StatusConflict, "setup.already_completed", nil)
	case errors.Is(err, auth.ErrSetupToken):
		m.audit(r, modutil.Actor{Username: req.Username}, "auth.setup", "setup.invalid_token", nil)
		return api.NewError(http.StatusBadRequest, "setup.invalid_token", nil)
	case err != nil:
		return modutil.MapError(err, nil)
	}
	m.audit(r, modutil.Actor{UserID: u.ID, Username: u.Username}, "auth.setup", "ok", nil)
	if err := m.signIn(w, r, u); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, userBody(u))
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type mfaResponse struct {
	MFARequired bool   `json:"mfaRequired"`
	MFAToken    string `json:"mfaToken"`
}

func required(field string) error {
	return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": field, "rule": "required"})
}

func (m *Module) login(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if req.Username == "" {
		return required("username")
	}
	if req.Password == "" {
		return required("password")
	}
	ctx := r.Context()
	keys := []string{auth.IPKey(api.ClientIP(r)), auth.UserKey(req.Username)}
	if wait, err := auth.LockedFor(ctx, m.DB, keys...); err != nil {
		return err
	} else if wait > 0 {
		return modutil.RateLimited(wait)
	}
	u, err := auth.Authenticate(ctx, m.DB, req.Username, req.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		if err := auth.RecordFailure(ctx, m.DB, keys...); err != nil {
			return err
		}
		m.audit(r, modutil.Actor{Username: req.Username}, "auth.login", "auth.invalid_credentials", nil)
		return api.NewError(http.StatusUnauthorized, "auth.invalid_credentials", nil)
	}
	if err != nil {
		return err
	}
	if u.TOTPEnabled {
		// Password accepted; failures are reset only after the second step.
		token, err := auth.CreateMFAChallenge(ctx, m.DB, u.ID, api.ClientIP(r))
		if err != nil {
			return err
		}
		return api.WriteJSON(w, http.StatusOK, mfaResponse{MFARequired: true, MFAToken: token})
	}
	if err := auth.ResetFailures(ctx, m.DB, keys...); err != nil {
		return err
	}
	m.audit(r, modutil.Actor{UserID: u.ID, Username: u.Username}, "auth.login", "ok", nil)
	if err := m.signIn(w, r, u); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, userBody(u))
}

type loginMFARequest struct {
	MFAToken     string `json:"mfaToken"`
	Code         string `json:"code"`
	RecoveryCode string `json:"recoveryCode"`
}

func (m *Module) loginMFA(w http.ResponseWriter, r *http.Request) error {
	var req loginMFARequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	userID, err := auth.MFAChallengeUser(ctx, m.DB, req.MFAToken)
	if err != nil {
		return modutil.MapError(err, nil)
	}
	u, err := auth.UserByID(ctx, m.DB, userID)
	if err != nil || u.Disabled {
		return modutil.MapError(auth.ErrMFAChallengeExpired, nil)
	}
	keys := []string{auth.IPKey(api.ClientIP(r)), auth.UserKey(u.Username)}
	if wait, err := auth.LockedFor(ctx, m.DB, keys...); err != nil {
		return err
	} else if wait > 0 {
		return modutil.RateLimited(wait)
	}
	method := "totp"
	if req.Code == "" {
		method = "recovery"
	}
	err = auth.VerifySecondFactor(ctx, m.DB, m.Box, u.ID, req.Code, req.RecoveryCode)
	if errors.Is(err, auth.ErrMFAInvalidCode) || errors.Is(err, auth.ErrMFARequired) {
		_ = auth.FailMFAChallenge(ctx, m.DB, req.MFAToken)
		if err := auth.RecordFailure(ctx, m.DB, keys...); err != nil {
			return err
		}
		m.audit(r, modutil.Actor{UserID: u.ID, Username: u.Username}, "auth.login_mfa", modutil.ErrorCode(err), map[string]any{"method": method})
		return modutil.MapError(err, nil)
	}
	if err != nil {
		return err
	}
	if err := auth.EndMFAChallenge(ctx, m.DB, req.MFAToken); err != nil {
		return err
	}
	if err := auth.ResetFailures(ctx, m.DB, keys...); err != nil {
		return err
	}
	m.audit(r, modutil.Actor{UserID: u.ID, Username: u.Username}, "auth.login_mfa", "ok", map[string]any{"method": method})
	if err := m.signIn(w, r, u); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, userBody(u))
}

func (m *Module) logout(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	if err := auth.DeleteSession(r.Context(), m.DB, p.Token); err != nil {
		return err
	}
	m.audit(r, modutil.ActorOf(r), "auth.logout", "ok", nil)
	clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type meResponse struct {
	userResponse
	ElevatedUntil int64 `json:"elevatedUntil"`
}

func (m *Module) me(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	u, err := auth.UserByID(r.Context(), m.DB, p.UserID)
	if err != nil {
		return err
	}
	until := p.ElevatedUntil
	if until <= time.Now().Unix() {
		until = 0
	}
	return api.WriteJSON(w, http.StatusOK, meResponse{userResponse: userBody(u), ElevatedUntil: until})
}

type elevateRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

type elevateResponse struct {
	ElevatedUntil int64 `json:"elevatedUntil"`
}

// elevate re-authenticates the session for sensitive operations: password,
// plus a TOTP code when two-step sign-in is enabled.
func (m *Module) elevate(w http.ResponseWriter, r *http.Request) error {
	var req elevateRequest
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
	fail := func(err error) error {
		if err := auth.RecordFailure(ctx, m.DB, keys...); err != nil {
			return err
		}
		m.audit(r, modutil.ActorOf(r), "auth.elevate", modutil.ErrorCode(err), nil)
		return modutil.MapError(err, nil)
	}
	if err := auth.CheckPassword(ctx, m.DB, p.UserID, req.Password); errors.Is(err, auth.ErrWrongPassword) {
		return fail(err)
	} else if err != nil {
		return err
	}
	if p.TOTPEnabled {
		if req.Code == "" {
			return modutil.MapError(auth.ErrMFARequired, nil)
		}
		if err := auth.VerifySecondFactor(ctx, m.DB, m.Box, p.UserID, req.Code, ""); errors.Is(err, auth.ErrMFAInvalidCode) {
			return fail(err)
		} else if err != nil {
			return err
		}
	}
	until, err := auth.Elevate(ctx, m.DB, p.SessionID)
	if err != nil {
		return err
	}
	if err := auth.ResetFailures(ctx, m.DB, keys...); err != nil {
		return err
	}
	m.audit(r, modutil.ActorOf(r), "auth.elevate", "ok", nil)
	return api.WriteJSON(w, http.StatusOK, elevateResponse{ElevatedUntil: until})
}

func (m *Module) signIn(w http.ResponseWriter, r *http.Request, u *auth.User) error {
	token, err := auth.CreateSession(r.Context(), m.DB, u.ID, api.ClientIP(r), r.UserAgent())
	if err != nil {
		return err
	}
	if err := auth.TouchLogin(r.Context(), m.DB, u.ID); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: token, Path: "/",
		MaxAge: int(auth.AbsoluteTimeout / time.Second), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func localeError(locale string) error {
	return api.NewError(http.StatusBadRequest, "locale.unsupported", map[string]any{"locale": locale})
}
