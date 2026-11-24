// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/secrets"
)

// RFC 6238 appendix B vectors (SHA-1, 8 digits) truncated to 6 digits: a
// 6-digit code is the 8-digit code modulo 10^6.
func TestTOTPMatchesRFC6238(t *testing.T) {
	key := []byte("12345678901234567890")
	for _, c := range []struct {
		unix int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}} {
		if got := totpCode(key, c.unix/totpPeriod); got != c.want {
			t.Errorf("t=%d: got %s want %s", c.unix, got, c.want)
		}
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key)
	now := time.Unix(1111111111, 0)
	step, ok := VerifyTOTP(secret, "050 471", now, 0)
	if !ok || step != 1111111111/totpPeriod {
		t.Fatalf("verify: %v %d", ok, step)
	}
	if _, ok := VerifyTOTP(secret, "050471", now, step); ok {
		t.Fatal("the same step must not be accepted twice")
	}
	if _, ok := VerifyTOTP(secret, "050471", now.Add(2*time.Minute), 0); ok {
		t.Fatal("code accepted far outside the skew window")
	}
}

func codeNow(t *testing.T, secret string, offset time.Duration) string {
	t.Helper()
	key, err := b32.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return totpCode(key, time.Now().Add(offset).Unix()/totpPeriod)
}

func TestTOTPEnrollmentAndSecondFactor(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	box, err := secrets.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, err := CreateUser(ctx, db, "admin", goodPassword, "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmTOTP(ctx, db, box, u.ID, "123456"); !errors.Is(err, ErrTOTPNotPending) {
		t.Fatalf("confirm without begin: %v", err)
	}
	secret, uri, err := BeginTOTP(ctx, db, box, u)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, "otpauth://totp/Cairn:admin?") || !strings.Contains(uri, "secret="+secret) {
		t.Fatalf("bad uri %s", uri)
	}
	var stored string
	_ = db.QueryRow(`SELECT totp_pending FROM users WHERE id = ?`, u.ID).Scan(&stored)
	if strings.Contains(stored, secret) {
		t.Fatal("TOTP secret stored in plaintext")
	}
	if _, err := ConfirmTOTP(ctx, db, box, u.ID, "000000"); !errors.Is(err, ErrMFAInvalidCode) {
		t.Fatalf("wrong confirm code: %v", err)
	}
	// Use the previous step for enrollment so the current one stays fresh.
	codes, err := ConfirmTOTP(ctx, db, box, u.ID, codeNow(t, secret, -totpPeriod*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != recoveryCodeCount || len(codes[0]) != 14 {
		t.Fatalf("recovery codes: %v", codes)
	}
	u, _ = UserByID(ctx, db, u.ID)
	if !u.TOTPEnabled {
		t.Fatal("TOTP not enabled")
	}
	if _, _, err := BeginTOTP(ctx, db, box, u); !errors.Is(err, ErrTOTPAlreadyEnabled) {
		t.Fatalf("second enrollment: %v", err)
	}

	// Second factor with a TOTP code: once only.
	code := codeNow(t, secret, 0)
	if err := VerifySecondFactor(ctx, db, box, u.ID, code, ""); err != nil {
		t.Fatalf("valid code rejected: %v", err)
	}
	if err := VerifySecondFactor(ctx, db, box, u.ID, code, ""); !errors.Is(err, ErrMFAInvalidCode) {
		t.Fatalf("replayed code accepted: %v", err)
	}
	if err := VerifySecondFactor(ctx, db, box, u.ID, "", ""); !errors.Is(err, ErrMFARequired) {
		t.Fatalf("empty factor: %v", err)
	}

	// Recovery codes: case and dashes do not matter, each works once.
	rc := strings.ToUpper(strings.ReplaceAll(codes[3], "-", " "))
	if err := VerifySecondFactor(ctx, db, box, u.ID, "", rc); err != nil {
		t.Fatalf("recovery code rejected: %v", err)
	}
	if err := VerifySecondFactor(ctx, db, box, u.ID, "", codes[3]); !errors.Is(err, ErrMFAInvalidCode) {
		t.Fatal("recovery code accepted twice")
	}
	// Another user's code never matches (hash is bound to the user).
	other, _ := CreateUser(ctx, db, "bob", goodPassword, "", "operator")
	if err := VerifySecondFactor(ctx, db, box, other.ID, "", codes[4]); !errors.Is(err, ErrMFAInvalidCode) {
		t.Fatal("recovery code accepted for another user")
	}

	if err := DisableTOTP(ctx, db, u.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM recovery_codes WHERE user_id = ?`, u.ID).Scan(&n)
	if n != 0 {
		t.Fatal("recovery codes survived disabling")
	}
}

func TestMFAChallengeLimits(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u, _ := CreateUser(ctx, db, "admin", goodPassword, "", "admin")
	token, err := CreateMFAChallenge(ctx, db, u.ID, "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	// Concurrent claims get exactly the challenge's attempts.
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := 0; i < 3*mfaMaxAttempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := ClaimMFAChallenge(ctx, db, token)
			if err == nil && id == u.ID {
				mu.Lock()
				granted++
				mu.Unlock()
			} else if !errors.Is(err, ErrMFAChallengeExpired) {
				t.Errorf("claim: %v", err)
			}
		}()
	}
	wg.Wait()
	if granted != mfaMaxAttempts {
		t.Fatalf("%d attempts granted, want %d", granted, mfaMaxAttempts)
	}
	if _, err := ClaimMFAChallenge(ctx, db, token); !errors.Is(err, ErrMFAChallengeExpired) {
		t.Fatalf("challenge survived too many attempts: %v", err)
	}
	if _, err := ClaimMFAChallenge(ctx, db, "unknown"); !errors.Is(err, ErrMFAChallengeExpired) {
		t.Fatalf("unknown challenge: %v", err)
	}
}

func TestAttemptCountsBeforeChecking(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	key := IPKey("198.51.100.8")
	// Many attempts at once: only the free ones get through; the last of
	// them locks the key at once.
	var wg sync.WaitGroup
	var mu sync.Mutex
	passed := 0
	for i := 0; i < 4*freeFailures; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wait, err := Attempt(ctx, db, key)
			if err != nil {
				t.Errorf("attempt: %v", err)
				return
			}
			if wait == 0 {
				mu.Lock()
				passed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if passed != freeFailures {
		t.Fatalf("%d concurrent attempts passed, want %d", passed, freeFailures)
	}
	// A success clears the key.
	if err := ResetFailures(ctx, db, key); err != nil {
		t.Fatal(err)
	}
	if wait, err := Attempt(ctx, db, key); err != nil || wait != 0 {
		t.Fatalf("after reset: %v %v", wait, err)
	}
	// A username only backs off for seconds, however many failures.
	user := UserKey("admin")
	for i := 0; i < 40; i++ {
		_ = RecordFailure(ctx, db, user)
	}
	if d, _ := LockedFor(ctx, db, user); d <= 0 || d > maxBackoff {
		t.Fatalf("username back-off %v, want at most %v", d, maxBackoff)
	}
}

func TestHashingIsBounded(t *testing.T) {
	h, err := HashPassword(context.Background(), goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	// With every slot taken, hashing waits and gives up with the context.
	for i := 0; i < cap(hashSlots); i++ {
		hashSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(hashSlots); i++ {
			<-hashSlots
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := HashPassword(ctx, goodPassword); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hashing without a slot: %v", err)
	}
	// Nor does it wait longer than hashWait.
	defer func(d time.Duration) { hashWait = d }(hashWait)
	hashWait = 20 * time.Millisecond
	if _, err := VerifyPassword(context.Background(), goodPassword, h); !errors.Is(err, ErrBusy) {
		t.Fatalf("verifying without a slot: %v", err)
	}
}

func TestSessionScopingAndElevation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	alice, _ := CreateUser(ctx, db, "alice", goodPassword, "", "admin")
	bob, _ := CreateUser(ctx, db, "bob", goodPassword, "", "operator")
	a1, k1, _ := CreateSession(ctx, db, alice.ID, "", "")
	a2, k2, _ := CreateSession(ctx, db, alice.ID, "", "")
	b1, _, _ := CreateSession(ctx, db, bob.ID, "", "")

	// Alice cannot revoke Bob's session by id.
	if ok, _ := DeleteSessionByID(ctx, db, alice.ID, SessionID(b1)); ok {
		t.Fatal("revoked another user's session")
	}
	list, _ := ListSessions(ctx, db, alice.ID, SessionID(a1))
	if len(list) != 2 || list[0].Current == list[1].Current {
		t.Fatalf("want two sessions, exactly one current: %+v", list)
	}
	if n, _ := DeleteOtherSessions(ctx, db, alice.ID, SessionID(a1)); n != 1 {
		t.Fatalf("want 1 revoked, got %d", n)
	}
	if _, _, err := LookupSession(ctx, db, a2, k2); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("revoked session still valid")
	}

	s, _, _ := LookupSession(ctx, db, a1, k1)
	if s.Elevated(time.Now()) {
		t.Fatal("new session must not be elevated")
	}
	if _, err := Elevate(ctx, db, s.ID); err != nil {
		t.Fatal(err)
	}
	s, _, _ = LookupSession(ctx, db, a1, k1)
	if !s.Elevated(time.Now()) || s.Elevated(time.Now().Add(ElevationWindow+time.Second)) {
		t.Fatal("elevation window wrong")
	}

	if err := CheckPassword(ctx, db, alice.ID, "nope nope nope"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if n, _ := CountActiveAdmins(ctx, db); n != 1 {
		t.Fatalf("admins: %d", n)
	}
	if err := SetDisabled(ctx, db, alice.ID, true); err != nil {
		t.Fatal(err)
	}
	if n, _ := CountActiveAdmins(ctx, db); n != 0 {
		t.Fatal("disabled admin still counted")
	}
	if _, _, err := LookupSession(ctx, db, a1, k1); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("disabled user's session still valid")
	}
	if _, err := CreateUser(ctx, db, "bob", goodPassword, "", "operator"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate username: %v", err)
	}
	var fe *FieldError
	if _, err := CreateUser(ctx, db, "carol", goodPassword, "", "root"); !errors.As(err, &fe) || fe.Rule != "enum" {
		t.Fatalf("invalid role: %v", err)
	}
}
