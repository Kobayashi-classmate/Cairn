// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/secrets"
)

// Second-factor settings.
const (
	totpEnrollTTL     = 10 * time.Minute
	mfaChallengeTTL   = 5 * time.Minute
	mfaMaxAttempts    = 5
	recoveryCodeCount = 10
)

var (
	ErrMFARequired         = errors.New("auth: second factor required")
	ErrMFAInvalidCode      = errors.New("auth: invalid second-factor code")
	ErrMFAChallengeExpired = errors.New("auth: sign-in challenge expired")
	ErrTOTPNotPending      = errors.New("auth: no TOTP enrollment in progress")
	ErrTOTPAlreadyEnabled  = errors.New("auth: TOTP already enabled")
)

func totpContext(userID int64) string { return "totp:user:" + strconv.FormatInt(userID, 10) }

// BeginTOTP starts enrollment: a new secret is stored sealed as pending for
// a few minutes and returned (with its otpauth URI) for the user's app.
func BeginTOTP(ctx context.Context, q queryer, box *secrets.Box, u *User) (secret, uri string, err error) {
	if u.TOTPEnabled {
		return "", "", ErrTOTPAlreadyEnabled
	}
	secret, err = NewTOTPSecret()
	if err != nil {
		return "", "", err
	}
	sealed, err := box.Seal([]byte(secret), totpContext(u.ID))
	if err != nil {
		return "", "", err
	}
	_, err = q.ExecContext(ctx, `UPDATE users SET totp_pending = ?, totp_pending_expires = ? WHERE id = ?`,
		sealed, time.Now().Add(totpEnrollTTL).Unix(), u.ID)
	return secret, TOTPURI(secret, u.Username), err
}

// ConfirmTOTP finishes enrollment with a code from the app, enables TOTP and
// returns a fresh set of recovery codes.
func ConfirmTOTP(ctx context.Context, db *sql.DB, box *secrets.Box, userID int64, code string) ([]string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var sealed string
	var expires int64
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT totp_pending, totp_pending_expires, totp_enabled FROM users WHERE id = ?`, userID).
		Scan(&sealed, &expires, &enabled)
	if err != nil {
		return nil, err
	}
	if enabled {
		return nil, ErrTOTPAlreadyEnabled
	}
	if sealed == "" || time.Now().Unix() > expires {
		return nil, ErrTOTPNotPending
	}
	secret, err := box.Open(sealed, totpContext(userID))
	if err != nil {
		return nil, err
	}
	step, ok := VerifyTOTP(string(secret), code, time.Now(), 0)
	if !ok {
		return nil, ErrMFAInvalidCode
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET totp_secret = totp_pending, totp_enabled = 1, totp_last_step = ?,
		   totp_pending = '', totp_pending_expires = 0, updated_at = ? WHERE id = ?`,
		step, time.Now().Unix(), userID); err != nil {
		return nil, err
	}
	codes, err := replaceRecoveryCodes(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	return codes, tx.Commit()
}

// DisableTOTP turns two-step sign-in off and drops the recovery codes.
func DisableTOTP(ctx context.Context, q queryer, userID int64) error {
	if _, err := q.ExecContext(ctx,
		`UPDATE users SET totp_secret = '', totp_enabled = 0, totp_last_step = 0,
		   totp_pending = '', totp_pending_expires = 0, updated_at = ? WHERE id = ?`,
		time.Now().Unix(), userID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID)
	return err
}

// RegenerateRecoveryCodes replaces a user's recovery codes.
func RegenerateRecoveryCodes(ctx context.Context, db *sql.DB, u *User) ([]string, error) {
	if !u.TOTPEnabled {
		return nil, ErrTOTPNotPending
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	codes, err := replaceRecoveryCodes(ctx, tx, u.ID)
	if err != nil {
		return nil, err
	}
	return codes, tx.Commit()
}

const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // 31 symbols, no look-alikes

func replaceRecoveryCodes(ctx context.Context, q queryer, userID int64) ([]string, error) {
	if _, err := q.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return nil, err
	}
	codes := make([]string, 0, recoveryCodeCount)
	for len(codes) < recoveryCodeCount {
		raw, err := randomFrom(recoveryAlphabet, 12)
		if err != nil {
			return nil, err
		}
		code := raw[0:4] + "-" + raw[4:8] + "-" + raw[8:12]
		if _, err := q.ExecContext(ctx, `INSERT INTO recovery_codes(user_id, code_hash) VALUES(?, ?)`,
			userID, recoveryHash(userID, code)); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, nil
}

// recoveryHash hashes a normalized code. The codes carry ~59 bits of entropy,
// so a fast hash bound to the user is sufficient.
func recoveryHash(userID int64, code string) string {
	norm := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	sum := sha256.Sum256([]byte(strconv.FormatInt(userID, 10) + ":" + norm))
	return hex.EncodeToString(sum[:])
}

func randomFrom(alphabet string, n int) (string, error) {
	limit := 256 - 256%len(alphabet)
	out := make([]byte, 0, n)
	buf := make([]byte, 32)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, c := range buf {
			if int(c) < limit && len(out) < n {
				out = append(out, alphabet[int(c)%len(alphabet)])
			}
		}
	}
	return string(out), nil
}

// VerifySecondFactor checks a TOTP code or, if code is empty, a recovery
// code. A TOTP step and a recovery code can each be used only once.
func VerifySecondFactor(ctx context.Context, q queryer, box *secrets.Box, userID int64, code, recoveryCode string) error {
	if code == "" && recoveryCode == "" {
		return ErrMFARequired
	}
	if code != "" {
		var sealed string
		var lastStep int64
		if err := q.QueryRowContext(ctx, `SELECT totp_secret, totp_last_step FROM users WHERE id = ? AND totp_enabled = 1`, userID).
			Scan(&sealed, &lastStep); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrMFAInvalidCode
			}
			return err
		}
		secret, err := box.Open(sealed, totpContext(userID))
		if err != nil {
			return err
		}
		step, ok := VerifyTOTP(string(secret), code, time.Now(), lastStep)
		if !ok {
			return ErrMFAInvalidCode
		}
		// Conditional update: of two concurrent uses of the same code, one wins.
		res, err := q.ExecContext(ctx, `UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, userID, step)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrMFAInvalidCode
		}
		return nil
	}
	res, err := q.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at = 0`,
		time.Now().Unix(), userID, recoveryHash(userID, recoveryCode))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrMFAInvalidCode
	}
	return nil
}

// CreateMFAChallenge records that userID passed the password step and
// returns the token for the second step.
func CreateMFAChallenge(ctx context.Context, q queryer, userID int64, remote string) (string, error) {
	token, err := RandomToken(tokenBytes)
	if err != nil {
		return "", err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO mfa_challenges(token_hash, user_id, expires_at, remote_addr) VALUES(?, ?, ?, ?)`,
		hashToken(token), userID, time.Now().Add(mfaChallengeTTL).Unix(), remote)
	return token, err
}

// MFAChallengeUser returns the user of a live challenge with attempts
// left, without spending one (ClaimMFAChallenge does).
func MFAChallengeUser(ctx context.Context, q queryer, token string) (int64, error) {
	var userID int64
	err := q.QueryRowContext(ctx, `SELECT user_id FROM mfa_challenges WHERE token_hash = ? AND expires_at >= ? AND attempts < ?`,
		hashToken(token), time.Now().Unix(), mfaMaxAttempts).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrMFAChallengeExpired
	}
	return userID, err
}

// ClaimMFAChallenge spends one attempt of a live challenge and returns its
// user; the challenge dies after a few. The attempt is counted before the
// code is checked, in one statement, so concurrent requests with the same
// challenge cannot get more than its attempts; a success ends it
// (EndMFAChallenge).
func ClaimMFAChallenge(ctx context.Context, q queryer, token string) (int64, error) {
	var userID int64
	err := q.QueryRowContext(ctx, `UPDATE mfa_challenges SET attempts = attempts + 1
		WHERE token_hash = ? AND expires_at >= ? AND attempts < ? RETURNING user_id`,
		hashToken(token), time.Now().Unix(), mfaMaxAttempts).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrMFAChallengeExpired
	}
	return userID, err
}

// EndMFAChallenge removes a challenge after success.
func EndMFAChallenge(ctx context.Context, q queryer, token string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM mfa_challenges WHERE token_hash = ?`, hashToken(token))
	return err
}
