-- SPDX-License-Identifier: AGPL-3.0-only
-- Browsers an account signed in from (a long-lived cookie): signing in
-- again from one is exempt from the username's throttle, so whoever fails
-- with the username elsewhere cannot lock the owner out.

CREATE TABLE known_devices (
    token_hash TEXT PRIMARY KEY,           -- hex SHA-256 of the cookie token
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
) STRICT;
CREATE INDEX known_devices_user ON known_devices(user_id);
