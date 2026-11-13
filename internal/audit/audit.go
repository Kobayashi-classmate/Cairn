// SPDX-License-Identifier: AGPL-3.0-only

// Package audit records who did what. Entries are structured (an action code
// plus parameters) so they can be rendered in the viewer's language later.
// Callers must never put secrets into Params.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Entry is one audit record.
type Entry struct {
	UserID   int64
	Username string
	Action   string
	Params   map[string]any
	Result   string // "ok" or an error code
	Remote   string
	Source   string // web | cli | helper
}

// Record stores e. Failing to audit is reported to the caller, which decides
// whether the surrounding operation may proceed.
func Record(ctx context.Context, db *sql.DB, e Entry) error {
	params := []byte("{}")
	if len(e.Params) > 0 {
		var err error
		if params, err = json.Marshal(e.Params); err != nil {
			return err
		}
	}
	if e.Source == "" {
		e.Source = "web"
	}
	var uid any
	if e.UserID > 0 {
		uid = e.UserID
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO audit_log(ts, user_id, username, action, params, result, remote_addr, source)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), uid, e.Username, e.Action, string(params), e.Result, e.Remote, e.Source)
	return err
}
