-- SPDX-License-Identifier: AGPL-3.0-only
-- Each account's colour scheme: light, dark, or follow the system.
ALTER TABLE users ADD COLUMN theme TEXT NOT NULL DEFAULT 'system';
