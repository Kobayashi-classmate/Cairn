// SPDX-License-Identifier: AGPL-3.0-only

// Package modutil holds helpers shared by feature modules: mapping domain
// errors onto API error codes and writing audit entries.
package modutil

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
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

// Actor identifies who performed an audited action.
type Actor struct {
	UserID   int64
	Username string
}

// ActorOf returns the request's principal as an Actor.
func ActorOf(r *http.Request) Actor {
	if p, ok := api.PrincipalFrom(r.Context()); ok {
		return Actor{UserID: p.UserID, Username: p.Username}
	}
	return Actor{}
}

// Audit records an action. Failures are logged, not returned: the action
// already happened and must not be reported as failed to the client.
func Audit(r *http.Request, db *sql.DB, log *slog.Logger, who Actor, action, result string, params map[string]any) {
	err := audit.Record(r.Context(), db, audit.Entry{
		UserID: who.UserID, Username: who.Username, Action: action, Params: params,
		Result: result, Remote: api.ClientIP(r), Source: "web",
	})
	if err != nil {
		log.Error("audit write failed", "action", action, "err", err)
	}
}
