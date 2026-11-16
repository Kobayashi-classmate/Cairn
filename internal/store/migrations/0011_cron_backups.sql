-- SPDX-License-Identifier: AGPL-3.0-only
-- Scheduled tasks and backups, kept like sites: what the panel wants, the
-- root helper keeping what it applied (the plans with their storage
-- credentials, in its root-only state).

CREATE TABLE cron_tasks (
    id           TEXT PRIMARY KEY,          -- t + 15 hex digits
    name         TEXT NOT NULL,
    spec         TEXT NOT NULL,             -- helperapi.CronSpec as JSON
    state        TEXT NOT NULL,             -- applying | active | failed
    error_code   TEXT NOT NULL DEFAULT '',
    last_job     TEXT NOT NULL DEFAULT '',
    notified_run INTEGER NOT NULL DEFAULT 0, -- start of the last failed run notified
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;

CREATE TABLE backup_storages (
    id         TEXT PRIMARY KEY,            -- s + 15 hex digits
    name       TEXT NOT NULL,
    config     TEXT NOT NULL,               -- helperapi.S3Storage without the secret key
    secret     TEXT NOT NULL,               -- the secret key, sealed
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE backup_plans (
    id           TEXT PRIMARY KEY,          -- p + 15 hex digits
    name         TEXT NOT NULL,
    spec         TEXT NOT NULL,             -- helperapi.BackupPlan without the storage
    storage_id   TEXT NOT NULL DEFAULT '',
    notified_run INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;
