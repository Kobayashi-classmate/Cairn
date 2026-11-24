-- SPDX-License-Identifier: AGPL-3.0-only
-- Browsers send a host's cookies to every port of it, so a website on the
-- panel's host name sees the panel's cookies. A cookie alone is therefore
-- never enough: a session also has a key, which the page keeps in storage
-- only its own origin (port included) can read and sends in a header, and
-- a known device has one the same way. One-time tickets stand in for the
-- session key where a request cannot carry headers (WebSocket, downloads).

-- Sessions and known devices from before have no key and could never be
-- used again: everyone signs in once more.
DELETE FROM sessions;
DELETE FROM known_devices;
ALTER TABLE sessions      ADD COLUMN key_hash TEXT NOT NULL DEFAULT ''; -- hex SHA-256 of the session key
ALTER TABLE known_devices ADD COLUMN key_hash TEXT NOT NULL DEFAULT ''; -- hex SHA-256 of the device key

CREATE TABLE session_tickets (
    token_hash TEXT PRIMARY KEY,           -- hex SHA-256 of the ticket
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    target     TEXT NOT NULL,              -- hex SHA-256 of the request the ticket is for
    expires_at INTEGER NOT NULL
) STRICT;
CREATE INDEX session_tickets_session ON session_tickets(session_id);
