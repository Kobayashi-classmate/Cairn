// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
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
	ctx := context.Background()
	h, err := HashPassword(ctx, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("unexpected encoding %s", h)
	}
	if ok, err := VerifyPassword(ctx, goodPassword, h); !ok || err != nil {
		t.Fatalf("verify failed: %v %v", ok, err)
	}
	if ok, _ := VerifyPassword(ctx, "wrong password!!", h); ok {
		t.Fatal("wrong password accepted")
	}
	if _, err := VerifyPassword(ctx, goodPassword, "$2y$10$bcrypt"); err == nil {
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
	if _, err := CreateUser(ctx, db, "admin", goodPassword, "", "admin"); err != nil {
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
	u, err := CreateUser(ctx, db, "admin", goodPassword, "zh-CN", "admin")
	if err != nil {
		t.Fatal(err)
	}
	token, key, err := CreateSession(ctx, db, u.ID, "203.0.113.5", "test")
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := LookupSession(ctx, db, token, key)
	if err != nil || got.Username != "admin" || got.Locale != "zh-CN" {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	// Neither the token nor the key is stored.
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id IN (?, ?) OR key_hash IN (?, ?)`, token, key, token, key).Scan(&n)
	if n != 0 {
		t.Fatal("raw token or key found in database")
	}

	// Idle timeout.
	if _, err := db.Exec(`UPDATE sessions SET last_seen_at = ?`, time.Now().Add(-IdleTimeout-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LookupSession(ctx, db, token, key); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("idle session accepted: %v", err)
	}

	// Absolute timeout.
	token, key, _ = CreateSession(ctx, db, u.ID, "", "")
	if _, err := db.Exec(`UPDATE sessions SET expires_at = ?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LookupSession(ctx, db, token, key); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("expired session accepted: %v", err)
	}
}

// The cookie token is half of a session: whoever holds it without the
// session key (a website on the panel's host name gets it from the browser)
// has nothing, and trying leaves the session as it was.
func TestSessionNeedsItsKey(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u, _ := CreateUser(ctx, db, "admin", goodPassword, "", "admin")
	token, key, err := CreateSession(ctx, db, u.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if token == key {
		t.Fatal("the key is the token")
	}
	otherToken, otherKey, _ := CreateSession(ctx, db, u.ID, "", "")
	for name, k := range map[string]string{"no key": "", "the token as key": token, "another session's key": otherKey,
		"a made-up key": "bm90IHRoZSBrZXk", "an oversized key": strings.Repeat("k", 129)} {
		if _, _, err := LookupSession(ctx, db, token, k); !errors.Is(err, ErrSessionInvalid) {
			t.Errorf("%s: want ErrSessionInvalid, got %v", name, err)
		}
	}
	if _, _, err := LookupSession(ctx, db, otherToken, key); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("a key opened another session: %v", err)
	}
	// A session whose row has no key hash (none can be made any more) opens
	// with nothing, the empty key included.
	if _, err := db.Exec(`UPDATE sessions SET key_hash = '' WHERE id = ?`, SessionID(otherToken)); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"", otherKey} {
		if _, _, err := LookupSession(ctx, db, otherToken, k); !errors.Is(err, ErrSessionInvalid) {
			t.Errorf("a session without a key hash opened with %q: %v", k, err)
		}
	}
	// A wrong key does not count as activity of the session.
	if _, err := db.Exec(`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).Unix(), SessionID(token)); err != nil {
		t.Fatal(err)
	}
	_, _, _ = LookupSession(ctx, db, token, otherKey)
	var seen int64
	_ = db.QueryRow(`SELECT last_seen_at FROM sessions WHERE id = ?`, SessionID(token)).Scan(&seen)
	if time.Since(time.Unix(seen, 0)) < 30*time.Minute {
		t.Error("a wrong key kept the session alive")
	}
	if s, _, err := LookupSession(ctx, db, token, key); err != nil || time.Since(time.Unix(s.LastSeenAt, 0)) > time.Minute {
		t.Fatalf("the right key: %+v %v", s, err)
	}
	// Signing out ends the key with the session.
	if err := DeleteSession(ctx, db, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LookupSession(ctx, db, token, key); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("a deleted session opened: %v", err)
	}
}

// A ticket stands in for the session key for one request: once, soon, for
// that address and that session only, and never without the cookie.
func TestTickets(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u, _ := CreateUser(ctx, db, "admin", goodPassword, "", "admin")
	token, _, _ := CreateSession(ctx, db, u.ID, "", "")
	otherToken, _, _ := CreateSession(ctx, db, u.ID, "", "")
	id := SessionID(token)
	target := TicketTarget("/api/v1/files/download", url.Values{"root": {"sites"}, "path": {"a/b.png"}}, "ticket")
	issue := func() string {
		t.Helper()
		ticket, err := IssueTicket(ctx, db, id, target)
		if err != nil {
			t.Fatal(err)
		}
		return ticket
	}
	refused := func(what, tok, ticket, target string) {
		t.Helper()
		if _, _, err := RedeemTicket(ctx, db, tok, ticket, target); !errors.Is(err, ErrSessionInvalid) {
			t.Errorf("%s: want ErrSessionInvalid, got %v", what, err)
		}
	}

	// The order of the parameters and the ticket itself do not change what
	// a ticket is for; anything else does.
	same := TicketTarget("/api/v1/files/download", url.Values{"path": {"a/b.png"}, "ticket": {"x"}, "root": {"sites"}}, "ticket")
	if same != target {
		t.Fatalf("targets differ: %q %q", same, target)
	}
	for _, other := range []string{
		TicketTarget("/api/v1/files/download", url.Values{"root": {"sites"}, "path": {"a/c.png"}}, "ticket"),
		TicketTarget("/api/v1/files/download", url.Values{"root": {"system"}, "path": {"a/b.png"}}, "ticket"),
		TicketTarget("/api/v1/files/download", url.Values{"root": {"sites"}, "path": {"a/b.png"}, "inline": {"true"}}, "ticket"),
		TicketTarget("/api/v1/files/downloads", url.Values{"root": {"sites"}, "path": {"a/b.png"}}, "ticket"),
		TicketTarget("/api/v1/files/download", url.Values{"root": {"sites&path=a/b.png"}}, "ticket"),
	} {
		if other == target {
			t.Fatalf("a different request has the same target: %q", other)
		}
	}

	ticket := issue()
	var stored int
	_ = db.QueryRow(`SELECT count(*) FROM session_tickets WHERE token_hash = ? OR target = ?`, ticket, target).Scan(&stored)
	if stored != 0 {
		t.Fatal("raw ticket or target found in database")
	}
	s, got, err := RedeemTicket(ctx, db, token, ticket, target)
	if err != nil || got.Username != "admin" || s.ID != id {
		t.Fatalf("redeem: %+v %+v %v", s, got, err)
	}
	refused("a second use", token, ticket, target)

	// Each refusal spends the ticket too: the right use after it fails.
	ticket = issue()
	refused("another target", token, ticket, TicketTarget("/api/v1/files/download", url.Values{"root": {"sites"}, "path": {"other"}}, "ticket"))
	refused("the right target after a wrong one", token, ticket, target)
	ticket = issue()
	refused("another session's cookie", otherToken, ticket, target)
	refused("the right cookie after a wrong one", token, ticket, target)
	// A request without the cookie is refused before the ticket is looked at.
	ticket = issue()
	refused("no cookie", "", ticket, target)
	if _, _, err := RedeemTicket(ctx, db, token, ticket, target); err != nil {
		t.Errorf("a request without a cookie spent the ticket: %v", err)
	}
	refused("no ticket", token, "", target)
	refused("a made-up ticket", token, "bm90IGEgdGlja2V0", target)

	// Too late.
	ticket = issue()
	if _, err := db.Exec(`UPDATE session_tickets SET expires_at = ?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	refused("an expired ticket", token, ticket, target)

	// The session's timeouts hold for tickets as for the key.
	ticket = issue()
	if _, err := db.Exec(`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, time.Now().Add(-IdleTimeout-time.Minute).Unix(), id); err != nil {
		t.Fatal(err)
	}
	refused("a ticket of an idle session", token, ticket, target)

	// Tickets end with their session: signed out, revoked, or everywhere.
	for name, end := range map[string]func(tok string) error{
		"sign-out":   func(tok string) error { return DeleteSession(ctx, db, tok) },
		"revocation": func(tok string) error { _, err := DeleteSessionByID(ctx, db, u.ID, SessionID(tok)); return err },
		"everywhere": func(string) error { return DeleteUserSessions(ctx, db, u.ID) },
	} {
		tok, _, _ := CreateSession(ctx, db, u.ID, "", "")
		ticket, err := IssueTicket(ctx, db, SessionID(tok), target)
		if err != nil {
			t.Fatal(err)
		}
		if err := end(tok); err != nil {
			t.Fatal(err)
		}
		refused("a ticket after "+name, tok, ticket, target)
		var left int
		_ = db.QueryRow(`SELECT count(*) FROM session_tickets WHERE session_id = ?`, SessionID(tok)).Scan(&left)
		if left != 0 {
			t.Errorf("%s left %d tickets behind", name, left)
		}
		if _, err := IssueTicket(ctx, db, SessionID(tok), target); !errors.Is(err, ErrSessionInvalid) {
			t.Errorf("a ticket for a session ended by %s: %v", name, err)
		}
	}

	// A session cannot pile tickets up; expired ones make room.
	tok, _, _ := CreateSession(ctx, db, u.ID, "", "")
	for range maxTickets {
		if _, err := IssueTicket(ctx, db, SessionID(tok), target); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := IssueTicket(ctx, db, SessionID(tok), target); !errors.Is(err, ErrTooManyTickets) {
		t.Fatalf("one ticket too many: %v", err)
	}
	if _, err := db.Exec(`UPDATE session_tickets SET expires_at = ? WHERE session_id = ?`, time.Now().Add(-time.Second).Unix(), SessionID(tok)); err != nil {
		t.Fatal(err)
	}
	if _, err := IssueTicket(ctx, db, SessionID(tok), target); err != nil {
		t.Fatalf("after the old tickets expired: %v", err)
	}
	if err := PurgeExpired(ctx, db); err != nil {
		t.Fatal(err)
	}
}

// A ticket is used once even when many requests bring it at the same time.
func TestTicketUsedOnceConcurrently(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u, _ := CreateUser(ctx, db, "admin", goodPassword, "", "admin")
	token, _, _ := CreateSession(ctx, db, u.ID, "", "")
	target := TicketTarget("/api/v1/terminal/sessions/abc/ws", nil, "ticket")
	ticket, err := IssueTicket(ctx, db, SessionID(token), target)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := RedeemTicket(ctx, db, token, ticket, target); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			} else if !errors.Is(err, ErrSessionInvalid) {
				t.Errorf("redeem: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("the ticket was accepted %d times", ok)
	}
}

// The device cookie alone does not make a browser a known device: the
// device key has to come with it.
func TestKnownDeviceNeedsItsKey(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	alice, _ := CreateUser(ctx, db, "alice", goodPassword, "", "admin")
	bob, _ := CreateUser(ctx, db, "bob", goodPassword, "", "operator")
	token, key, err := RememberDevice(ctx, db, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	bobToken, bobKey, _ := RememberDevice(ctx, db, bob.ID)
	known := func(token, key, username string) bool {
		t.Helper()
		ok, err := KnownDevice(ctx, db, token, key, username)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !known(token, key, "alice") {
		t.Fatal("the device is not known with its cookie and key")
	}
	for name, c := range map[string][3]string{
		"the cookie alone":       {token, "", "alice"},
		"the cookie as its key":  {token, token, "alice"},
		"another device's key":   {token, bobKey, "alice"},
		"the key alone":          {"", key, "alice"},
		"another account":        {token, key, "bob"},
		"another device's token": {bobToken, key, "alice"},
	} {
		if known(c[0], c[1], c[2]) {
			t.Errorf("%s made a known device", name)
		}
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM known_devices WHERE token_hash IN (?, ?) OR key_hash IN (?, ?)`, token, key, token, key).Scan(&n)
	if n != 0 {
		t.Fatal("raw device token or key found in database")
	}
	if _, err := db.Exec(`UPDATE known_devices SET expires_at = ?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if known(token, key, "alice") {
		t.Fatal("an expired device is known")
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

func TestDeleteOtherSessionsNeedsASessionToKeep(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u, _ := CreateUser(ctx, db, "alice", goodPassword, "", "admin")
	a, _, _ := CreateSession(ctx, db, u.ID, "", "")
	_, _, _ = CreateSession(ctx, db, u.ID, "", "")
	// An API token's request has no session: "keep none" must not mean
	// "sign out everywhere".
	if _, err := DeleteOtherSessions(ctx, db, u.ID, ""); err == nil {
		t.Fatal("an empty session to keep was accepted")
	}
	if list, _ := ListSessions(ctx, db, u.ID, ""); len(list) != 2 {
		t.Fatalf("sessions after a refused call: %d", len(list))
	}
	if n, err := DeleteOtherSessions(ctx, db, u.ID, SessionID(a)); err != nil || n != 1 {
		t.Fatalf("delete others: %d %v", n, err)
	}
}
