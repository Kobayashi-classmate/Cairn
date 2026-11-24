// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Known devices. A browser that signed in as an account keeps a random
// token in a long-lived cookie and a key in the page's own storage (only
// their hashes are stored). Signing in as that account again from it is
// exempt from the username's throttle key, though not from its address's:
// failures with the username elsewhere cannot lock the owner out of a
// browser they already used. The two prove nothing else; the password (and
// second factor) are still needed.
//
// The cookie alone does not make a device known: like the session cookie it
// is sent to every port of the panel's host name, and whoever reads it
// there could otherwise guess the account's password past the username's
// throttle. The key is sent in a header by the panel's page only.
const (
	// DeviceCookieName uses the __Host- prefix, like the session cookie.
	DeviceCookieName = "__Host-cairn_device"
	DeviceLifetime   = 180 * 24 * time.Hour
)

// RememberDevice records a browser for an account and returns the token
// for its cookie and the key for the page's storage.
func RememberDevice(ctx context.Context, q queryer, userID int64) (token, key string, err error) {
	if token, err = RandomToken(tokenBytes); err != nil {
		return "", "", err
	}
	if key, err = RandomToken(tokenBytes); err != nil {
		return "", "", err
	}
	now := time.Now()
	_, err = q.ExecContext(ctx, `INSERT INTO known_devices(token_hash, key_hash, user_id, created_at, expires_at) VALUES(?, ?, ?, ?, ?)`,
		hashToken(token), hashToken(key), userID, now.Unix(), now.Add(DeviceLifetime).Unix())
	return token, key, err
}

// KnownDevice reports whether token and key are those of a live device of
// the account named username.
func KnownDevice(ctx context.Context, q queryer, token, key, username string) (bool, error) {
	if token == "" || len(token) > 128 || key == "" || len(key) > 128 {
		return false, nil
	}
	var keyHash string
	err := q.QueryRowContext(ctx, `SELECT d.key_hash FROM known_devices d JOIN users u ON u.id = d.user_id
		WHERE d.token_hash = ? AND u.username = ? AND d.expires_at > ?`, hashToken(token), username, time.Now().Unix()).Scan(&keyHash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sameHash(keyHash, hashToken(key)), nil
}
