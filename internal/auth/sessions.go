// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// Session cookie and lifetime settings.
const (
	// CookieName uses the __Host- prefix: Secure, Path=/ and no Domain are
	// enforced by the browser, so no subdomain can set or read it.
	CookieName      = "__Host-cairn_session"
	IdleTimeout     = 2 * time.Hour
	AbsoluteTimeout = 12 * time.Hour
	tokenBytes      = 32
	touchInterval   = time.Minute // throttle last_seen_at writes
)

// ErrSessionInvalid covers unknown, expired and idle-timed-out sessions.
var ErrSessionInvalid = errors.New("auth: session invalid")

// Session is a signed-in browser.
type Session struct {
	ID         string // hash of the token; the token itself is never stored
	UserID     int64
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession starts a session and returns the cookie token.
func CreateSession(ctx context.Context, q queryer, userID int64, remote, userAgent string) (string, error) {
	token, err := RandomToken(tokenBytes)
	if err != nil {
		return "", err
	}
	now := time.Now()
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO sessions(id, user_id, created_at, last_seen_at, expires_at, remote_addr, user_agent)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		hashToken(token), userID, now.Unix(), now.Unix(), now.Add(AbsoluteTimeout).Unix(), remote, userAgent)
	if err != nil {
		return "", err
	}
	return token, nil
}

// LookupSession validates a cookie token and returns the session and user.
// Idle and absolute timeouts are enforced here; expired rows are deleted.
func LookupSession(ctx context.Context, q queryer, token string) (*Session, *User, error) {
	if token == "" || len(token) > 128 {
		return nil, nil, ErrSessionInvalid
	}
	id := hashToken(token)
	var s Session
	var created, seen, expires int64
	err := q.QueryRowContext(ctx,
		`SELECT user_id, created_at, last_seen_at, expires_at FROM sessions WHERE id = ?`, id).
		Scan(&s.UserID, &created, &seen, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrSessionInvalid
	}
	if err != nil {
		return nil, nil, err
	}
	s.ID, s.CreatedAt, s.LastSeenAt, s.ExpiresAt = id, time.Unix(created, 0), time.Unix(seen, 0), time.Unix(expires, 0)

	now := time.Now()
	if now.After(s.ExpiresAt) || now.Sub(s.LastSeenAt) > IdleTimeout {
		_, _ = q.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
		return nil, nil, ErrSessionInvalid
	}
	u, err := UserByID(ctx, q, s.UserID)
	if err != nil || u.Disabled {
		return nil, nil, ErrSessionInvalid
	}
	if now.Sub(s.LastSeenAt) > touchInterval {
		if _, err := q.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`, now.Unix(), id); err == nil {
			s.LastSeenAt = now
		}
	}
	return &s, u, nil
}

// DeleteSession signs a browser out.
func DeleteSession(ctx context.Context, q queryer, token string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, hashToken(token))
	return err
}

// DeleteUserSessions signs a user out everywhere.
func DeleteUserSessions(ctx context.Context, q queryer, userID int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// PurgeExpired removes sessions past their absolute or idle limit.
func PurgeExpired(ctx context.Context, q queryer) error {
	now := time.Now()
	_, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ? OR last_seen_at < ?`,
		now.Unix(), now.Add(-IdleTimeout).Unix())
	return err
}
