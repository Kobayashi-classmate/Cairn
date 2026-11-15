-- SPDX-License-Identifier: AGPL-3.0-only
-- Web sites. The spec is what the panel wants; the root helper keeps what
-- it last applied. Domains are unique across sites.

CREATE TABLE sites (
    code       TEXT PRIMARY KEY,           -- names the directory and users
    type       TEXT NOT NULL,              -- static | php | proxy
    spec       TEXT NOT NULL,              -- helperapi.SiteSpec as JSON
    state      TEXT NOT NULL,              -- applying | active | failed
    error_code TEXT NOT NULL DEFAULT '',   -- of the last failed change
    last_job   TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE site_domains (
    domain TEXT PRIMARY KEY,
    code   TEXT NOT NULL REFERENCES sites(code) ON DELETE CASCADE
) STRICT;

CREATE INDEX site_domains_code ON site_domains (code);
