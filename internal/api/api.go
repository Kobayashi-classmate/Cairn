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
	"strings"
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
	UserID    int64
	Username  string
	Locale    string
	SessionID string
	Token     string
}

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
// middleware chain.
type Router struct {
	mux  *http.ServeMux
	auth Authenticator
	log  *slog.Logger
}

// NewRouter returns a router using auth for non-public routes.
func NewRouter(auth Authenticator, log *slog.Logger) *Router {
	return &Router{mux: http.NewServeMux(), auth: auth, log: log}
}

// RouteOption customizes a route.
type RouteOption func(*routeConfig)

type routeConfig struct{ public bool }

// Public marks a route as reachable without a session.
func Public() RouteOption { return func(c *routeConfig) { c.public = true } }

// Handle registers pattern ("METHOD /api/v1/path") with h. Routes require a
// session unless Public is given. State-changing methods always pass the CSRF
// check.
func (rt *Router) Handle(pattern string, h HandlerFunc, opts ...RouteOption) {
	var cfg routeConfig
	for _, o := range opts {
		o(&cfg)
	}
	method, _, _ := strings.Cut(pattern, " ")
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
			r = r.WithContext(WithPrincipal(r.Context(), p))
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

// CSRFHeader must be present on every state-changing request. Browsers cannot
// add custom headers to cross-site requests without a CORS preflight, which
// the panel never grants; SameSite=Strict cookies are a second layer.
const CSRFHeader = "X-Cairn-Request"

func checkCSRF(r *http.Request) error {
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
