-- SPDX-License-Identifier: AGPL-3.0-only
-- Certificates and the DNS provider accounts DNS-01 validation uses. Keys
-- stay with the root helper; DNS credentials are sealed with the master key.

CREATE TABLE dns_accounts (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    provider   TEXT NOT NULL,
    secret     TEXT NOT NULL,            -- sealed JSON of the credential fields
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE certificates (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    source           TEXT NOT NULL,              -- acme | upload
    domains          TEXT NOT NULL,              -- JSON array
    ca               TEXT NOT NULL DEFAULT '',   -- acme: CA name
    challenge        TEXT NOT NULL DEFAULT '',   -- acme: http-01 | dns-01
    dns_account      TEXT NOT NULL DEFAULT '',   -- acme dns-01: dns_accounts.id
    key_type         TEXT NOT NULL DEFAULT '',
    auto_renew       INTEGER NOT NULL DEFAULT 1,
    state            TEXT NOT NULL,              -- issuing | valid | failed
    error_code       TEXT NOT NULL DEFAULT '',
    error_params     TEXT NOT NULL DEFAULT '{}',
    not_before       INTEGER NOT NULL DEFAULT 0,
    not_after        INTEGER NOT NULL DEFAULT 0,
    issuer           TEXT NOT NULL DEFAULT '',
    serial           TEXT NOT NULL DEFAULT '',
    fingerprint      TEXT NOT NULL DEFAULT '',
    last_job         TEXT NOT NULL DEFAULT '',
    renew_attempt_at INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
) STRICT;
