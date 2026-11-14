// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import (
	"encoding/json"
	"regexp"
)

// Operations are long-running privileged actions (installing packages,
// refreshing package indexes, ...). Starting one returns an OpRef at once;
// the helper runs it in the background, keeps its state and log on disk, and
// the caller follows it with ActionOpGet. Only actions registered as
// operations in the helper's fixed table can be started; there is no generic
// "run this" operation.
const (
	ActionOpGet    = "op.get"
	ActionOpCancel = "op.cancel"
)

// Operation states. An operation that was queued or running when the helper
// stopped is marked interrupted when it starts again.
const (
	OpQueued      = "queued"
	OpRunning     = "running"
	OpSucceeded   = "succeeded"
	OpFailed      = "failed"
	OpCanceled    = "canceled"
	OpInterrupted = "interrupted"
)

// Operation error codes.
const (
	CodeOpNotFound      = "op.not_found"
	CodeOpNotCancelable = "op.not_cancelable"
	CodeOpInterrupted   = "op.interrupted"
	CodeOpCanceled      = "op.canceled"
	CodeOpFailed        = "op.failed"
)

// OpDone reports whether state is final.
func OpDone(state string) bool {
	switch state {
	case OpSucceeded, OpFailed, OpCanceled, OpInterrupted:
		return true
	}
	return false
}

// OpRef identifies a started operation.
type OpRef struct {
	ID string `json:"id"`
}

// Op is the state of an operation.
type Op struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"` // the action that started it
	State      string `json:"state"`
	CreatedAt  int64  `json:"createdAt"`
	StartedAt  int64  `json:"startedAt,omitempty"`
	FinishedAt int64  `json:"finishedAt,omitempty"`
	// WaitingFor names the locks a queued operation is waiting for.
	WaitingFor []string `json:"waitingFor,omitempty"`
	// Cancelable tells whether op.cancel would currently be accepted.
	Cancelable bool `json:"cancelable"`
	// Error is set for failed, canceled and interrupted operations.
	Error *Error `json:"error,omitempty"`
	// Result is the operation's typed result, when it has one.
	Result json.RawMessage `json:"result,omitempty"`
	// Caller who started it (audit).
	User string `json:"user,omitempty"`
}

var opIDPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)

// ValidOpID reports whether s has the shape of an operation ID.
func ValidOpID(s string) bool { return opIDPattern.MatchString(s) }

// MaxOpLogChunk is the most log bytes one op.get returns.
const MaxOpLogChunk = 256 << 10

// OpGetRequest asks for an operation's state and its log from Offset on.
type OpGetRequest struct {
	ID     string `json:"id"`
	Offset int64  `json:"offset"`
}

// Validate implements the helper's request validation hook.
func (r OpGetRequest) Validate() error {
	if !ValidOpID(r.ID) || r.Offset < 0 {
		return &Error{Code: CodeInvalidRequest}
	}
	return nil
}

// OpGetResponse carries the state and the next log chunk. Offset is where
// the next request should continue; LogSize is the log's current length.
type OpGetResponse struct {
	Op      Op     `json:"op"`
	Log     string `json:"log"`
	Offset  int64  `json:"offset"`
	LogSize int64  `json:"logSize"`
}

// OpCancelRequest asks to cancel an operation.
type OpCancelRequest struct {
	ID string `json:"id"`
}

// Validate implements the helper's request validation hook.
func (r OpCancelRequest) Validate() error {
	if !ValidOpID(r.ID) {
		return &Error{Code: CodeInvalidRequest}
	}
	return nil
}

// ActionPkgRefresh refreshes the system package indexes (apt-get update or
// dnf makecache). It is an operation and takes the package manager lock.
const ActionPkgRefresh = "pkg.refresh"
