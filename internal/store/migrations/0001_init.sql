-- SPDX-License-Identifier: AGPL-3.0-only
-- Initial schema: users, sessions, setup tokens, login throttling, audit log.
-- Timestamps are Unix seconds.

CREATE TABLE users (
	id            INTEGER PRIMARY KEY,
	username      TEXT    NOT NULL UNIQUE,
	password_hash TEXT    NOT NULL,           -- argon2id PHC string
	locale        TEXT    NOT NULL DEFAULT '', -- empty: use the panel default
	disabled      INTEGER NOT NULL DEFAULT 0,
	created_at    INTEGER NOT NULL,
	updated_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
	id           TEXT    PRIMARY KEY,          -- hex SHA-256 of the cookie token
	user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at   INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL,
	expires_at   INTEGER NOT NULL,             -- absolute expiry
	remote_addr  TEXT    NOT NULL DEFAULT '',
	user_agent   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_id ON sessions(user_id);

CREATE TABLE setup_tokens (
	token_hash TEXT    PRIMARY KEY,            -- hex SHA-256 of the token
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);

CREATE TABLE login_failures (
	key          TEXT    PRIMARY KEY,          -- "ip:<addr>" or "user:<name>"
	failures     INTEGER NOT NULL,
	locked_until INTEGER NOT NULL DEFAULT 0,
	updated_at   INTEGER NOT NULL
);

-- Structured audit entries; rendered in the viewer's language at read time.
CREATE TABLE audit_log (
	id          INTEGER PRIMARY KEY,
	ts          INTEGER NOT NULL,
	user_id     INTEGER,
	username    TEXT    NOT NULL DEFAULT '',
	action      TEXT    NOT NULL,              -- e.g. "auth.login"
	params      TEXT    NOT NULL DEFAULT '{}', -- JSON, never secrets
	result      TEXT    NOT NULL,              -- "ok" or an error code
	remote_addr TEXT    NOT NULL DEFAULT '',
	source      TEXT    NOT NULL DEFAULT 'web'
);
CREATE INDEX audit_log_ts ON audit_log(ts);
