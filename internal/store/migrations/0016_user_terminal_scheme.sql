-- SPDX-License-Identifier: AGPL-3.0-only
-- Each account's colour scheme for the web terminal; "cairn" is the
-- panel's own, which follows the light and dark mode.
ALTER TABLE users ADD COLUMN terminal_scheme TEXT NOT NULL DEFAULT 'cairn';
