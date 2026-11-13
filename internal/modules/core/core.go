// SPDX-License-Identifier: AGPL-3.0-only

// Package core is the always-on module: instance metadata, first-run setup,
// sign-in and sign-out, and language preferences.
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
	"github.com/Kobayashi-classmate/Cairn/internal/audit"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/buildinfo"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
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
	Log      *slog.Logger
}

// ID implements module.Module.
func (m *Module) ID() string { return "core" }

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	r.Handle("GET /api/v1/meta", m.meta, api.Public())
	r.Handle("POST /api/v1/setup", m.setup, api.Public())
	r.Handle("POST /api/v1/auth/login", m.login, api.Public())
	r.Handle("POST /api/v1/auth/logout", m.logout)
	r.Handle("GET /api/v1/auth/me", m.me)
	r.Handle("PUT /api/v1/me/locale", m.setMyLocale)
	r.Handle("PUT /api/v1/settings/default-locale", m.setDefaultLocale)
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
		return &api.Principal{UserID: u.ID, Username: u.Username, Locale: u.Locale, SessionID: s.ID, Token: c.Value}, nil
	}
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
	User *auth.User `json:"user"`
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
		m.record(r, 0, req.Username, "auth.setup", "setup.invalid_token")
		return api.NewError(http.StatusBadRequest, "setup.invalid_token", nil)
	case err != nil:
		return fieldError(err)
	}
	m.record(r, u.ID, u.Username, "auth.setup", "ok")
	if err := m.startSession(w, r, u); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, userResponse{User: u})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (m *Module) login(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if req.Username == "" {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "username", "rule": "required"})
	}
	if req.Password == "" {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "password", "rule": "required"})
	}
	ctx := r.Context()
	keys := []string{auth.IPKey(api.ClientIP(r)), auth.UserKey(req.Username)}
	wait, err := auth.LockedFor(ctx, m.DB, keys...)
	if err != nil {
		return err
	}
	if wait > 0 {
		return rateLimited(wait)
	}
	u, err := auth.Authenticate(ctx, m.DB, req.Username, req.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		if err := auth.RecordFailure(ctx, m.DB, keys...); err != nil {
			return err
		}
		m.record(r, 0, req.Username, "auth.login", "auth.invalid_credentials")
		return api.NewError(http.StatusUnauthorized, "auth.invalid_credentials", nil)
	}
	if err != nil {
		return err
	}
	if err := auth.ResetFailures(ctx, m.DB, keys...); err != nil {
		return err
	}
	m.record(r, u.ID, u.Username, "auth.login", "ok")
	if err := m.startSession(w, r, u); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, userResponse{User: u})
}

func (m *Module) logout(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	if err := auth.DeleteSession(r.Context(), m.DB, p.Token); err != nil {
		return err
	}
	m.record(r, p.UserID, p.Username, "auth.logout", "ok")
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) me(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	u, err := auth.UserByID(r.Context(), m.DB, p.UserID)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, userResponse{User: u})
}

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
		m.record(r, p.UserID, p.Username, "settings.default_locale", he.Code)
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
	m.recordParams(r, p.UserID, p.Username, "settings.default_locale", "ok", map[string]any{"locale": req.Locale})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) startSession(w http.ResponseWriter, r *http.Request, u *auth.User) error {
	token, err := auth.CreateSession(r.Context(), m.DB, u.ID, api.ClientIP(r), r.UserAgent())
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: token, Path: "/",
		MaxAge: int(auth.AbsoluteTimeout / time.Second), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func (m *Module) record(r *http.Request, uid int64, username, action, result string) {
	m.recordParams(r, uid, username, action, result, nil)
}

func (m *Module) recordParams(r *http.Request, uid int64, username, action, result string, params map[string]any) {
	err := audit.Record(r.Context(), m.DB, audit.Entry{
		UserID: uid, Username: username, Action: action, Params: params,
		Result: result, Remote: api.ClientIP(r), Source: "web",
	})
	if err != nil {
		m.Log.Error("audit write failed", "action", action, "err", err)
	}
}

func localeError(locale string) error {
	return api.NewError(http.StatusBadRequest, "locale.unsupported", map[string]any{"locale": locale})
}

func rateLimited(wait time.Duration) error {
	secs := int(wait.Round(time.Second) / time.Second)
	return api.NewError(http.StatusTooManyRequests, "auth.rate_limited", map[string]any{"retryAfterSeconds": max(secs, 1)})
}

// fieldError maps validation failures to "validation.invalid".
func fieldError(err error) error {
	var fe *auth.FieldError
	if !errors.As(err, &fe) {
		return err
	}
	params := map[string]any{"field": fe.Field, "rule": fe.Rule}
	if fe.Min > 0 {
		params["min"] = fe.Min
	}
	if fe.Max > 0 {
		params["max"] = fe.Max
	}
	return api.NewError(http.StatusBadRequest, "validation.invalid", params)
}
