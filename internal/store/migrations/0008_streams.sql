-- SPDX-License-Identifier: AGPL-3.0-only
-- Four-layer (TCP/UDP) proxies, kept like sites: the spec the panel wants,
-- the root helper keeping what it applied.

CREATE TABLE streams (
    code       TEXT PRIMARY KEY,
    spec       TEXT NOT NULL,              -- helperapi.StreamSpec as JSON
    state      TEXT NOT NULL,              -- applying | active | failed
    error_code TEXT NOT NULL DEFAULT '',
    last_job   TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
