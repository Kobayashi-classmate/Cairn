-- SPDX-License-Identifier: AGPL-3.0-only
-- Databases and database users the panel manages. The engines hold the
-- truth about what exists; the panel keeps who owns what, the access it
-- gave each user, and their passwords (sealed with the master key) so
-- they can be shown again.

CREATE TABLE db_databases (
    engine     TEXT NOT NULL,             -- mysql | postgresql-<line>
    name       TEXT NOT NULL,
    owner      TEXT NOT NULL DEFAULT '',  -- a db_users name, or ''
    created_at INTEGER NOT NULL,
    PRIMARY KEY (engine, name)
) STRICT;

CREATE TABLE db_users (
    engine     TEXT NOT NULL,
    name       TEXT NOT NULL,
    password   TEXT NOT NULL DEFAULT '',  -- sealed; '' when the panel does not know it
    hosts      TEXT NOT NULL,             -- JSON array
    grants     TEXT NOT NULL,             -- JSON array of {database, level}
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (engine, name)
) STRICT;
