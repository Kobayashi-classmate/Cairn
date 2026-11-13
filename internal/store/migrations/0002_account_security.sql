-- SPDX-License-Identifier: AGPL-3.0-only
-- Roles, two-step sign-in (TOTP + recovery codes) and step-up elevation.

ALTER TABLE users ADD COLUMN role                 TEXT    NOT NULL DEFAULT 'admin';
ALTER TABLE users ADD COLUMN last_login_at        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN totp_secret          TEXT    NOT NULL DEFAULT ''; -- sealed, see internal/secrets
ALTER TABLE users ADD COLUMN totp_enabled         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN totp_last_step       INTEGER NOT NULL DEFAULT 0; -- replay protection
ALTER TABLE users ADD COLUMN totp_pending         TEXT    NOT NULL DEFAULT ''; -- sealed, during enrollment
ALTER TABLE users ADD COLUMN totp_pending_expires INTEGER NOT NULL DEFAULT 0;

-- Re-authentication window for sensitive operations.
ALTER TABLE sessions ADD COLUMN elevated_until INTEGER NOT NULL DEFAULT 0;

CREATE TABLE recovery_codes (
	user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	code_hash TEXT    NOT NULL,                -- hex SHA-256 of the normalized code
	used_at   INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (user_id, code_hash)
);

-- Pending second steps of a sign-in: the password was right, the code is due.
CREATE TABLE mfa_challenges (
	token_hash  TEXT    PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at  INTEGER NOT NULL,
	attempts    INTEGER NOT NULL DEFAULT 0,
	remote_addr TEXT    NOT NULL DEFAULT ''
);
