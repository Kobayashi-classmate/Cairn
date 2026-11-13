// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/store"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "cairn.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	// Migrations must be idempotent.
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

const goodPassword = "correct horse battery"

func TestPasswordHashRoundTrip(t *testing.T) {
	h, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("unexpected encoding %s", h)
	}
	if ok, err := VerifyPassword(goodPassword, h); !ok || err != nil {
		t.Fatalf("verify failed: %v %v", ok, err)
	}
	if ok, _ := VerifyPassword("wrong password!!", h); ok {
		t.Fatal("wrong password accepted")
	}
	if _, err := VerifyPassword(goodPassword, "$2y$10$bcrypt"); err == nil {
		t.Fatal("foreign hash format should error")
	}
}

func TestValidation(t *testing.T) {
	for _, c := range []struct {
		user, rule string
	}{{"", "required"}, {"ab", "min_length"}, {"Admin", "pattern"}, {"1admin", "pattern"}, {strings.Repeat("a", 33), "max_length"}} {
		var fe *FieldError
		if err := ValidateUsername(c.user); !errors.As(err, &fe) || fe.Rule != c.rule {
			t.Errorf("username %q: want rule %s, got %v", c.user, c.rule, err)
		}
	}
	if err := ValidateUsername("ops.admin-1"); err != nil {
		t.Errorf("valid username rejected: %v", err)
	}
	var fe *FieldError
	if err := ValidatePassword("short"); !errors.As(err, &fe) || fe.Rule != "min_length" || fe.Min != MinPasswordLen {
		t.Errorf("short password: %v", err)
	}
	// 12 two-byte characters: long enough by character count.
	if err := ValidatePassword(strings.Repeat("ü", 12)); err != nil {
		t.Errorf("length must count characters, not bytes: %v", err)
	}
}

func TestAuthenticate(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := CreateUser(ctx, db, "admin", goodPassword, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Authenticate(ctx, db, "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{"admin", "wrong password!!"}, {"nobody", goodPassword}} {
		if _, err := Authenticate(ctx, db, c[0], c[1]); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%v: want ErrInvalidCredentials, got %v", c, err)
		}
	}
}

func TestSessionsExpire(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u, err := CreateUser(ctx, db, "admin", goodPassword, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	token, err := CreateSession(ctx, db, u.ID, "203.0.113.5", "test")
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := LookupSession(ctx, db, token)
	if err != nil || got.Username != "admin" || got.Locale != "zh-CN" {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	// The token itself is never stored.
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, token).Scan(&n)
	if n != 0 {
		t.Fatal("raw token found in database")
	}

	// Idle timeout.
	if _, err := db.Exec(`UPDATE sessions SET last_seen_at = ?`, time.Now().Add(-IdleTimeout-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LookupSession(ctx, db, token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("idle session accepted: %v", err)
	}

	// Absolute timeout.
	token, _ = CreateSession(ctx, db, u.ID, "", "")
	if _, err := db.Exec(`UPDATE sessions SET expires_at = ?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LookupSession(ctx, db, token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("expired session accepted: %v", err)
	}
}

func TestThrottleLocksAfterRepeatedFailures(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	key := IPKey("198.51.100.7")
	for i := 0; i < freeFailures-1; i++ {
		_ = RecordFailure(ctx, db, key)
	}
	if d, _ := LockedFor(ctx, db, key); d > 0 {
		t.Fatalf("locked too early: %v", d)
	}
	_ = RecordFailure(ctx, db, key)
	if d, _ := LockedFor(ctx, db, key); d <= 0 || d > baseLock {
		t.Fatalf("want a lock of about %v, got %v", baseLock, d)
	}
	_ = RecordFailure(ctx, db, key)
	if d, _ := LockedFor(ctx, db, key); d <= baseLock {
		t.Fatalf("lock should double, got %v", d)
	}
	_ = ResetFailures(ctx, db, key)
	if d, _ := LockedFor(ctx, db, key); d != 0 {
		t.Fatalf("reset did not clear lock: %v", d)
	}
}

func TestSetupFlow(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	token, err := CreateSetupToken(ctx, db, SetupTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompleteSetup(ctx, db, "not-the-token", "admin", goodPassword, "en"); !errors.Is(err, ErrSetupToken) {
		t.Fatalf("bad token: %v", err)
	}
	var fe *FieldError
	if _, err := CompleteSetup(ctx, db, token, "admin", "short", "en"); !errors.As(err, &fe) {
		t.Fatalf("weak password should fail validation: %v", err)
	}

	// Two concurrent completions: exactly one may win.
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = CompleteSetup(ctx, db, token, fmt.Sprintf("admin%d", i), goodPassword, "en")
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("want exactly one successful setup, got results %v", results)
	}
	if _, err := CreateSetupToken(ctx, db, SetupTokenTTL); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("token issued after setup: %v", err)
	}
}

func TestSetupTokenExpires(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	token, err := CreateSetupToken(ctx, db, -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompleteSetup(ctx, db, token, "admin", goodPassword, "en"); !errors.Is(err, ErrSetupToken) {
		t.Fatalf("expired token accepted: %v", err)
	}
}

func TestRandomPassword(t *testing.T) {
	p, err := RandomPassword(20)
	if err != nil || len(p) != 20 {
		t.Fatalf("got %q %v", p, err)
	}
	if strings.ContainsAny(p, "0O1lI") {
		t.Fatalf("ambiguous characters in %q", p)
	}
}
