// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"time"
)

// Known devices. A browser that signed in as an account keeps a random
// token in a long-lived cookie (only its hash is stored). Signing in as
// that account again from it is exempt from the username's throttle key,
// though not from its address's: failures with the username elsewhere
// cannot lock the owner out of a browser they already used. The token
// proves nothing else; the password (and second factor) are still needed.
const (
	// DeviceCookieName uses the __Host- prefix, like the session cookie.
	DeviceCookieName = "__Host-cairn_device"
	DeviceLifetime   = 180 * 24 * time.Hour
)

// RememberDevice records a browser for an account and returns the token
// for its cookie.
func RememberDevice(ctx context.Context, q queryer, userID int64) (string, error) {
	token, err := RandomToken(tokenBytes)
	if err != nil {
		return "", err
	}
	now := time.Now()
	_, err = q.ExecContext(ctx, `INSERT INTO known_devices(token_hash, user_id, created_at, expires_at) VALUES(?, ?, ?, ?)`,
		hashToken(token), userID, now.Unix(), now.Add(DeviceLifetime).Unix())
	return token, err
}

// KnownDevice reports whether token is a live device token of the account
// named username.
func KnownDevice(ctx context.Context, q queryer, token, username string) (bool, error) {
	if token == "" || len(token) > 128 {
		return false, nil
	}
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM known_devices d JOIN users u ON u.id = d.user_id
		WHERE d.token_hash = ? AND u.username = ? AND d.expires_at > ?`, hashToken(token), username, time.Now().Unix()).Scan(&n)
	return n > 0, err
}
