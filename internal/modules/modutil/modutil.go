// SPDX-License-Identifier: AGPL-3.0-only

// Package modutil holds helpers shared by feature modules: mapping domain
// errors onto API error codes and writing audit entries.
package modutil

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/audit"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
)

// MapError converts known domain errors into API errors; anything else is
// returned unchanged (and reported to the client as internal.error).
// rename maps validation field names onto the names the API exposes.
func MapError(err error, rename map[string]string) error {
	var fe *auth.FieldError
	switch {
	case errors.As(err, &fe):
		field := fe.Field
		if r, ok := rename[field]; ok {
			field = r
		}
		params := map[string]any{"field": field, "rule": fe.Rule}
		if fe.Min > 0 {
			params["min"] = fe.Min
		}
		if fe.Max > 0 {
			params["max"] = fe.Max
		}
		return api.NewError(http.StatusBadRequest, "validation.invalid", params)
	case errors.Is(err, auth.ErrNotFound):
		return api.ErrNotFound
	case errors.Is(err, auth.ErrWrongPassword):
		return api.NewError(http.StatusBadRequest, "auth.wrong_password", nil)
	case errors.Is(err, auth.ErrMFARequired):
		return api.NewError(http.StatusBadRequest, "auth.mfa_required", nil)
	case errors.Is(err, auth.ErrMFAInvalidCode):
		return api.NewError(http.StatusBadRequest, "auth.mfa_invalid_code", nil)
	case errors.Is(err, auth.ErrMFAChallengeExpired):
		return api.NewError(http.StatusBadRequest, "auth.mfa_challenge_expired", nil)
	case errors.Is(err, auth.ErrTOTPNotPending):
		return api.NewError(http.StatusBadRequest, "auth.totp_not_pending", nil)
	case errors.Is(err, auth.ErrTOTPAlreadyEnabled):
		return api.NewError(http.StatusConflict, "auth.totp_already_enabled", nil)
	case errors.Is(err, auth.ErrUsernameTaken):
		return api.NewError(http.StatusConflict, "users.username_taken", nil)
	case errors.Is(err, auth.ErrTokenInvalid):
		return api.NewError(http.StatusUnauthorized, "auth.token_invalid", nil)
	case errors.Is(err, auth.ErrBusy):
		return api.NewError(http.StatusServiceUnavailable, "auth.busy", nil)
	case errors.Is(err, auth.ErrTooManyTickets):
		return RateLimited(auth.TicketTTL)
	case errors.Is(err, auth.ErrSessionInvalid):
		return api.NewError(http.StatusUnauthorized, "auth.session_expired", nil)
	}
	return err
}

// ErrorCode returns the API error code for err, for audit results.
func ErrorCode(err error) string {
	var ae *api.Error
	if errors.As(MapError(err, nil), &ae) {
		return ae.Code
	}
	return api.ErrInternal.Code
}

// RateLimited builds the auth.rate_limited error.
func RateLimited(wait time.Duration) error {
	secs := int(wait.Round(time.Second) / time.Second)
	return api.NewError(http.StatusTooManyRequests, "auth.rate_limited", map[string]any{"retryAfterSeconds": max(secs, 1)})
}

// DownloadLink is the address a page downloads the resource of this GET
// from, for the routes that answer with it (?link=1) before the browser
// follows it. A link cannot carry the session key, so a browser session
// gets the path with a one-time ticket for it. An API token is sent with
// every request and gets the plain path.
func DownloadLink(r *http.Request, db *sql.DB) (string, error) {
	link := r.URL.EscapedPath()
	p, ok := api.PrincipalFrom(r.Context())
	if !ok || p.SessionID == "" {
		return link, nil
	}
	ticket, err := auth.IssueTicket(r.Context(), db, p.SessionID, auth.TicketTarget(r.URL.Path, nil, api.TicketParam))
	if err != nil {
		return "", MapError(err, nil)
	}
	return link + "?" + url.Values{api.TicketParam: {ticket}}.Encode(), nil
}

// Actor identifies who performed an audited action.
type Actor struct {
	UserID   int64
	Username string
	Source   string // web | api; empty means web
}

// ActorOf returns the request's principal as an Actor.
func ActorOf(r *http.Request) Actor {
	if p, ok := api.PrincipalFrom(r.Context()); ok {
		return Actor{UserID: p.UserID, Username: p.Username, Source: p.Source}
	}
	return Actor{}
}

// auditTimeout bounds an audit write. It is longer than the database waits
// for its lock (store.Open), so that a busy database delays the entry
// rather than losing it.
const auditTimeout = 15 * time.Second

// Audit records an action. Failures are logged, not returned: the action
// already happened and must not be reported as failed to the client.
//
// The entry is written whatever becomes of the request: a client that
// goes away, or half-closes its connection to end the request's context,
// must not take the record of what it did (or tried) with it.
func Audit(r *http.Request, db *sql.DB, log *slog.Logger, who Actor, action, result string, params map[string]any) {
	src := who.Source
	if src == "" {
		src = "web"
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditTimeout)
	defer cancel()
	err := audit.Record(ctx, db, audit.Entry{
		UserID: who.UserID, Username: who.Username, Action: action, Params: params,
		Result: result, Remote: api.ClientIP(r), Source: src,
	})
	if err != nil {
		log.Error("audit write failed", "action", action, "user", who.Username, "remote", api.ClientIP(r), "err", err)
	}
}
