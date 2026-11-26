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
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/buildinfo"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
	"github.com/Kobayashi-classmate/Cairn/internal/secrets"
)

// Settings holds runtime-adjustable panel settings: the default language
// from the config file, and the step-up switch from the database (see
// LoadStepUp).
type Settings struct {
	mu            sync.RWMutex
	defaultLocale string

	// stepUpOff caches the step-up switch, which every step-up route
	// reads; the zero value is on. stepUpMu orders saving it.
	stepUpOff atomic.Bool
	stepUpMu  sync.Mutex
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
	Events   *events.Bus // may be nil
	Log      *slog.Logger
	// Modules is the registry of the running panel (menu, module switches).
	Modules *module.Registry

	// router knows which routes take tickets (set by Routes).
	router *api.Router
}

// ID implements module.Module.
func (m *Module) ID() string { return "core" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{Nav: []module.NavItem{
		{Key: "settings", LabelKey: "common.nav.settings", Icon: "settings", Route: "settings", Group: module.GroupSystem, Order: 100},
	}}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	m.router = r
	self := api.Require(authz.Self)
	// The account's own security (password, sessions, tokens, two-step
	// sign-in) is never reachable with an API token.
	account := api.SessionOnly()
	authDoc := func(s string) api.RouteOption { return api.Doc("auth", s) }
	accountDoc := func(s string) api.RouteOption { return api.Doc("account", s) }
	settingsDoc := func(s string) api.RouteOption { return api.Doc("settings", s) }
	noContent := api.Returns(http.StatusNoContent, nil)

	r.Handle("GET /api/v1/meta", m.meta, api.Public(), api.Doc("meta", "Instance metadata and supported languages"), api.Returns(200, metaResponse{}))
	r.Handle("POST /api/v1/setup", m.setup, api.Public(), authDoc("Create the first administrator with the setup token; answers with the session key"),
		api.Body(setupRequest{}), api.Returns(200, sessionResponse{}))
	r.Handle("POST /api/v1/auth/login", m.login, api.Public(), authDoc("Sign in; answers with the session key, or with a second-step challenge"),
		api.Body(loginRequest{}), api.Returns(200, loginResponse{}))
	r.Handle("POST /api/v1/auth/login/mfa", m.loginMFA, api.Public(), authDoc("Complete sign-in with a TOTP or recovery code; answers with the session key"),
		api.Body(loginMFARequest{}), api.Returns(200, sessionResponse{}))
	r.Handle("POST /api/v1/auth/logout", m.logout, self, authDoc("Sign out this session"), api.NoBody(), noContent)
	r.Handle("GET /api/v1/auth/me", m.me, self, authDoc("Current user, permissions and step-up state"), api.Returns(200, meResponse{}))
	r.Handle("POST /api/v1/auth/elevate", m.elevate, self, account, authDoc("Re-authenticate for sensitive actions (step-up)"),
		api.Body(elevateRequest{}), api.Returns(200, elevateResponse{}))
	r.Handle("POST /api/v1/auth/tickets", m.issueTicket, self, account,
		authDoc("Get a one-time ticket for a request that cannot carry the session key (WebSocket, download link)"),
		api.Body(ticketRequest{}), api.Returns(200, ticketResponse{}))

	r.Handle("PUT /api/v1/me/locale", m.setMyLocale, self, accountDoc("Set my language"), api.Body(localeRequest{}), noContent)
	r.Handle("PUT /api/v1/me/theme", m.setMyTheme, self, accountDoc("Set my colour scheme: light, dark or system"), api.Body(themeRequest{}), noContent)
	r.Handle("PUT /api/v1/me/terminal-scheme", m.setMyTerminalScheme, self, accountDoc("Set my web terminal's colour scheme"), api.Body(terminalSchemeRequest{}), noContent)
	r.Handle("PUT /api/v1/me/password", m.changePassword, self, account, accountDoc("Change my password; signs out other sessions"),
		api.Body(changePasswordRequest{}), noContent)
	r.Handle("GET /api/v1/me/sessions", m.listSessions, self, account, accountDoc("List my sessions"), api.Returns(200, sessionsResponse{}))
	r.Handle("DELETE /api/v1/me/sessions/{id}", m.revokeSession, self, account, accountDoc("Revoke one of my sessions"), noContent)
	r.Handle("DELETE /api/v1/me/sessions", m.revokeOtherSessions, self, account, accountDoc("Revoke all my other sessions"), api.Returns(200, revokedResponse{}))
	r.Handle("POST /api/v1/me/totp/begin", m.beginTOTP, self, account, api.StepUp(), accountDoc("Start two-step sign-in enrollment"), api.NoBody(),
		api.Returns(200, totpBeginResponse{}))
	r.Handle("POST /api/v1/me/totp/confirm", m.confirmTOTP, self, account, accountDoc("Confirm enrollment and get recovery codes"),
		api.Body(codeRequest{}), api.Returns(200, recoveryCodesResponse{}))
	r.Handle("DELETE /api/v1/me/totp", m.disableTOTP, self, account, api.StepUp(), accountDoc("Turn off two-step sign-in"), noContent)
	r.Handle("POST /api/v1/me/recovery-codes", m.regenerateRecoveryCodes, self, account, api.StepUp(), accountDoc("Replace my recovery codes"), api.NoBody(),
		api.Returns(200, recoveryCodesResponse{}))
	r.Handle("GET /api/v1/me/tokens", m.listTokens, self, account, accountDoc("List my API tokens"), api.Returns(200, tokensResponse{}))
	r.Handle("POST /api/v1/me/tokens", m.createToken, self, account, api.StepUp(), accountDoc("Create an API token (shown once)"),
		api.Body(createTokenRequest{}), api.Returns(201, createTokenResponse{}))
	r.Handle("DELETE /api/v1/me/tokens/{id}", m.deleteToken, self, account, accountDoc("Revoke one of my API tokens"), noContent)

	r.Handle("GET /api/v1/settings", m.getSettings, api.Require(authz.SettingsRead), settingsDoc("Panel settings"),
		api.Returns(200, settingsResponse{}))
	r.Handle("GET /api/v1/setup/wizard", m.getWizard, self, settingsDoc("Whether the first-login wizard is still to be shown"),
		api.Returns(200, wizardBody{}))
	r.Handle("PUT /api/v1/setup/wizard", m.putWizard, api.Require(authz.SettingsManage), api.Body(wizardBody{}),
		settingsDoc("Mark the first-login wizard finished, skipped or pending again"), api.Returns(200, wizardBody{}))
	r.Handle("PUT /api/v1/settings/default-locale", m.setDefaultLocale, api.Require(authz.SettingsManage),
		settingsDoc("Change the panel default language"), api.Body(localeRequest{}), noContent)
	r.Handle("PUT /api/v1/settings/step-up", m.setStepUp, api.Require(authz.SettingsManage), api.StepUp(),
		settingsDoc("Turn re-authentication for sensitive operations on or off (API tokens stay refused on step-up routes)"),
		api.Body(stepUpBody{}), api.Returns(200, stepUpResponse{}))

	r.Handle("GET /api/v1/overview", m.overview, self, api.Doc("meta", "The overview page: services, checks and notices of the modules the caller may read"),
		api.Returns(200, module.Overview{}))
	r.Handle("GET /api/v1/ui/nav", m.nav, self, api.Doc("meta", "Menu entries of the enabled modules the caller may open"),
		api.Returns(200, navResponse{}))
	r.Handle("GET /api/v1/modules", m.listModules, api.Require(authz.SettingsRead), settingsDoc("Modules and whether they are on"),
		api.Returns(200, modulesResponse{}))
	r.Handle("PUT /api/v1/modules/{id}", m.setModule, api.Require(authz.SettingsManage), api.StepUp(),
		settingsDoc("Turn an optional module on or off"), api.Body(moduleRequest{}), api.Returns(200, moduleResponse{}))
}

// loginResponse documents both shapes of a successful POST /auth/login.
type loginResponse struct {
	userResponse
	SessionKey     string `json:"sessionKey,omitempty"`
	DeviceKey      string `json:"deviceKey,omitempty"`
	ElevatedUntil  int64  `json:"elevatedUntil,omitempty"`
	StepUpRequired bool   `json:"stepUpRequired,omitempty"`
	MFARequired    bool   `json:"mfaRequired,omitempty"`
	MFAToken       string `json:"mfaToken,omitempty"`
}

// Authenticator resolves a browser session, or an API token presented as
// "Authorization: Bearer", to a principal. Token requests never consult
// cookies.
//
// A session takes the cookie and the session key in its header, both: the
// cookie also reaches whatever else answers on the panel's host name (a
// website on another port), the key does not. On the routes that take
// tickets, a one-time ticket in the address may stand in for the key.
// Whatever is missing or wrong, the answer says nothing about the other
// half.
func Authenticator(db *sql.DB) api.Authenticator {
	return func(r *http.Request) (*api.Principal, error) {
		if api.IsBearer(r) {
			return tokenPrincipal(r, db)
		}
		c, err := r.Cookie(auth.CookieName)
		if err != nil || c.Value == "" {
			return nil, api.ErrUnauthenticated
		}
		var s *auth.Session
		var u *auth.User
		key := r.Header.Get(api.SessionHeader)
		ticket := ""
		if key == "" && api.TakesTicket(r.Context()) {
			ticket = r.URL.Query().Get(api.TicketParam)
		}
		switch {
		case key != "":
			s, u, err = auth.LookupSession(r.Context(), db, c.Value, key)
		case ticket != "":
			s, u, err = auth.RedeemTicket(r.Context(), db, c.Value, ticket, auth.TicketTarget(r.URL.Path, r.URL.Query(), api.TicketParam))
		default:
			return nil, api.ErrUnauthenticated
		}
		if errors.Is(err, auth.ErrSessionInvalid) {
			return nil, api.NewError(http.StatusUnauthorized, "auth.session_expired", nil)
		}
		if err != nil {
			return nil, err
		}
		return &api.Principal{
			UserID: u.ID, Username: u.Username, Locale: u.Locale, Role: u.Role, TOTPEnabled: u.TOTPEnabled,
			SessionID: s.ID, Token: c.Value, ElevatedUntil: s.ElevatedUntil, Source: "web",
		}, nil
	}
}

// tokenPrincipal authenticates an API token; repeated bad tokens from one
// address are throttled like failed sign-ins.
func tokenPrincipal(r *http.Request, db *sql.DB) (*api.Principal, error) {
	ctx := r.Context()
	key := auth.IPKey(api.ClientIP(r))
	if wait, err := auth.LockedFor(ctx, db, key); err != nil {
		return nil, err
	} else if wait > 0 {
		return nil, modutil.RateLimited(wait)
	}
	t, u, err := auth.LookupToken(ctx, db, api.BearerToken(r), api.ClientIP(r))
	if errors.Is(err, auth.ErrTokenInvalid) {
		if err := auth.RecordFailure(ctx, db, key); err != nil {
			return nil, err
		}
		return nil, modutil.MapError(err, nil)
	}
	if err != nil {
		return nil, err
	}
	scopes := append([]string{}, t.Permissions...) // non-nil marks a token principal
	return &api.Principal{
		UserID: u.ID, Username: u.Username, Locale: u.Locale, Role: u.Role, TOTPEnabled: u.TOTPEnabled,
		Scopes: scopes, Source: "api",
	}, nil
}

// attempt takes an attempt slot on the throttle keys before credentials
// are checked (see auth.Attempt): the attempt already counts as a failure,
// so a failed check needs nothing more than failed.
func (m *Module) attempt(r *http.Request, keys ...string) error {
	wait, err := auth.Attempt(r.Context(), m.DB, keys...)
	if err != nil {
		return err
	}
	if wait > 0 {
		return modutil.RateLimited(wait)
	}
	return nil
}

// failed announces a lockout when the failed check (counted by attempt)
// locked one of the keys. One notice per address: the name tried is the
// sender's to choose, and a new one for every attempt must not make each
// a new notice.
func (m *Module) failed(r *http.Request, username string, keys ...string) {
	wait, err := auth.LockedFor(r.Context(), m.DB, keys...)
	if err == nil && wait > 0 {
		ip := api.ClientIP(r)
		m.Events.Publish(events.Event{
			Type: events.SecurityLockout, Level: events.Warning, DedupeKey: "lockout:" + auth.IPKey(ip),
			Params: map[string]any{"username": logName(username), "ip": ip, "minutes": int(wait.Round(time.Minute) / time.Minute)},
		})
	}
}

// maxLogName bounds a username as tried, where it is recorded.
const maxLogName = 64

// logName is a username someone tried, as recorded in the audit log and a
// lockout notice: any text an anonymous client sent, so cut short and
// without control characters (line breaks, terminal escapes).
func logName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(name, "\uFFFD"))
	if len(name) <= maxLogName {
		return name
	}
	cut := maxLogName
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut]
}

// finishTimeout bounds what a sign-in still does once its credentials are
// checked: a few database writes.
const finishTimeout = 15 * time.Second

// finish gives what is left of a request whose credentials have been
// checked a context of its own. A client that goes away now, or half-closes
// its connection to end the request's context, must leave the attempt
// audited, the lockout announced and the throttle up to date; and with the
// right password it must not get an error where a wrong one gets an
// answer, which would tell the two apart.
func finish(r *http.Request) (*http.Request, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), finishTimeout)
	return r.WithContext(ctx), cancel
}

// knownDevice reports whether this browser signed in as username before:
// it sends the device cookie and, in its header, the device key.
func (m *Module) knownDevice(r *http.Request, username string) (bool, error) {
	c, err := r.Cookie(auth.DeviceCookieName)
	if err != nil {
		return false, nil
	}
	return auth.KnownDevice(r.Context(), m.DB, c.Value, r.Header.Get(api.DeviceHeader), username)
}

// signInKeys are the throttle keys of a sign-in as username: the address,
// and the username unless this browser signed in as it before (a known
// device), whose owner others must not be able to lock out.
func (m *Module) signInKeys(r *http.Request, username string) ([]string, error) {
	keys := []string{auth.IPKey(api.ClientIP(r))}
	known, err := m.knownDevice(r, username)
	if err != nil {
		return nil, err
	}
	if !known {
		keys = append(keys, auth.UserKey(username))
	}
	return keys, nil
}

func (m *Module) announceLogin(r *http.Request, u *auth.User) {
	m.Events.Publish(events.Event{Type: events.SecurityLogin, Level: events.Info,
		Params: map[string]any{"username": u.Username, "ip": api.ClientIP(r)}})
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

// sessionResponse answers a completed sign-in. Only this answer carries the
// session key, and the device key of a browser not seen before; neither is
// logged or audited. The step-up state comes with it, as /auth/me reports
// it, so the page knows it without asking.
type sessionResponse struct {
	userResponse
	SessionKey string `json:"sessionKey"`
	DeviceKey  string `json:"deviceKey,omitempty"`
	stepUpState
}

// stepUpState is the step-up state of a session: until when it is
// re-authenticated (Unix seconds, 0 when not), and whether sensitive
// operations ask for that at all (the panel's step-up switch).
type stepUpState struct {
	ElevatedUntil  int64 `json:"elevatedUntil"`
	StepUpRequired bool  `json:"stepUpRequired"`
}

func (m *Module) stepUpOf(elevatedUntil int64) stepUpState {
	if elevatedUntil <= time.Now().Unix() {
		elevatedUntil = 0
	}
	return stepUpState{ElevatedUntil: elevatedUntil, StepUpRequired: m.Settings.StepUpRequired()}
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
	done := api.NewError(http.StatusConflict, "setup.already_completed", nil)
	if n, err := auth.CountUsers(r.Context(), m.DB); err != nil {
		return err
	} else if n > 0 {
		return done
	}
	// Tokens are tried like passwords: counted per address, and each wrong
	// one audited without the rest of what was sent.
	key := auth.IPKey(api.ClientIP(r))
	if err := m.attempt(r, key); err != nil {
		return err
	}
	u, err := auth.CompleteSetup(r.Context(), m.DB, req.Token, req.Username, req.Password, req.Locale)
	switch {
	case errors.Is(err, auth.ErrSetupDone):
		return done
	case errors.Is(err, auth.ErrSetupToken):
		m.audit(r, modutil.Actor{}, "auth.setup", "setup.invalid_token", nil)
		return api.NewError(http.StatusBadRequest, "setup.invalid_token", nil)
	case err != nil:
		return modutil.MapError(err, nil)
	}
	r, cancel := finish(r)
	defer cancel()
	if err := auth.ResetFailures(r.Context(), m.DB, key); err != nil {
		return err
	}
	m.audit(r, modutil.Actor{UserID: u.ID, Username: u.Username}, "auth.setup", "ok", nil)
	// The password was entered a moment ago: the new session starts
	// re-authenticated, so that the first-run wizard's settings (the
	// step-up switch among them) save without asking for it again.
	return m.signIn(w, r, u, true)
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
	// A name no account can have is wrong credentials like any other, but
	// gets no throttle key of its own: anyone could make up any number of
	// them, of any size.
	keys := []string{auth.IPKey(api.ClientIP(r))}
	if auth.ValidateUsername(req.Username) == nil {
		var err error
		if keys, err = m.signInKeys(r, req.Username); err != nil {
			return err
		}
	}
	if err := m.attempt(r, keys...); err != nil {
		return err
	}
	// The password check waits for a hashing slot only as long as the
	// client does; what follows it is finished either way.
	u, err := auth.Authenticate(r.Context(), m.DB, req.Username, req.Password)
	r, cancel := finish(r)
	defer cancel()
	ctx := r.Context()
	if errors.Is(err, auth.ErrInvalidCredentials) {
		m.failed(r, req.Username, keys...)
		m.audit(r, modutil.Actor{Username: logName(req.Username)}, "auth.login", "auth.invalid_credentials", nil)
		return api.NewError(http.StatusUnauthorized, "auth.invalid_credentials", nil)
	}
	if err != nil {
		return modutil.MapError(err, nil)
	}
	if u.TOTPEnabled {
		// Password accepted; the attempt stays counted until the second
		// step succeeds.
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
	m.announceLogin(r, u)
	return m.signIn(w, r, u, false)
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
	// The throttle first: a refused attempt does not use up the challenge.
	keys, err := m.signInKeys(r, u.Username)
	if err != nil {
		return err
	}
	if err := m.attempt(r, keys...); err != nil {
		return err
	}
	// Claiming spends one of the challenge's attempts before the code is
	// checked, atomically.
	if _, err := auth.ClaimMFAChallenge(ctx, m.DB, req.MFAToken); err != nil {
		return modutil.MapError(err, nil)
	}
	r, cancel := finish(r)
	defer cancel()
	ctx = r.Context()
	method := "totp"
	if req.Code == "" {
		method = "recovery"
	}
	err = auth.VerifySecondFactor(ctx, m.DB, m.Box, u.ID, req.Code, req.RecoveryCode)
	if errors.Is(err, auth.ErrMFAInvalidCode) || errors.Is(err, auth.ErrMFARequired) {
		m.failed(r, u.Username, keys...)
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
	m.announceLogin(r, u)
	return m.signIn(w, r, u, false)
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
	stepUpState
}

func (m *Module) me(w http.ResponseWriter, r *http.Request) error {
	p, _ := api.PrincipalFrom(r.Context())
	u, err := auth.UserByID(r.Context(), m.DB, p.UserID)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, meResponse{userResponse: userBody(u), stepUpState: m.stepUpOf(p.ElevatedUntil)})
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
	if p.IsToken() || p.SessionID == "" {
		// Step-up belongs to interactive sessions; tokens cannot elevate.
		return api.ErrForbidden
	}
	// Failures count for this session only: whoever knows the username
	// must not be able to lock its owner out of step-up.
	keys := []string{auth.SessionKey(p.SessionID)}
	if err := m.attempt(r, keys...); err != nil {
		return err
	}
	fail := func(err error) error {
		m.failed(r, p.Username, keys...)
		m.audit(r, modutil.ActorOf(r), "auth.elevate", modutil.ErrorCode(err), nil)
		return modutil.MapError(err, nil)
	}
	err := auth.CheckPassword(ctx, m.DB, p.UserID, req.Password)
	r, cancel := finish(r)
	defer cancel()
	ctx = r.Context()
	if errors.Is(err, auth.ErrWrongPassword) {
		return fail(err)
	} else if err != nil {
		return modutil.MapError(err, nil)
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

// signIn starts a session and answers the sign-in: the cookie, and in the
// body the session key that every later request has to send with it. With
// elevated, the session starts re-authenticated (first-run setup).
func (m *Module) signIn(w http.ResponseWriter, r *http.Request, u *auth.User, elevated bool) error {
	token, key, err := auth.CreateSession(r.Context(), m.DB, u.ID, api.ClientIP(r), r.UserAgent())
	if err != nil {
		return err
	}
	if err := auth.TouchLogin(r.Context(), m.DB, u.ID); err != nil {
		return err
	}
	var until int64
	if elevated {
		if until, err = auth.Elevate(r.Context(), m.DB, auth.SessionID(token)); err != nil {
			return err
		}
	}
	resp := sessionResponse{userResponse: userBody(u), SessionKey: key, stepUpState: m.stepUpOf(until)}
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: token, Path: "/",
		MaxAge: int(auth.AbsoluteTimeout / time.Second), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	// This browser is now a known device of the account (see signInKeys).
	known, err := m.knownDevice(r, u.Username)
	if err != nil {
		return err
	}
	if !known {
		device, deviceKey, err := auth.RememberDevice(r.Context(), m.DB, u.ID)
		if err != nil {
			return err
		}
		http.SetCookie(w, &http.Cookie{
			Name: auth.DeviceCookieName, Value: device, Path: "/",
			MaxAge: int(auth.DeviceLifetime / time.Second), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})
		resp.DeviceKey = deviceKey
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

type ticketRequest struct {
	// Path is the request the ticket is for: the path and query of a GET
	// that takes tickets, as it will be sent (without the ticket).
	Path string `json:"path"`
}

type ticketResponse struct {
	Ticket string `json:"ticket"`
	// ExpiresIn is how many seconds the ticket waits to be used.
	ExpiresIn int `json:"expiresIn"`
}

// maxTicketPath bounds the address a ticket is asked for; file paths make
// the longest ones.
const maxTicketPath = 16 << 10

// issueTicket trades the session key (this request carried it) for a
// one-time ticket for one request the browser will make by itself. What
// that request needs otherwise (permission, step-up) is checked when the
// ticket is used.
func (m *Module) issueTicket(w http.ResponseWriter, r *http.Request) error {
	var req ticketRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	p, _ := api.PrincipalFrom(r.Context())
	bad := api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "path", "rule": "ticket_path"})
	if req.Path == "" || len(req.Path) > maxTicketPath {
		return bad
	}
	u, err := url.Parse(req.Path)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Fragment != "" || u.Opaque != "" ||
		!strings.HasPrefix(u.Path, "/") || path.Clean(u.Path) != u.Path || !m.router.TicketRoute(u) {
		return bad
	}
	ticket, err := auth.IssueTicket(r.Context(), m.DB, p.SessionID, auth.TicketTarget(u.Path, u.Query(), api.TicketParam))
	if err != nil {
		return modutil.MapError(err, nil)
	}
	return api.WriteJSON(w, http.StatusOK, ticketResponse{Ticket: ticket, ExpiresIn: int(auth.TicketTTL / time.Second)})
}

func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func localeError(locale string) error {
	return api.NewError(http.StatusBadRequest, "locale.unsupported", map[string]any{"locale": locale})
}
