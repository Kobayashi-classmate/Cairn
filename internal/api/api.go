// SPDX-License-Identifier: AGPL-3.0-only

// Package api provides the HTTP API framework: explicit route registration,
// structured errors (a code plus parameters, never pre-rendered sentences),
// strict JSON decoding, security headers, CSRF protection and session
// authentication. Every route is registered explicitly; nothing is dispatched
// by name from request data.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/authz"
)

// Error is an API failure: an HTTP status, a stable code that the UI
// translates, and optional parameters.
type Error struct {
	Status int
	Code   string
	Params map[string]any
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s %v", e.Status, e.Code, e.Params) }

// NewError builds an *Error.
func NewError(status int, code string, params map[string]any) *Error {
	return &Error{Status: status, Code: code, Params: params}
}

// Common errors.
var (
	ErrUnauthenticated = NewError(http.StatusUnauthorized, "auth.unauthenticated", nil)
	ErrCSRF            = NewError(http.StatusForbidden, "request.csrf_failed", nil)
	ErrInvalidJSON     = NewError(http.StatusBadRequest, "request.invalid_json", nil)
	ErrNotFound        = NewError(http.StatusNotFound, "request.not_found", nil)
	ErrInternal        = NewError(http.StatusInternalServerError, "internal.error", nil)
)

// HandlerFunc is an API handler; a returned error is written as JSON.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// WriteJSON writes v with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error struct {
		Code   string         `json:"code"`
		Params map[string]any `json:"params,omitempty"`
	} `json:"error"`
}

// writeError renders err. Anything that is not an *Error is logged and
// reported as internal.error, so internal details never reach the client.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var ae *Error
	if !errors.As(err, &ae) {
		log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		ae = ErrInternal
	}
	var body errorBody
	body.Error.Code = ae.Code
	body.Error.Params = ae.Params
	_ = WriteJSON(w, ae.Status, body)
}

const maxJSONBody = 1 << 20

// DecodeJSON strictly decodes the request body into v: at most 1 MiB, no
// unknown fields, exactly one JSON value.
func DecodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return ErrInvalidJSON
	}
	if dec.More() {
		return ErrInvalidJSON
	}
	return nil
}

// ClientIP returns the peer address. Forwarded headers are not trusted: the
// panel is reached directly, and a reverse-proxy mode would be explicit.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type ctxKey int

const principalKey ctxKey = iota

// Principal is the authenticated user of a request.
type Principal struct {
	UserID        int64
	Username      string
	Locale        string
	Role          string
	TOTPEnabled   bool
	SessionID     string
	Token         string
	ElevatedUntil int64 // Unix seconds; step-up valid while in the future
	// Scopes is non-nil for API-token requests: the token's permissions,
	// already intersected with the owner's role.
	Scopes []string
	// Source is "web" for browser sessions and "api" for tokens (auditing).
	Source string
}

// Can reports whether p may use permission perm, honoring token scopes.
func (p *Principal) Can(perm authz.Permission) bool {
	if !authz.Allows(p.Role, perm) {
		return false
	}
	if p.Scopes == nil || perm == authz.Self {
		return true
	}
	for _, s := range p.Scopes {
		if s == string(perm) {
			return true
		}
	}
	return false
}

// IsToken reports whether p authenticated with an API token.
func (p *Principal) IsToken() bool { return p.Scopes != nil }

// PrincipalFrom returns the request's principal, if authenticated.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey).(*Principal)
	return p, ok
}

// WithPrincipal attaches p to ctx (used by the authenticator and tests).
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// Authenticator resolves a request to a principal, or returns an *Error.
type Authenticator func(r *http.Request) (*Principal, error)

// Router registers routes explicitly and wraps them with the standard
// middleware chain. It also keeps the registry the OpenAPI document is
// generated from.
type Router struct {
	*routerState
	// module and gate are set on the views ForModule returns.
	module string
	gate   func() error
}

type routerState struct {
	mux    *http.ServeMux
	auth   Authenticator
	log    *slog.Logger
	routes []Route
}

// NewRouter returns a router using auth for non-public routes.
func NewRouter(auth Authenticator, log *slog.Logger) *Router {
	return &Router{routerState: &routerState{mux: http.NewServeMux(), auth: auth, log: log}}
}

// ForModule returns a view of the router for one module: routes registered
// through it are tagged with the module ID, and gate (when not nil) is
// checked after authorization on every request; its error is returned
// instead of running the handler (a module that is turned off).
func (rt *Router) ForModule(id string, gate func() error) *Router {
	return &Router{routerState: rt.routerState, module: id, gate: gate}
}

// Route describes a registered endpoint.
type Route struct {
	Method     string
	Path       string
	Public     bool
	Permission authz.Permission
	StepUp     bool
	// SessionOnly routes refuse API tokens.
	SessionOnly bool
	Tag         string
	Summary     string
	Module      string               // registering module, if any
	Request     reflect.Type         // nil: no JSON body
	NoBody      bool                 // explicitly documented as taking no body
	RawBody     string               // content type of a non-JSON body (uploads)
	Responses   map[int]reflect.Type // nil type: no body
	// Files are responses that are not JSON (downloads), by content type.
	Files map[int]string
}

// Routes returns the registered endpoints in registration order.
func (rt *Router) Routes() []Route { return rt.routes }

// RouteOption customizes a route.
type RouteOption func(*routeConfig)

type routeConfig struct {
	public    bool
	perm      authz.Permission
	stepUp    bool
	session   bool
	tag       string
	summary   string
	request   reflect.Type
	noBody    bool
	rawBody   string
	responses map[int]reflect.Type
	files     map[int]string
}

// Doc documents a route for the OpenAPI document.
func Doc(tag, summary string) RouteOption {
	return func(c *routeConfig) { c.tag, c.summary = tag, summary }
}

// Body documents the JSON request body type (pass a zero value).
func Body(v any) RouteOption { return func(c *routeConfig) { c.request = reflect.TypeOf(v) } }

// NoBody documents that a state-changing route takes no request body.
func NoBody() RouteOption { return func(c *routeConfig) { c.noBody = true } }

// RawBody documents a request body that is not JSON (an upload) by its
// content type; the handler reads and limits it itself.
func RawBody(contentType string) RouteOption { return func(c *routeConfig) { c.rawBody = contentType } }

// ReturnsFile documents a response that is a file of the content type.
func ReturnsFile(status int, contentType string) RouteOption {
	return func(c *routeConfig) {
		if c.files == nil {
			c.files = map[int]string{}
		}
		c.files[status] = contentType
	}
}

// Returns documents a response; v is a zero value of the body type, or nil
// for an empty body.
func Returns(status int, v any) RouteOption {
	return func(c *routeConfig) {
		if c.responses == nil {
			c.responses = map[int]reflect.Type{}
		}
		var t reflect.Type
		if v != nil {
			t = reflect.TypeOf(v)
		}
		c.responses[status] = t
	}
}

// Public marks a route as reachable without a session.
func Public() RouteOption { return func(c *routeConfig) { c.public = true } }

// Require sets the permission a route needs. Every non-public route must
// declare one; registration panics otherwise (default deny).
func Require(p authz.Permission) RouteOption { return func(c *routeConfig) { c.perm = p } }

// StepUp additionally requires a recent re-authentication of the session.
func StepUp() RouteOption { return func(c *routeConfig) { c.stepUp = true } }

// SessionOnly refuses API tokens, whatever their scopes: for the routes
// that guard the account itself (password, sessions, tokens, two-step
// sign-in). Every role has authz.Self, so a token of any scope would
// otherwise pass, and a leaked read-only token could take the account.
func SessionOnly() RouteOption { return func(c *routeConfig) { c.session = true } }

// Authorization errors.
var (
	ErrForbidden      = NewError(http.StatusForbidden, "auth.forbidden", nil)
	ErrStepUpRequired = NewError(http.StatusForbidden, "auth.step_up_required", nil)
)

// Handle registers pattern ("METHOD /api/v1/path") with h. Routes require a
// session and a permission unless Public is given. State-changing methods
// always pass the CSRF check.
func (rt *Router) Handle(pattern string, h HandlerFunc, opts ...RouteOption) {
	var cfg routeConfig
	for _, o := range opts {
		o(&cfg)
	}
	if !cfg.public && cfg.perm == "" {
		panic("api: route " + pattern + " declares no permission")
	}
	if !cfg.public && !authz.Defined(cfg.perm) {
		panic("api: route " + pattern + " needs undefined permission " + string(cfg.perm))
	}
	method, path, _ := strings.Cut(pattern, " ")
	rt.routes = append(rt.routes, Route{
		Method: method, Path: path, Public: cfg.public, Permission: cfg.perm, StepUp: cfg.stepUp, SessionOnly: cfg.session,
		Tag: cfg.tag, Summary: cfg.summary, Module: rt.module, Request: cfg.request, NoBody: cfg.noBody, RawBody: cfg.rawBody, Responses: cfg.responses, Files: cfg.files,
	})
	gate := rt.gate
	rt.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if isStateChanging(method) {
			if err := checkCSRF(r); err != nil {
				writeError(w, r, rt.log, err)
				return
			}
		}
		if !cfg.public {
			p, err := rt.auth(r)
			if err != nil {
				writeError(w, r, rt.log, err)
				return
			}
			if !p.Can(cfg.perm) || cfg.session && p.IsToken() {
				writeError(w, r, rt.log, ErrForbidden)
				return
			}
			if cfg.stepUp && p.ElevatedUntil <= time.Now().Unix() {
				writeError(w, r, rt.log, ErrStepUpRequired)
				return
			}
			r = r.WithContext(WithPrincipal(r.Context(), p))
		}
		if gate != nil {
			if err := gate(); err != nil {
				writeError(w, r, rt.log, err)
				return
			}
		}
		if err := h(w, r); err != nil {
			writeError(w, r, rt.log, err)
		}
	})
}

// ServeHTTP implements http.Handler; unknown API paths get a JSON 404.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, pattern := rt.mux.Handler(r); pattern == "" {
		writeError(w, r, rt.log, ErrNotFound)
		return
	}
	rt.mux.ServeHTTP(w, r)
}

func isStateChanging(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// IsBearer reports whether the request authenticates with an API token.
func IsBearer(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// BearerToken returns the token of an Authorization: Bearer header.
func BearerToken(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

// CSRFHeader must be present on every state-changing request. Browsers cannot
// add custom headers to cross-site requests without a CORS preflight, which
// the panel never grants; SameSite=Strict cookies are a second layer.
const CSRFHeader = "X-Cairn-Request"

func checkCSRF(r *http.Request) error {
	// Bearer tokens are not sent automatically by browsers, so token requests
	// cannot be forged cross-site; the authenticator ignores cookies on them.
	if IsBearer(r) {
		return nil
	}
	if r.Header.Get(CSRFHeader) != "1" {
		return ErrCSRF
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "https://"+r.Host {
		return ErrCSRF
	}
	return nil
}

// SecurityHeaders sets headers for every response (API and UI).
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
				"connect-src 'self'; font-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// Recover turns panics into a logged internal.error response.
func Recover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Error("panic serving request", "path", r.URL.Path, "panic", v)
				writeError(w, r, log, ErrInternal)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
