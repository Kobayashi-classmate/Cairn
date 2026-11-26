// SPDX-License-Identifier: AGPL-3.0-only
//
// The web terminal's colour schemes, chosen per account (PUT
// /me/terminal-scheme; the ids are in schemeIds.ts). "cairn", the default,
// is the panel's own palette (theme.ts): it
// follows the light and dark mode and keeps every colour readable on the
// terminal background. The others are well-known schemes with their
// authors' colours, so a few of them may be hard to read on their own
// background; the menu says so. A scheme with a light and a dark variant
// follows the panel's mode, Dracula and Nord have a dark one only. Log
// views keep the panel's palette: they sit on the page's own background.
//
// Colours as their authors publish them (read 2026-11-25):
// - Solarized: altercation/solarized, xresources/solarized (MIT). The light
//   variant swaps background and text only and keeps the 16 colours, as
//   Solarized's own terminal presets and GNOME Terminal do.
// - One Half: sonph/onehalf, iterm/OneHalf*.itermcolors (MIT). The dark
//   variant's bright black is the background colour there; Windows
//   Terminal's grey (#5a6374, MIT) is used instead so that dim text shows.
// - GitHub: primer/github-vscode-theme, src/theme.js with
//   @primer/primitives 7.10.0 (MIT), "Light Default" and "Dark Default".
// - Gruvbox: morhetz/gruvbox, colors/gruvbox.vim, medium contrast (MIT).
// - Catppuccin: catppuccin/windows-terminal, latte.json and mocha.json (MIT).
// - Tango: the Tango Desktop Project palette (public domain) with GNOME
//   Terminal's "Tango light" and "Tango dark" text and background.
// - xterm: ThomasDickey/xterm-snapshots, XTerm-col.ad (X11): black on
//   white, and white on black as xterm's reverse video.
// - Dracula: dracula/windows-terminal, dracula.json (MIT).
// - Nord: nordtheme/xresources, src/nord, with the selection of
//   nordtheme/alacritty (MIT).
// A scheme that defines no cursor uses the text colour, and one without a
// selection colour the text colour at a quarter.
import type { ITheme } from '@xterm/xterm'
import { TERMINAL_SCHEME_IDS, type TerminalSchemeId } from './schemeIds'
import { ANSI_ORDER, terminalTheme } from './theme'

interface Variant {
  background: string
  foreground: string
  cursor?: string
  /** #rrggbb or #rrggbbaa. */
  selection?: string
  /** Black, red, green, yellow, blue, magenta, cyan, white, then the bright ones. */
  ansi: readonly string[]
}

/** The scheme's own name, the same in every language, and its variants: both, or a dark one only. */
type Scheme = { name: string; dark: Variant } & ({ light: Variant } | { light?: undefined })

const SOLARIZED_ANSI = [
  '#073642', '#dc322f', '#859900', '#b58900', '#268bd2', '#d33682', '#2aa198', '#eee8d5',
  '#002b36', '#cb4b16', '#586e75', '#657b83', '#839496', '#6c71c4', '#93a1a1', '#fdf6e3',
]
const TANGO_ANSI = [
  '#2e3436', '#cc0000', '#4e9a06', '#c4a000', '#3465a4', '#75507b', '#06989a', '#d3d7cf',
  '#555753', '#ef2929', '#8ae234', '#fce94f', '#729fcf', '#ad7fa8', '#34e2e2', '#eeeeec',
]
const XTERM_ANSI = [
  '#000000', '#cd0000', '#00cd00', '#cdcd00', '#0000ee', '#cd00cd', '#00cdcd', '#e5e5e5',
  '#7f7f7f', '#ff0000', '#00ff00', '#ffff00', '#5c5cff', '#ff00ff', '#00ffff', '#ffffff',
]

const SCHEMES: Record<Exclude<TerminalSchemeId, 'cairn'>, Scheme> = {
  solarized: {
    name: 'Solarized',
    light: { background: '#fdf6e3', foreground: '#657b83', cursor: '#586e75', ansi: SOLARIZED_ANSI },
    dark: { background: '#002b36', foreground: '#839496', cursor: '#93a1a1', ansi: SOLARIZED_ANSI },
  },
  'one-half': {
    name: 'One Half',
    light: {
      background: '#fafafa',
      foreground: '#383a42',
      selection: '#bfceff',
      ansi: [
        '#383a42', '#e45649', '#50a14f', '#c18401', '#0184bc', '#a626a4', '#0997b3', '#fafafa',
        '#4f525e', '#e06c75', '#98c379', '#e5c07b', '#61afef', '#c678dd', '#56b6c2', '#ffffff',
      ],
    },
    dark: {
      background: '#282c34',
      foreground: '#dcdfe4',
      cursor: '#a3b3cc',
      selection: '#474e5d',
      ansi: [
        '#282c34', '#e06c75', '#98c379', '#e5c07b', '#61afef', '#c678dd', '#56b6c2', '#dcdfe4',
        '#5a6374', '#e06c75', '#98c379', '#e5c07b', '#61afef', '#c678dd', '#56b6c2', '#dcdfe4',
      ],
    },
  },
  github: {
    name: 'GitHub',
    light: {
      background: '#ffffff',
      foreground: '#1f2328',
      selection: '#0969da33',
      ansi: [
        '#24292f', '#cf222e', '#116329', '#4d2d00', '#0969da', '#8250df', '#1b7c83', '#6e7781',
        '#57606a', '#a40e26', '#1a7f37', '#633c01', '#218bff', '#a475f9', '#3192aa', '#8c959f',
      ],
    },
    dark: {
      background: '#0d1117',
      foreground: '#e6edf3',
      selection: '#2f81f733',
      ansi: [
        '#484f58', '#ff7b72', '#3fb950', '#d29922', '#58a6ff', '#bc8cff', '#39c5cf', '#b1bac4',
        '#6e7681', '#ffa198', '#56d364', '#e3b341', '#79c0ff', '#d2a8ff', '#56d4dd', '#ffffff',
      ],
    },
  },
  gruvbox: {
    name: 'Gruvbox',
    light: {
      background: '#fbf1c7',
      foreground: '#3c3836',
      ansi: [
        '#fbf1c7', '#cc241d', '#98971a', '#d79921', '#458588', '#b16286', '#689d6a', '#7c6f64',
        '#928374', '#9d0006', '#79740e', '#b57614', '#076678', '#8f3f71', '#427b58', '#3c3836',
      ],
    },
    dark: {
      background: '#282828',
      foreground: '#ebdbb2',
      ansi: [
        '#282828', '#cc241d', '#98971a', '#d79921', '#458588', '#b16286', '#689d6a', '#a89984',
        '#928374', '#fb4934', '#b8bb26', '#fabd2f', '#83a598', '#d3869b', '#8ec07c', '#ebdbb2',
      ],
    },
  },
  catppuccin: {
    name: 'Catppuccin',
    light: {
      background: '#eff1f5',
      foreground: '#4c4f69',
      cursor: '#dc8a78',
      selection: '#acb0be',
      ansi: [
        '#5c5f77', '#d20f39', '#40a02b', '#df8e1d', '#1e66f5', '#ea76cb', '#179299', '#acb0be',
        '#acb0be', '#d20f39', '#40a02b', '#df8e1d', '#1e66f5', '#ea76cb', '#179299', '#bcc0cc',
      ],
    },
    dark: {
      background: '#1e1e2e',
      foreground: '#cdd6f4',
      cursor: '#f5e0dc',
      selection: '#585b70',
      ansi: [
        '#45475a', '#f38ba8', '#a6e3a1', '#f9e2af', '#89b4fa', '#f5c2e7', '#94e2d5', '#bac2de',
        '#585b70', '#f38ba8', '#a6e3a1', '#f9e2af', '#89b4fa', '#f5c2e7', '#94e2d5', '#a6adc8',
      ],
    },
  },
  tango: {
    name: 'Tango',
    light: { background: '#eeeeec', foreground: '#2e3436', ansi: TANGO_ANSI },
    dark: { background: '#2e3436', foreground: '#d3d7cf', ansi: TANGO_ANSI },
  },
  xterm: {
    name: 'xterm',
    light: { background: '#ffffff', foreground: '#000000', ansi: XTERM_ANSI },
    dark: { background: '#000000', foreground: '#ffffff', ansi: XTERM_ANSI },
  },
  dracula: {
    name: 'Dracula',
    dark: {
      background: '#282a36',
      foreground: '#f8f8f2',
      selection: '#44475a',
      ansi: [
        '#21222c', '#ff5555', '#50fa7b', '#f1fa8c', '#bd93f9', '#ff79c6', '#8be9fd', '#f8f8f2',
        '#6272a4', '#ff6e6e', '#69ff94', '#ffffa5', '#d6acff', '#ff92df', '#a4ffff', '#ffffff',
      ],
    },
  },
  nord: {
    name: 'Nord',
    dark: {
      background: '#2e3440',
      foreground: '#d8dee9',
      selection: '#4c566a',
      ansi: [
        '#3b4252', '#bf616a', '#a3be8c', '#ebcb8b', '#81a1c1', '#b48ead', '#88c0d0', '#e5e9f0',
        '#4c566a', '#bf616a', '#a3be8c', '#ebcb8b', '#81a1c1', '#b48ead', '#8fbcbb', '#eceff4',
      ],
    },
  },
}

/** A scheme as the menu lists it. */
export interface SchemeInfo {
  id: TerminalSchemeId
  /** Empty for "cairn", whose name is translated. */
  name: string
  /** Whether the scheme follows the panel's light and dark mode. */
  followsMode: boolean
}

export const TERMINAL_SCHEMES: readonly SchemeInfo[] = TERMINAL_SCHEME_IDS.map((id) => {
  if (id === 'cairn') return { id, name: '', followsMode: true }
  const scheme = SCHEMES[id]
  return { id, name: scheme.name, followsMode: scheme.light !== undefined }
})

function variantOf(id: Exclude<TerminalSchemeId, 'cairn'>, dark: boolean): Variant {
  const scheme = SCHEMES[id]
  // A scheme with a dark variant only shows it in either mode.
  return dark ? scheme.dark : (scheme.light ?? scheme.dark)
}

/** The terminal's colours for a scheme in the panel's current mode. */
export function schemeTheme(id: TerminalSchemeId, dark: boolean): ITheme {
  if (id === 'cairn') return terminalTheme(dark)
  const v = variantOf(id, dark)
  const theme: ITheme = {
    background: v.background,
    foreground: v.foreground,
    cursor: v.cursor ?? v.foreground,
    cursorAccent: v.background,
    selectionBackground: v.selection ?? `${v.foreground}40`,
    scrollbarSliderBackground: `${v.foreground}26`,
    scrollbarSliderHoverBackground: `${v.foreground}40`,
    scrollbarSliderActiveBackground: `${v.foreground}59`,
  }
  ANSI_ORDER.forEach((name, index) => {
    theme[name] = v.ansi[index]
  })
  return theme
}
