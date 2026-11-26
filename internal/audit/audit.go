// SPDX-License-Identifier: AGPL-3.0-only

// Package audit records who did what. Entries are structured (an action code
// plus parameters) so they can be rendered in the viewer's language later.
// Callers must never put secrets into Params; Record still masks the values
// of parameters whose names say they are secrets, in case one does.
package audit

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// redacted stands for the value of a parameter whose name says it is a
// secret.
const redacted = "[redacted]"

// secretNames end the names of parameters that hold secrets, compared in
// lower case, without "_" and "-" and a plural s ("newPassword",
// "api_token", "mfaToken", "secrets").
var secretNames = []string{"password", "passwd", "passphrase", "secret", "token", "privatekey", "apikey", "secretkey",
	"sessionkey", "devicekey", "credential"}

func secretName(k string) bool {
	k = strings.TrimSuffix(strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(k)), "s")
	for _, s := range secretNames {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

// encodeParams encodes parameters with the values of secret names masked,
// at any depth. A flag of that name stays ("password": true records that
// one was set).
func encodeParams(params map[string]any) ([]byte, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	// Decoded again, so that structs and typed maps are seen by the names
	// they are stored under; numbers keep their digits.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if !mask(v) {
		return data, nil
	}
	return json.Marshal(v)
}

// mask replaces the values of secret names in v and reports whether it
// replaced any.
func mask(v any) bool {
	masked := false
	switch v := v.(type) {
	case map[string]any:
		for k, e := range v {
			if secretName(k) && holdsValue(e) {
				v[k] = redacted
				masked = true
				continue
			}
			if mask(e) {
				masked = true
			}
		}
	case []any:
		for _, e := range v {
			if mask(e) {
				masked = true
			}
		}
	}
	return masked
}

// holdsValue reports whether a parameter's value could be a secret: not a
// flag, a number or nothing.
func holdsValue(v any) bool {
	switch v := v.(type) {
	case nil, bool, json.Number:
		return false
	case string:
		return v != ""
	}
	return true
}

// Entry is one audit record.
type Entry struct {
	ID       int64          `json:"id"`
	TS       int64          `json:"ts"`
	UserID   int64          `json:"userId"`
	Username string         `json:"username"`
	Action   string         `json:"action"`
	Params   map[string]any `json:"params"`
	Result   string         `json:"result"` // "ok" or an error code
	Remote   string         `json:"remote"`
	Source   string         `json:"source"` // web | cli | helper
}

// Record stores e. Failing to audit is reported to the caller, which decides
// whether the surrounding operation may proceed.
func Record(ctx context.Context, db *sql.DB, e Entry) error {
	params := []byte("{}")
	if len(e.Params) > 0 {
		var err error
		if params, err = encodeParams(e.Params); err != nil {
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

// Filter narrows a listing. Zero values mean "no filter".
type Filter struct {
	Before       int64  // only entries with id < Before
	Username     string // exact match
	ActionPrefix string // e.g. "auth." or "users.create"
	// Result is "ok" (succeeded) or "failed" (any error code).
	Result string
	Since  int64 // only entries at or after this Unix time
	Until  int64 // only entries before this Unix time
	Limit  int   // 1..200, default 50
}

// List returns entries newest first, plus the cursor for the next page
// (0 when there are no more entries).
func List(ctx context.Context, db *sql.DB, f Filter) ([]Entry, int64, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var where []string
	var args []any
	if f.Before > 0 {
		where = append(where, "id < ?")
		args = append(args, f.Before)
	}
	if f.Username != "" {
		where = append(where, "username = ?")
		args = append(args, f.Username)
	}
	if f.ActionPrefix != "" {
		where = append(where, `action LIKE ? ESCAPE '\'`)
		args = append(args, escapeLike(f.ActionPrefix)+"%")
	}
	switch f.Result {
	case "ok":
		where = append(where, "result = 'ok'")
	case "failed":
		where = append(where, "result <> 'ok'")
	}
	if f.Since > 0 {
		where = append(where, "ts >= ?")
		args = append(args, f.Since)
	}
	if f.Until > 0 {
		where = append(where, "ts < ?")
		args = append(args, f.Until)
	}
	query := `SELECT id, ts, COALESCE(user_id, 0), username, action, params, result, remote_addr, source FROM audit_log`
	if len(where) > 0 {
		// Only the fixed clause literals above are joined; every value is a
		// bound parameter.
		query += " WHERE " + strings.Join(where, " AND ") //nolint:gosec // G202: see above.
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit+1)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var params string
		if err := rows.Scan(&e.ID, &e.TS, &e.UserID, &e.Username, &e.Action, &params, &e.Result, &e.Remote, &e.Source); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal([]byte(params), &e.Params); err != nil || e.Params == nil {
			e.Params = map[string]any{}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var next int64
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// Get returns one entry, or ok false when there is none with that id.
func Get(ctx context.Context, db *sql.DB, id int64) (Entry, bool, error) {
	entries, _, err := List(ctx, db, Filter{Before: id + 1, Limit: 1})
	if err != nil || len(entries) == 0 || entries[0].ID != id {
		return Entry{}, false, err
	}
	return entries[0], true, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
