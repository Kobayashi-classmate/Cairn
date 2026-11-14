-- SPDX-License-Identifier: AGPL-3.0-only
-- Notification channels with a durable outbox, and API tokens.

CREATE TABLE notify_channels (
	id         INTEGER PRIMARY KEY,
	type       TEXT    NOT NULL,              -- email | webhook
	name       TEXT    NOT NULL,
	locale     TEXT    NOT NULL,              -- language of the messages
	enabled    INTEGER NOT NULL DEFAULT 1,
	events     TEXT    NOT NULL,              -- JSON list of event-type prefixes
	min_level  TEXT    NOT NULL,              -- info | warning | critical
	config     TEXT    NOT NULL DEFAULT '',   -- sealed JSON (contains credentials)
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE TABLE notify_outbox (
	id              INTEGER PRIMARY KEY,
	channel_id      INTEGER NOT NULL REFERENCES notify_channels(id) ON DELETE CASCADE,
	event_type      TEXT    NOT NULL,
	level           TEXT    NOT NULL,
	params          TEXT    NOT NULL DEFAULT '{}', -- JSON, never secrets
	dedupe_key      TEXT    NOT NULL DEFAULT '',
	status          TEXT    NOT NULL,              -- pending | sent | failed
	attempts        INTEGER NOT NULL DEFAULT 0,
	next_attempt_at INTEGER NOT NULL,
	last_error      TEXT    NOT NULL DEFAULT '',
	created_at      INTEGER NOT NULL,
	sent_at         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX notify_outbox_due ON notify_outbox(status, next_attempt_at);
CREATE INDEX notify_outbox_dedupe ON notify_outbox(channel_id, dedupe_key, created_at);

CREATE TABLE api_tokens (
	id           INTEGER PRIMARY KEY,
	user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name         TEXT    NOT NULL,
	token_hash   TEXT    NOT NULL UNIQUE,       -- hex SHA-256; the token is shown once
	prefix       TEXT    NOT NULL,              -- first characters, to recognize it
	permissions  TEXT    NOT NULL,              -- JSON list, subset of the user's role
	allowed_ips  TEXT    NOT NULL DEFAULT '[]', -- JSON list of IPs/CIDRs; empty = any
	expires_at   INTEGER NOT NULL DEFAULT 0,    -- 0 = never
	last_used_at INTEGER NOT NULL DEFAULT 0,
	created_at   INTEGER NOT NULL
);
CREATE INDEX api_tokens_user ON api_tokens(user_id);
