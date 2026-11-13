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
	// ElevationWindow is how long a re-authentication (step-up) lasts.
	ElevationWindow = 10 * time.Minute
	tokenBytes      = 32
	touchInterval   = time.Minute // throttle last_seen_at writes
)

// ErrSessionInvalid covers unknown, expired and idle-timed-out sessions.
var ErrSessionInvalid = errors.New("auth: session invalid")

// Session is a signed-in browser.
type Session struct {
	ID            string `json:"id"` // hash of the token; the token itself is never stored
	UserID        int64  `json:"-"`
	CreatedAt     int64  `json:"createdAt"`
	LastSeenAt    int64  `json:"lastSeenAt"`
	ExpiresAt     int64  `json:"-"`
	ElevatedUntil int64  `json:"-"`
	Remote        string `json:"remote"`
	UserAgent     string `json:"userAgent"`
	Current       bool   `json:"current"`
}

// Elevated reports whether a step-up re-authentication is still valid.
func (s *Session) Elevated(now time.Time) bool { return s.ElevatedUntil > now.Unix() }

// SessionID returns the stored identifier of a cookie token.
func SessionID(token string) string { return hashToken(token) }

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

const sessionColumns = `id, user_id, created_at, last_seen_at, expires_at, elevated_until, remote_addr, user_agent`

func scanSession(row scanner) (*Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.UserID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.ElevatedUntil, &s.Remote, &s.UserAgent)
	return &s, err
}

// LookupSession validates a cookie token and returns the session and user.
// Idle and absolute timeouts are enforced here; expired rows are deleted.
func LookupSession(ctx context.Context, q queryer, token string) (*Session, *User, error) {
	if token == "" || len(token) > 128 {
		return nil, nil, ErrSessionInvalid
	}
	id := hashToken(token)
	s, err := scanSession(q.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrSessionInvalid
	}
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	if now.Unix() > s.ExpiresAt || now.Sub(time.Unix(s.LastSeenAt, 0)) > IdleTimeout {
		_, _ = q.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
		return nil, nil, ErrSessionInvalid
	}
	u, err := UserByID(ctx, q, s.UserID)
	if err != nil || u.Disabled {
		return nil, nil, ErrSessionInvalid
	}
	if now.Sub(time.Unix(s.LastSeenAt, 0)) > touchInterval {
		if _, err := q.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`, now.Unix(), id); err == nil {
			s.LastSeenAt = now.Unix()
		}
	}
	return s, u, nil
}

// Elevate opens the step-up window of a session and returns its end.
func Elevate(ctx context.Context, q queryer, sessionID string) (int64, error) {
	until := time.Now().Add(ElevationWindow).Unix()
	_, err := q.ExecContext(ctx, `UPDATE sessions SET elevated_until = ? WHERE id = ?`, until, sessionID)
	return until, err
}

// ListSessions returns a user's live sessions, newest activity first, marking
// the one identified by currentID.
func ListSessions(ctx context.Context, q queryer, userID int64, currentID string) ([]*Session, error) {
	now := time.Now()
	rows, err := q.QueryContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE user_id = ? AND expires_at >= ? AND last_seen_at >= ?
		 ORDER BY last_seen_at DESC`, userID, now.Unix(), now.Add(-IdleTimeout).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		s.Current = s.ID == currentID
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteSession signs a browser out by cookie token.
func DeleteSession(ctx context.Context, q queryer, token string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, hashToken(token))
	return err
}

// DeleteSessionByID revokes one of the user's own sessions. It reports
// whether a session was removed; other users' sessions are never touched.
func DeleteSessionByID(ctx context.Context, q queryer, userID int64, id string) (bool, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteOtherSessions signs a user out everywhere except keepID.
func DeleteOtherSessions(ctx context.Context, q queryer, userID int64, keepID string) (int64, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id != ?`, userID, keepID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteUserSessions signs a user out everywhere.
func DeleteUserSessions(ctx context.Context, q queryer, userID int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// PurgeExpired removes stale sessions and MFA challenges.
func PurgeExpired(ctx context.Context, q queryer) error {
	now := time.Now()
	if _, err := q.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ? OR last_seen_at < ?`,
		now.Unix(), now.Add(-IdleTimeout).Unix()); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `DELETE FROM mfa_challenges WHERE expires_at < ?`, now.Unix())
	return err
}
