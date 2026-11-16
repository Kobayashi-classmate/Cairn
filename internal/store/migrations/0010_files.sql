-- SPDX-License-Identifier: AGPL-3.0-only
-- File manager: resumable uploads (tus) in progress, kept in the panel's
-- uploads/ directory until the root helper moves them into place, and
-- each user's favourite directories.

CREATE TABLE file_uploads (
    id         TEXT PRIMARY KEY,           -- 32 hex digits; uploads/<id>.part
    user_id    INTEGER NOT NULL,
    root       TEXT NOT NULL,
    dir        TEXT NOT NULL,
    name       TEXT NOT NULL,
    size       INTEGER NOT NULL,
    offset     INTEGER NOT NULL DEFAULT 0,
    overwrite  INTEGER NOT NULL DEFAULT 0,
    job        TEXT NOT NULL DEFAULT '',   -- the job placing it, once complete
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
) STRICT;

CREATE TABLE file_favorites (
    user_id    INTEGER NOT NULL,
    root       TEXT NOT NULL,
    path       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, root, path)
) STRICT;
