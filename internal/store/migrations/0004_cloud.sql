-- SPDX-License-Identifier: AGPL-3.0-only
-- Official messages pulled from the Cairn cloud, per-user read state, and a
-- small key-value table for cloud bookkeeping (last feed timestamps, the last
-- update job already audited).

CREATE TABLE announcements (
	id           TEXT    PRIMARY KEY,
	level        TEXT    NOT NULL,
	category     TEXT    NOT NULL,
	published_at INTEGER NOT NULL,
	expires_at   INTEGER NOT NULL DEFAULT 0,
	title        TEXT    NOT NULL,   -- JSON {locale: text}
	body         TEXT    NOT NULL,   -- JSON {locale: text}
	link         TEXT    NOT NULL DEFAULT '',
	received_at  INTEGER NOT NULL
);

CREATE TABLE announcement_reads (
	user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	announcement_id TEXT    NOT NULL REFERENCES announcements(id) ON DELETE CASCADE,
	read_at         INTEGER NOT NULL,
	PRIMARY KEY (user_id, announcement_id)
);

CREATE TABLE kv (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
