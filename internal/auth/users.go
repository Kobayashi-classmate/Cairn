// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrNotFound is returned when a user does not exist.
var ErrNotFound = errors.New("auth: not found")

// User is a panel account.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Locale   string `json:"locale"` // empty: panel default applies
	Disabled bool   `json:"-"`

	passwordHash string
}

// queryer is satisfied by *sql.DB and *sql.Tx.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const userColumns = `id, username, locale, disabled, password_hash`

func scanUser(row *sql.Row) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Username, &u.Locale, &u.Disabled, &u.passwordHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// CreateUser validates and inserts a user.
func CreateUser(ctx context.Context, q queryer, username, password, locale string) (*User, error) {
	username = strings.TrimSpace(username)
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	res, err := q.ExecContext(ctx,
		`INSERT INTO users(username, password_hash, locale, created_at, updated_at) VALUES(?, ?, ?, ?, ?)`,
		username, hash, locale, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &User{ID: id, Username: username, Locale: locale, passwordHash: hash}, nil
}

// UserByID loads a user.
func UserByID(ctx context.Context, q queryer, id int64) (*User, error) {
	return scanUser(q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// UserByName loads a user.
func UserByName(ctx context.Context, q queryer, username string) (*User, error) {
	return scanUser(q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, username))
}

// CountUsers returns the number of accounts.
func CountUsers(ctx context.Context, q queryer) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// SetLocale stores a user's preferred language ("" resets to the default).
func SetLocale(ctx context.Context, q queryer, userID int64, locale string) error {
	_, err := q.ExecContext(ctx, `UPDATE users SET locale = ?, updated_at = ? WHERE id = ?`, locale, time.Now().Unix(), userID)
	return err
}

// SetPassword replaces a user's password after validating it.
func SetPassword(ctx context.Context, q queryer, userID int64, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, time.Now().Unix(), userID)
	return err
}

// Authenticate checks credentials. It spends the same hashing effort whether
// or not the user exists, and returns ErrInvalidCredentials for every
// failure that must look identical to the client.
func Authenticate(ctx context.Context, q queryer, username, password string) (*User, error) {
	u, err := UserByName(ctx, q, username)
	if errors.Is(err, ErrNotFound) {
		_, _ = VerifyPassword(password, dummyHash())
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	ok, err := VerifyPassword(password, u.passwordHash)
	if err != nil {
		return nil, err
	}
	if !ok || u.Disabled {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}

// ErrInvalidCredentials hides whether the username or the password was wrong.
var ErrInvalidCredentials = errors.New("auth: invalid credentials")
