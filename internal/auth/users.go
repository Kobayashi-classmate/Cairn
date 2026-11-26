// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/authz"
)

var (
	// ErrNotFound is returned when a user does not exist.
	ErrNotFound = errors.New("auth: not found")
	// ErrUsernameTaken is returned when creating a duplicate username.
	ErrUsernameTaken = errors.New("auth: username taken")
	// ErrInvalidCredentials hides whether the username or the password was wrong.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	// ErrWrongPassword is returned when re-entering one's own password fails.
	ErrWrongPassword = errors.New("auth: wrong password")
)

// User is a panel account.
type User struct {
	ID             int64  `json:"id"`
	Username       string `json:"username"`
	Locale         string `json:"locale"`         // empty: panel default applies
	Theme          string `json:"theme"`          // ThemeLight, ThemeDark or ThemeSystem
	TerminalScheme string `json:"terminalScheme"` // one of TerminalSchemes
	Role           string `json:"role"`
	Disabled       bool   `json:"disabled"`
	TOTPEnabled    bool   `json:"totpEnabled"`
	CreatedAt      int64  `json:"createdAt"`
	LastLoginAt    int64  `json:"lastLoginAt"`

	passwordHash string
}

// queryer is satisfied by *sql.DB and *sql.Tx.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const userColumns = `id, username, locale, theme, terminal_scheme, role, disabled, totp_enabled, created_at, last_login_at, password_hash`

// Colour schemes a user can choose.
const (
	ThemeLight  = "light"
	ThemeDark   = "dark"
	ThemeSystem = "system" // follow the browser's prefers-color-scheme
)

// TerminalSchemeCairn, the default terminal colour scheme, is the panel's
// own palette.
const TerminalSchemeCairn = "cairn"

// TerminalSchemes are the web terminal's colour schemes a user can choose,
// by id, as web/src/terminal/schemeIds.ts lists them; the interface holds
// their colours.
var TerminalSchemes = []string{TerminalSchemeCairn, "solarized", "one-half", "github", "gruvbox", "catppuccin", "tango", "xterm", "dracula", "nord"}

type scanner interface{ Scan(dest ...any) error }

func scanUser(row scanner) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.Locale, &u.Theme, &u.TerminalScheme, &u.Role, &u.Disabled, &u.TOTPEnabled, &u.CreatedAt, &u.LastLoginAt, &u.passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// ValidateRole checks a role name.
func ValidateRole(role string) error {
	if !authz.ValidRole(role) {
		return &FieldError{Field: "role", Rule: "enum"}
	}
	return nil
}

// CreateUser validates and inserts a user.
func CreateUser(ctx context.Context, q queryer, username, password, locale, role string) (*User, error) {
	username = strings.TrimSpace(username)
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	if err := ValidateRole(role); err != nil {
		return nil, err
	}
	var exists int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&exists); err != nil {
		return nil, err
	}
	if exists > 0 {
		return nil, ErrUsernameTaken
	}
	hash, err := HashPassword(ctx, password)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	res, err := q.ExecContext(ctx,
		`INSERT INTO users(username, password_hash, locale, role, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?)`,
		username, hash, locale, role, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return UserByID(ctx, q, id)
}

// UserByID loads a user.
func UserByID(ctx context.Context, q queryer, id int64) (*User, error) {
	return scanUser(q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// UserByName loads a user.
func UserByName(ctx context.Context, q queryer, username string) (*User, error) {
	return scanUser(q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, username))
}

// ListUsers returns all accounts ordered by id.
func ListUsers(ctx context.Context, q queryer) ([]*User, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers returns the number of accounts.
func CountUsers(ctx context.Context, q queryer) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CountActiveAdmins returns the number of enabled administrators.
func CountActiveAdmins(ctx context.Context, q queryer) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = ? AND disabled = 0`, string(authz.Admin)).Scan(&n)
	return n, err
}

// SetRole changes a user's role.
func SetRole(ctx context.Context, q queryer, userID int64, role string) error {
	if err := ValidateRole(role); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, role, time.Now().Unix(), userID)
	return err
}

// SetDisabled enables or disables an account; disabling signs it out.
func SetDisabled(ctx context.Context, q queryer, userID int64, disabled bool) error {
	if _, err := q.ExecContext(ctx, `UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?`, disabled, time.Now().Unix(), userID); err != nil {
		return err
	}
	if disabled {
		return DeleteUserSessions(ctx, q, userID)
	}
	return nil
}

// DeleteUser removes an account; sessions, tokens, recovery codes and
// pending challenges go with it (ON DELETE CASCADE). The file manager's
// uploads and favourites have no foreign key, and SQLite gives the highest
// free ID to the next account: they are deleted here, or that account
// would inherit them. (The upload files left behind go with the file
// manager's cleanup of uploads nothing refers to.)
func DeleteUser(ctx context.Context, q queryer, userID int64) error {
	for _, stmt := range []string{
		`DELETE FROM file_uploads WHERE user_id = ?`,
		`DELETE FROM file_favorites WHERE user_id = ?`,
		`DELETE FROM users WHERE id = ?`,
	} {
		if _, err := q.ExecContext(ctx, stmt, userID); err != nil {
			return err
		}
	}
	return nil
}

// SetLocale stores a user's preferred language ("" resets to the default).
func SetLocale(ctx context.Context, q queryer, userID int64, locale string) error {
	_, err := q.ExecContext(ctx, `UPDATE users SET locale = ?, updated_at = ? WHERE id = ?`, locale, time.Now().Unix(), userID)
	return err
}

// SetTheme stores a user's colour scheme.
func SetTheme(ctx context.Context, q queryer, userID int64, theme string) error {
	switch theme {
	case ThemeLight, ThemeDark, ThemeSystem:
	default:
		return &FieldError{Field: "theme", Rule: "enum"}
	}
	_, err := q.ExecContext(ctx, `UPDATE users SET theme = ?, updated_at = ? WHERE id = ?`, theme, time.Now().Unix(), userID)
	return err
}

// SetTerminalScheme stores a user's colour scheme for the web terminal.
func SetTerminalScheme(ctx context.Context, q queryer, userID int64, scheme string) error {
	if !slices.Contains(TerminalSchemes, scheme) {
		return &FieldError{Field: "scheme", Rule: "enum"}
	}
	_, err := q.ExecContext(ctx, `UPDATE users SET terminal_scheme = ?, updated_at = ? WHERE id = ?`, scheme, time.Now().Unix(), userID)
	return err
}

// SetPassword replaces a user's password after validating it.
func SetPassword(ctx context.Context, q queryer, userID int64, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(ctx, password)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, time.Now().Unix(), userID)
	return err
}

// CheckPassword verifies a user's own password (re-authentication).
func CheckPassword(ctx context.Context, q queryer, userID int64, password string) error {
	u, err := UserByID(ctx, q, userID)
	if err != nil {
		return err
	}
	ok, err := VerifyPassword(ctx, password, u.passwordHash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrWrongPassword
	}
	return nil
}

// TouchLogin records a successful sign-in.
func TouchLogin(ctx context.Context, q queryer, userID int64) error {
	_, err := q.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, time.Now().Unix(), userID)
	return err
}

// Authenticate checks credentials. It spends the same hashing effort whether
// or not the user exists, and returns ErrInvalidCredentials for every
// failure that must look identical to the client. A name no account can
// have (see ValidateUsername) is not looked up at all.
func Authenticate(ctx context.Context, q queryer, username, password string) (*User, error) {
	var u *User
	err := ErrNotFound
	if ValidateUsername(username) == nil {
		u, err = UserByName(ctx, q, username)
	}
	if errors.Is(err, ErrNotFound) {
		if _, err := VerifyPassword(ctx, password, dummyHash()); err != nil {
			return nil, err // the request ended while waiting for a slot
		}
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	ok, err := VerifyPassword(ctx, password, u.passwordHash)
	if err != nil {
		return nil, err
	}
	if !ok || u.Disabled {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}
