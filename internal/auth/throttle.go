// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Login throttling: after freeFailures consecutive failures a key is locked,
// starting at baseLock and doubling per further failure up to maxLock. Keys
// are tracked per client address and per username; counters live in the
// database, so restarting the panel does not reset them.
const (
	freeFailures = 5
	baseLock     = time.Minute
	maxLock      = time.Hour
)

// IPKey and UserKey build throttle keys.
func IPKey(addr string) string   { return "ip:" + addr }
func UserKey(name string) string { return "user:" + name }

// LockedFor returns how long the longest lock among keys still lasts.
func LockedFor(ctx context.Context, q queryer, keys ...string) (time.Duration, error) {
	var longest time.Duration
	now := time.Now()
	for _, k := range keys {
		var until int64
		err := q.QueryRowContext(ctx, `SELECT locked_until FROM login_failures WHERE key = ?`, k).Scan(&until)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if d := time.Unix(until, 0).Sub(now); d > longest {
			longest = d
		}
	}
	return longest, nil
}

// RecordFailure counts a failed login for each key and applies locks.
func RecordFailure(ctx context.Context, q queryer, keys ...string) error {
	now := time.Now()
	for _, k := range keys {
		var failures int
		err := q.QueryRowContext(ctx, `SELECT failures FROM login_failures WHERE key = ?`, k).Scan(&failures)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		failures++
		var until int64
		if failures >= freeFailures {
			lock := baseLock << min(failures-freeFailures, 10)
			lock = min(lock, maxLock)
			until = now.Add(lock).Unix()
		}
		if _, err := q.ExecContext(ctx,
			`INSERT INTO login_failures(key, failures, locked_until, updated_at) VALUES(?, ?, ?, ?)
			 ON CONFLICT(key) DO UPDATE SET failures = excluded.failures,
			   locked_until = excluded.locked_until, updated_at = excluded.updated_at`,
			k, failures, until, now.Unix()); err != nil {
			return err
		}
	}
	return nil
}

// ResetFailures clears counters after a successful login or a password reset.
func ResetFailures(ctx context.Context, q queryer, keys ...string) error {
	for _, k := range keys {
		if _, err := q.ExecContext(ctx, `DELETE FROM login_failures WHERE key = ?`, k); err != nil {
			return err
		}
	}
	return nil
}
