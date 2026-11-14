// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Kobayashi-classmate/Cairn/internal/authz"
)

// API token settings.
const (
	TokenPrefix      = "cairn_"
	tokenPrefixLen   = len(TokenPrefix) + 8 // stored and shown to recognize a token
	maxTokenIPs      = 20
	MaxTokenLifeDays = 3650
	tokenTouchEvery  = time.Minute
)

// ErrTokenInvalid covers unknown, expired, IP-restricted and orphaned tokens.
var ErrTokenInvalid = errors.New("auth: token invalid")

// Token is an API token as listed to its owner; the secret is never stored.
type Token struct {
	ID          int64    `json:"id"`
	UserID      int64    `json:"-"`
	Name        string   `json:"name"`
	Prefix      string   `json:"prefix"`
	Permissions []string `json:"permissions"`
	AllowedIPs  []string `json:"allowedIps"`
	ExpiresAt   int64    `json:"expiresAt"`
	LastUsedAt  int64    `json:"lastUsedAt"`
	CreatedAt   int64    `json:"createdAt"`
}

// TokenRequest describes a token to create.
type TokenRequest struct {
	Name          string
	Permissions   []string
	ExpiresInDays int
	AllowedIPs    []string
}

func (r *TokenRequest) validate(role string) error {
	switch n := utf8.RuneCountInString(strings.TrimSpace(r.Name)); {
	case n == 0:
		return &FieldError{Field: "name", Rule: "required"}
	case n > 64:
		return &FieldError{Field: "name", Rule: "max_length", Max: 64}
	}
	if len(r.Permissions) == 0 {
		return &FieldError{Field: "permissions", Rule: "required"}
	}
	for _, p := range r.Permissions {
		if !slices.Contains(authz.PermissionsOf(role), p) {
			return &FieldError{Field: "permissions", Rule: "subset"}
		}
	}
	if r.ExpiresInDays < 0 || r.ExpiresInDays > MaxTokenLifeDays {
		return &FieldError{Field: "expiresInDays", Rule: "range", Max: MaxTokenLifeDays}
	}
	if len(r.AllowedIPs) > maxTokenIPs {
		return &FieldError{Field: "allowedIps", Rule: "max_length", Max: maxTokenIPs}
	}
	for _, s := range r.AllowedIPs {
		if _, err := parseAllowed(s); err != nil {
			return &FieldError{Field: "allowedIps", Rule: "ip"}
		}
	}
	return nil
}

// parseAllowed accepts "203.0.113.7", "2001:db8::1" or a CIDR prefix.
func parseAllowed(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// CreateToken issues a token for u. The returned string is the only copy.
func CreateToken(ctx context.Context, q queryer, u *User, req TokenRequest) (string, *Token, error) {
	if err := req.validate(u.Role); err != nil {
		return "", nil, err
	}
	secret, err := RandomToken(tokenBytes)
	if err != nil {
		return "", nil, err
	}
	token := TokenPrefix + secret
	now := time.Now()
	var expires int64
	if req.ExpiresInDays > 0 {
		expires = now.AddDate(0, 0, req.ExpiresInDays).Unix()
	}
	ips := make([]string, 0, len(req.AllowedIPs))
	for _, s := range req.AllowedIPs {
		p, _ := parseAllowed(s)
		ips = append(ips, p.String())
	}
	perms := slices.Clone(req.Permissions)
	slices.Sort(perms)
	perms = slices.Compact(perms)
	permsJSON, _ := json.Marshal(perms)
	ipsJSON, _ := json.Marshal(ips)
	res, err := q.ExecContext(ctx,
		`INSERT INTO api_tokens(user_id, name, token_hash, prefix, permissions, allowed_ips, expires_at, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, strings.TrimSpace(req.Name), hashToken(token), token[:tokenPrefixLen], string(permsJSON), string(ipsJSON), expires, now.Unix())
	if err != nil {
		return "", nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", nil, err
	}
	return token, &Token{ID: id, UserID: u.ID, Name: strings.TrimSpace(req.Name), Prefix: token[:tokenPrefixLen],
		Permissions: perms, AllowedIPs: ips, ExpiresAt: expires, CreatedAt: now.Unix()}, nil
}

const tokenColumns = `id, user_id, name, prefix, permissions, allowed_ips, expires_at, last_used_at, created_at` //nolint:gosec // G101: SQL column list, not a credential.

func scanToken(row scanner) (*Token, error) {
	var t Token
	var perms, ips string
	if err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &perms, &ips, &t.ExpiresAt, &t.LastUsedAt, &t.CreatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(perms), &t.Permissions); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(ips), &t.AllowedIPs); err != nil {
		return nil, err
	}
	return &t, nil
}

// ListTokens returns a user's tokens, newest first.
func ListTokens(ctx context.Context, q queryer, userID int64) ([]*Token, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+tokenColumns+` FROM api_tokens WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteToken revokes one of the user's own tokens and returns its name.
func DeleteToken(ctx context.Context, q queryer, userID, id int64) (string, error) {
	var name string
	err := q.QueryRowContext(ctx, `SELECT name FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	_, err = q.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	return name, err
}

// LookupToken authenticates a bearer token presented from remote. The
// token's permissions are re-intersected with the owner's current role, so
// a demoted user's tokens lose what the role lost.
func LookupToken(ctx context.Context, q queryer, token, remote string) (*Token, *User, error) {
	if !strings.HasPrefix(token, TokenPrefix) || len(token) > 128 {
		return nil, nil, ErrTokenInvalid
	}
	t, err := scanToken(q.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM api_tokens WHERE token_hash = ?`, hashToken(token)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	if t.ExpiresAt > 0 && now.Unix() > t.ExpiresAt {
		return nil, nil, ErrTokenInvalid
	}
	if len(t.AllowedIPs) > 0 && !ipAllowed(t.AllowedIPs, remote) {
		return nil, nil, ErrTokenInvalid
	}
	u, err := UserByID(ctx, q, t.UserID)
	if err != nil || u.Disabled {
		return nil, nil, ErrTokenInvalid
	}
	role := authz.PermissionsOf(u.Role)
	t.Permissions = slices.DeleteFunc(t.Permissions, func(p string) bool { return !slices.Contains(role, p) })
	if now.Unix()-t.LastUsedAt > int64(tokenTouchEvery/time.Second) {
		if _, err := q.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now.Unix(), t.ID); err == nil {
			t.LastUsedAt = now.Unix()
		}
	}
	return t, u, nil
}

func ipAllowed(allowed []string, remote string) bool {
	addr, err := netip.ParseAddr(remote)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, s := range allowed {
		if p, err := netip.ParsePrefix(s); err == nil && p.Contains(addr) {
			return true
		}
	}
	return false
}
