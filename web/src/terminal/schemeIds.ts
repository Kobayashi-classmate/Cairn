// SPDX-License-Identifier: AGPL-3.0-only
//
// The ids of the web terminal's colour schemes, apart from their colours
// (schemes.ts) so that the session store does not pull those into the main
// bundle. They must match auth.TerminalSchemes on the server.

export const TERMINAL_SCHEME_IDS = [
  'cairn',
  'solarized',
  'one-half',
  'github',
  'gruvbox',
  'catppuccin',
  'tango',
  'xterm',
  'dracula',
  'nord',
] as const

export type TerminalSchemeId = (typeof TERMINAL_SCHEME_IDS)[number]

/** The panel's own palette, which follows the light and dark mode. */
export const DEFAULT_TERMINAL_SCHEME: TerminalSchemeId = 'cairn'

export function isTerminalSchemeId(value: unknown): value is TerminalSchemeId {
  return typeof value === 'string' && (TERMINAL_SCHEME_IDS as readonly string[]).includes(value)
}
