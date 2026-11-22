// SPDX-License-Identifier: AGPL-3.0-only
//
// Colours of the web terminal (interface spec, sections 2 and 3): the
// background, text, cursor and selection come from the panel's tokens, so
// they follow the light and dark mode; the 16 ANSI colours have a light and
// a dark set. Every one of them, as text on the terminal's background (the
// `--card` token), has a contrast of at least 4.5:1 (WCAG 2.2, computed
// 2026-11-21): on white the lowest is bright red at 4.83, on #141417 black
// at 4.58.
import type { ITheme } from '@xterm/xterm'

type Ansi = Pick<
  ITheme,
  | 'black'
  | 'red'
  | 'green'
  | 'yellow'
  | 'blue'
  | 'magenta'
  | 'cyan'
  | 'white'
  | 'brightBlack'
  | 'brightRed'
  | 'brightGreen'
  | 'brightYellow'
  | 'brightBlue'
  | 'brightMagenta'
  | 'brightCyan'
  | 'brightWhite'
>

/** On a light background "white" is a mid grey and the bright colours are darker shades, so they stay readable. */
const LIGHT_ANSI: Ansi = {
  black: '#18181b',
  red: '#b91c1c',
  green: '#15703a',
  yellow: '#8a5a00',
  blue: '#1d4ed8',
  magenta: '#7c3aed',
  cyan: '#0f766e',
  white: '#5b5b63',
  brightBlack: '#6b6b73',
  brightRed: '#dc2626',
  brightGreen: '#15803d',
  brightYellow: '#a16207',
  brightBlue: '#2563eb',
  brightMagenta: '#9333ea',
  brightCyan: '#0e7490',
  brightWhite: '#3f3f46',
}

/** On a dark background "black" is a grey that can still be read. */
const DARK_ANSI: Ansi = {
  black: '#7e7e88',
  red: '#f87171',
  green: '#4ade80',
  yellow: '#fbbf24',
  blue: '#60a5fa',
  magenta: '#c084fc',
  cyan: '#2dd4bf',
  white: '#d4d4d8',
  brightBlack: '#a1a1aa',
  brightRed: '#fca5a5',
  brightGreen: '#86efac',
  brightYellow: '#fde68a',
  brightBlue: '#93c5fd',
  brightMagenta: '#d8b4fe',
  brightCyan: '#5eead4',
  brightWhite: '#fafafa',
}

const FALLBACK = {
  light: { background: '#ffffff', foreground: '#18181b', primary: '#0f766e' },
  dark: { background: '#141417', foreground: '#f4f4f5', primary: '#2dd4bf' },
}

/** A colour token as the stylesheet defines it now ("#0f766e"). */
function token(name: string, fallback: string): string {
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return /^#[0-9a-f]{6}$/i.test(value) ? value : fallback
}

/** The terminal's colours for the mode the panel shows now. */
export function terminalTheme(dark: boolean): ITheme {
  const base = dark ? FALLBACK.dark : FALLBACK.light
  const background = token('--card', base.background)
  const foreground = token('--foreground', base.foreground)
  const primary = token('--primary', base.primary)
  return {
    ...(dark ? DARK_ANSI : LIGHT_ANSI),
    background,
    foreground,
    cursor: foreground,
    cursorAccent: background,
    // The accent colour at about 30 % (xterm takes #rrggbbaa).
    selectionBackground: `${primary}4d`,
    selectionInactiveBackground: `${primary}2e`,
    scrollbarSliderBackground: `${foreground}26`,
    scrollbarSliderHoverBackground: `${foreground}40`,
    scrollbarSliderActiveBackground: `${foreground}59`,
  }
}

/** The bundled monospace font first (interface spec, section 4). */
export const TERMINAL_FONT = "'JetBrains Mono Variable', 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
