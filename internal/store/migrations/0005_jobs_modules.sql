-- SPDX-License-Identifier: AGPL-3.0-only
-- Background jobs and the on/off state of optional modules.

CREATE TABLE jobs (
    id                TEXT PRIMARY KEY,
    kind              TEXT NOT NULL,
    params            TEXT NOT NULL DEFAULT '{}',
    permission        TEXT NOT NULL,          -- needed to see the job
    manage_permission TEXT NOT NULL,          -- needed to cancel it
    state             TEXT NOT NULL,          -- queued | running | succeeded | failed | canceled | interrupted
    progress          REAL,                   -- 0..1, NULL when unknown
    error_code        TEXT NOT NULL DEFAULT '',
    error_params      TEXT NOT NULL DEFAULT '{}',
    op_id             TEXT NOT NULL DEFAULT '', -- helper operation being followed
    user_id           INTEGER NOT NULL DEFAULT 0,
    username          TEXT NOT NULL DEFAULT '',
    source            TEXT NOT NULL DEFAULT '',
    remote            TEXT NOT NULL DEFAULT '',
    created_at        INTEGER NOT NULL,
    started_at        INTEGER NOT NULL DEFAULT 0,
    finished_at       INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX jobs_created ON jobs (created_at DESC);

CREATE TABLE module_state (
    id      TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL
) STRICT;
