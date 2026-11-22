// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"strings"
	"time"
)

// Login throttling. Each key counts consecutive failures; after a number
// of free ones the key waits, starting at a base and doubling per further
// failure up to a maximum. Counters live in the database, so restarting the
// panel does not reset them. The keys:
//
//   - IPKey: every credential check from an address (sign-in, API tokens):
//     a lock of a minute, up to an hour. IPv6 addresses count per /64, the
//     block one host usually gets, so new addresses from it do not reset
//     anything.
//   - UserKey: sign-in attempts for a username, from any address: a lock of
//     a minute, up to 15 minutes, against guessing spread over many
//     addresses. Anyone who knows the name can cause it, so it does not
//     apply on a known device (KnownDevice): a browser the account signed
//     in from before keeps getting in.
//   - SessionKey: re-entering the password in a signed-in session (step-up,
//     password change): a lock like an address's, of that session only.
const (
	freeFailures = 5
	baseLock     = time.Minute
	maxLock      = time.Hour
	userBackoff  = time.Minute
	maxBackoff   = 15 * time.Minute
)

// IPKey, UserKey and SessionKey build throttle keys.
func UserKey(name string) string  { return "user:" + name }
func SessionKey(id string) string { return "session:" + id }

// IPKey is the key of a client address; IPv6 addresses count per /64.
func IPKey(addr string) string {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return "ip:" + addr
	}
	ip = ip.Unmap().WithZone("")
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return "ip:" + p.String()
		}
	}
	return "ip:" + ip.String()
}

// lockAfter is how long key waits after its failures-th failure in a row.
func lockAfter(key string, failures int) time.Duration {
	if failures < freeFailures {
		return 0
	}
	base, most := baseLock, maxLock
	if strings.HasPrefix(key, "user:") {
		base, most = userBackoff, maxBackoff
	}
	return min(base<<min(failures-freeFailures, 12), most)
}

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

// RecordFailure counts a failure for each key and applies locks.
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
		if lock := lockAfter(k, failures); lock > 0 {
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

// Attempt takes an attempt slot on every key before credentials are
// checked: unless a key is locked (then nothing is counted and the longest
// wait is returned), the attempt counts as a failure right away, and a key
// reaching its limit locks now. Checking and counting in one transaction
// keeps concurrent attempts from all passing a check before any of them is
// counted. A success clears the counts (ResetFailures); a failure needs
// nothing more.
func Attempt(ctx context.Context, db *sql.DB, keys ...string) (time.Duration, error) {
	tx, err := db.BeginTx(ctx, nil) // BEGIN IMMEDIATE (see store.Open): attempts are serialized
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if wait, err := LockedFor(ctx, tx, keys...); err != nil || wait > 0 {
		return wait, err
	}
	if err := RecordFailure(ctx, tx, keys...); err != nil {
		return 0, err
	}
	return 0, tx.Commit()
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

// purgeFailures forgets counters that have been quiet for a day (keys of
// sessions long gone, among others).
func purgeFailures(ctx context.Context, q queryer, now time.Time) error {
	_, err := q.ExecContext(ctx, `DELETE FROM login_failures WHERE locked_until < ? AND updated_at < ?`,
		now.Unix(), now.Add(-24*time.Hour).Unix())
	return err
}
