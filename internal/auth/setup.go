// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SetupTokenTTL bounds how long a setup link stays valid.
const SetupTokenTTL = time.Hour

var (
	// ErrSetupDone is returned once an administrator exists.
	ErrSetupDone = errors.New("auth: setup already completed")
	// ErrSetupToken is returned for unknown or expired setup tokens.
	ErrSetupToken = errors.New("auth: invalid setup token")
)

// CreateSetupToken issues a one-time token for creating the first
// administrator. It fails with ErrSetupDone when users already exist.
func CreateSetupToken(ctx context.Context, q queryer, ttl time.Duration) (string, error) {
	n, err := CountUsers(ctx, q)
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "", ErrSetupDone
	}
	token, err := RandomToken(tokenBytes)
	if err != nil {
		return "", err
	}
	now := time.Now()
	if _, err := q.ExecContext(ctx, `DELETE FROM setup_tokens WHERE expires_at < ?`, now.Unix()); err != nil {
		return "", err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO setup_tokens(token_hash, created_at, expires_at) VALUES(?, ?, ?)`,
		hashToken(token), now.Unix(), now.Add(ttl).Unix())
	return token, err
}

// CompleteSetup consumes a setup token and creates the first administrator,
// atomically: two concurrent attempts cannot both succeed.
func CompleteSetup(ctx context.Context, db *sql.DB, token, username, password, locale string) (*User, error) {
	tx, err := db.BeginTx(ctx, nil) // BEGIN IMMEDIATE (see store.Open)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	n, err := CountUsers(ctx, tx)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrSetupDone
	}
	var expires int64
	err = tx.QueryRowContext(ctx, `SELECT expires_at FROM setup_tokens WHERE token_hash = ?`, hashToken(token)).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && time.Now().Unix() > expires) {
		return nil, ErrSetupToken
	}
	if err != nil {
		return nil, err
	}
	u, err := CreateUser(ctx, tx, username, password, locale)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM setup_tokens`); err != nil {
		return nil, err
	}
	return u, tx.Commit()
}
