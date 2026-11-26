// SPDX-License-Identifier: AGPL-3.0-only

// Package helperapi defines the protocol between the unprivileged processes
// (cairn-web, the cairn CLI) and the root helper: action names, typed request
// and response bodies, the caller identity used for auditing, and a client.
//
// Transport: HTTP/1.1 over a Unix socket. Each action is "POST /v1/<action>"
// with a JSON body. Actions are registered at compile time; there is no
// dynamic dispatch by name beyond this fixed table.
package helperapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
)

// DefaultSocket is where systemd creates the helper socket.
const DefaultSocket = "/run/cairn/helper.sock"

// CallerHeader carries the base64url-encoded JSON Caller of a request.
const CallerHeader = "X-Cairn-Caller"

// Caller identifies who asked for an action, for the helper's audit log. The
// helper trusts it only as far as the peer (verified with SO_PEERCRED) is
// allowed to speak for panel users at all.
type Caller struct {
	UserID   int64  `json:"uid,omitempty"`
	Username string `json:"user,omitempty"`
	Remote   string `json:"remote,omitempty"`
	Source   string `json:"src"` // web | cli
	// PeerRoot is set by the helper from the peer's credentials, never
	// from the request: the action comes from a root process (the command
	// line, the install), not from the web process.
	PeerRoot bool `json:"-"`
}

// Encode serializes the caller for CallerHeader.
func (c Caller) Encode() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCaller parses a CallerHeader value.
func DecodeCaller(s string) (Caller, error) {
	var c Caller
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

// Error is a structured failure: a stable code plus parameters. The web UI
// translates the code; it is never a pre-rendered sentence.
type Error struct {
	Code   string         `json:"code"`
	Params map[string]any `json:"params,omitempty"`
}

func (e *Error) Error() string {
	if len(e.Params) == 0 {
		return e.Code
	}
	return fmt.Sprintf("%s %v", e.Code, e.Params)
}

// Error codes shared by both sides.
const (
	CodeUnavailable     = "helper.unavailable"
	CodeUnknownAction   = "helper.unknown_action"
	CodeInvalidRequest  = "request.invalid_json"
	CodeInternal        = "internal.error"
	CodeLocaleUnsupport = "locale.unsupported"
)

// Empty is used for actions without a request or response body.
type Empty struct{}

// ActionPing checks that the helper is reachable.
const ActionPing = "system.ping"

// PingResponse identifies the running helper.
type PingResponse struct {
	Version string `json:"version"`
	PID     int    `json:"pid"`
}

// ActionSetDefaultLocale changes ui.default_locale in the root-owned config.
const ActionSetDefaultLocale = "config.set_default_locale"

// SetDefaultLocaleRequest is the body of ActionSetDefaultLocale.
type SetDefaultLocaleRequest struct {
	Locale string `json:"locale"`
}

// Validate implements the helper's request validation hook.
func (r SetDefaultLocaleRequest) Validate() error {
	if !i18n.IsSupported(r.Locale) {
		return &Error{Code: CodeLocaleUnsupport, Params: map[string]any{"locale": r.Locale}}
	}
	return nil
}

// ActionSetCloud changes the [cloud] section of the root-owned config.
const ActionSetCloud = "config.set_cloud"

// SetCloudRequest is the body of ActionSetCloud.
type SetCloudRequest struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"baseUrl"`
	Channel string `json:"channel"`
}

// Validate implements the helper's request validation hook.
func (r SetCloudRequest) Validate() error {
	if r.Channel != "stable" && r.Channel != "beta" {
		return &Error{Code: "validation.invalid", Params: map[string]any{"field": "channel", "rule": "enum"}}
	}
	u, err := url.Parse(r.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return &Error{Code: "validation.invalid", Params: map[string]any{"field": "baseUrl", "rule": "url"}}
	}
	return nil
}

// Update actions. The web process only names a version: the helper fetches
// and verifies the signed release index itself, so a compromised web process
// cannot make it install an arbitrary package.
const (
	ActionUpdate       = "panel.update"
	ActionUpdateStatus = "panel.update_status"
)

// Update error codes.
const (
	CodeUpdateNotAvailable = "update.not_available"
	CodeUpdateInProgress   = "update.in_progress"
	CodeUpdateVerification = "update.verification_failed"
	CodeUpdateDownload     = "update.download_failed"
	// CodePkgBusy means another operation holds the package manager.
	CodePkgBusy = "pkg.busy"
)

// UpdateRequest is the body of ActionUpdate.
type UpdateRequest struct {
	Version string `json:"version"`
}

// UpdateJob describes the last update attempt.
type UpdateJob struct {
	State      string `json:"state"` // idle | running | succeeded | failed | interrupted (its run ended without finishing)
	Version    string `json:"version"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt int64  `json:"finishedAt"`
	Error      string `json:"error"`
	Log        string `json:"log"`
}
