-- SPDX-License-Identifier: AGPL-3.0-only
-- Each account's saved terminal commands, which the Terminal page pastes
-- into or runs in the terminal in front.

CREATE TABLE terminal_snippets (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    command    TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (user_id, name)
) STRICT;
