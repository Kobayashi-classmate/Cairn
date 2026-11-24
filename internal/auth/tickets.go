// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"time"
)

// Tickets. A request the browser makes by itself (a WebSocket handshake, a
// download link, an image) carries the session cookie but cannot carry the
// header with the session key. The page first trades the key for a ticket,
// in an ordinary request, and puts the ticket in the address instead. A
// ticket is for one request: it names the session and the exact address,
// lasts seconds and is spent by its first use, whether that succeeds or
// not. It replaces the key only, so the cookie of the same session is still
// required, and it ends with the session.
const (
	// TicketTTL is how long a ticket waits to be used.
	TicketTTL = 30 * time.Second
	// maxTickets is how many unused tickets a session can hold. A page asks
	// for one right before it uses it; anything near this is not a page.
	maxTickets = 64
)

// ErrTooManyTickets means the session holds too many unused tickets; some
// expire within TicketTTL.
var ErrTooManyTickets = errors.New("auth: too many unused tickets")

// TicketTarget names the request a ticket is for: the path and the query,
// whatever the order of its parameters, without the parameter that carries
// the ticket itself.
func TicketTarget(path string, query url.Values, ticketParam string) string {
	q := url.Values{}
	for k, v := range query {
		if k != ticketParam {
			q[k] = v
		}
	}
	// Encode sorts by key; a NUL cannot be part of a path.
	return path + "\x00" + q.Encode()
}

// IssueTicket returns a new ticket of the session for target (see
// TicketTarget).
func IssueTicket(ctx context.Context, db *sql.DB, sessionID, target string) (string, error) {
	if sessionID == "" {
		return "", ErrSessionInvalid
	}
	ticket, err := RandomToken(tokenBytes)
	if err != nil {
		return "", err
	}
	tx, err := db.BeginTx(ctx, nil) // BEGIN IMMEDIATE (see store.Open): the count holds
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := time.Now()
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_tickets WHERE session_id = ? AND expires_at < ?`, sessionID, now.Unix()); err != nil {
		return "", err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM session_tickets WHERE session_id = ?`, sessionID).Scan(&n); err != nil {
		return "", err
	}
	if n >= maxTickets {
		return "", ErrTooManyTickets
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE id = ?`, sessionID).Scan(&n); err != nil {
		return "", err
	}
	if n == 0 {
		return "", ErrSessionInvalid
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_tickets(token_hash, session_id, target, expires_at) VALUES(?, ?, ?, ?)`,
		hashToken(ticket), sessionID, hashToken(target), now.Add(TicketTTL).Unix()); err != nil {
		return "", err
	}
	return ticket, tx.Commit()
}

// RedeemTicket validates a cookie token together with a ticket for target
// and returns the session and user, as LookupSession does with the session
// key. The ticket is spent first, in one statement, so it is used at most
// once however the rest goes: wrong session, wrong target, too late.
func RedeemTicket(ctx context.Context, q queryer, token, ticket, target string) (*Session, *User, error) {
	if token == "" || len(token) > 128 || ticket == "" || len(ticket) > 128 {
		return nil, nil, ErrSessionInvalid
	}
	var sessionID, targetHash string
	var expires int64
	err := q.QueryRowContext(ctx, `DELETE FROM session_tickets WHERE token_hash = ? RETURNING session_id, target, expires_at`,
		hashToken(ticket)).Scan(&sessionID, &targetHash, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrSessionInvalid
	}
	if err != nil {
		return nil, nil, err
	}
	if time.Now().Unix() > expires || !sameHash(sessionID, hashToken(token)) || !sameHash(targetHash, hashToken(target)) {
		return nil, nil, ErrSessionInvalid
	}
	return liveSession(ctx, q, sessionID, func(*Session) bool { return true })
}
