// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTokenLifecycle(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u, _ := CreateUser(ctx, db, "ops", goodPassword, "", "operator")

	var fe *FieldError
	for want, req := range map[string]TokenRequest{
		"name:required":        {Permissions: []string{"system.read"}},
		"permissions:required": {Name: "ci"},
		"permissions:subset":   {Name: "ci", Permissions: []string{"users.manage"}},
		"expiresInDays:range":  {Name: "ci", Permissions: []string{"system.read"}, ExpiresInDays: -1},
		"allowedIps:ip":        {Name: "ci", Permissions: []string{"system.read"}, AllowedIPs: []string{"10.0.0.0/33"}},
	} {
		if _, _, err := CreateToken(ctx, db, u, req); !errors.As(err, &fe) || fe.Field+":"+fe.Rule != want {
			t.Errorf("%s: got %v", want, err)
		}
	}

	token, info, err := CreateToken(ctx, db, u, TokenRequest{Name: " ci ", Permissions: []string{"system.read", "system.read"},
		ExpiresInDays: 30, AllowedIPs: []string{"203.0.113.0/24", "2001:db8::1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, TokenPrefix) || info.Prefix != token[:tokenPrefixLen] || info.Name != "ci" || len(info.Permissions) != 1 {
		t.Fatalf("token %q info %+v", token, info)
	}
	var stored int
	_ = db.QueryRow(`SELECT COUNT(*) FROM api_tokens WHERE token_hash = ?`, token).Scan(&stored)
	if stored != 0 {
		t.Fatal("raw token stored")
	}

	if _, _, err := LookupToken(ctx, db, token, "198.51.100.1"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("address outside the allow-list accepted: %v", err)
	}
	tok, owner, err := LookupToken(ctx, db, token, "203.0.113.77")
	if err != nil || owner.ID != u.ID || tok.Permissions[0] != "system.read" {
		t.Fatalf("lookup: %+v %v", tok, err)
	}
	if _, _, err := LookupToken(ctx, db, token, "2001:db8::1"); err != nil {
		t.Fatalf("IPv6 allow-list entry: %v", err)
	}
	if _, _, err := LookupToken(ctx, db, token+"x", "203.0.113.77"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatal("altered token accepted")
	}

	// Demotion shrinks what existing tokens can do.
	admin, _ := CreateUser(ctx, db, "root", goodPassword, "", "admin")
	atok, _, err := CreateToken(ctx, db, admin, TokenRequest{Name: "full", Permissions: []string{"users.manage", "system.read"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = SetRole(ctx, db, admin.ID, "operator")
	got, _, _ := LookupToken(ctx, db, atok, "127.0.0.1")
	if len(got.Permissions) != 1 || got.Permissions[0] != "system.read" {
		t.Fatalf("permissions after demotion: %v", got.Permissions)
	}

	// Expiry and disabled owners.
	if _, err := db.Exec(`UPDATE api_tokens SET expires_at = 1 WHERE id = ?`, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LookupToken(ctx, db, token, "203.0.113.77"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatal("expired token accepted")
	}
	_ = SetDisabled(ctx, db, admin.ID, true)
	if _, _, err := LookupToken(ctx, db, atok, "127.0.0.1"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatal("token of a disabled user accepted")
	}

	// Revocation is scoped to the owner.
	if _, err := DeleteToken(ctx, db, admin.ID, info.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked another user's token")
	}
	if name, err := DeleteToken(ctx, db, u.ID, info.ID); err != nil || name != "ci" {
		t.Fatalf("delete: %q %v", name, err)
	}
	if list, _ := ListTokens(ctx, db, u.ID); len(list) != 0 {
		t.Fatalf("tokens left: %+v", list)
	}
}
