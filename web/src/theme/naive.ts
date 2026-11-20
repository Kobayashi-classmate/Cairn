// SPDX-License-Identifier: AGPL-3.0-only
//
// Pages not yet moved to the new components still use Naive UI. These
// overrides give them the design tokens of styles/main.css (teal accent,
// status colours, surfaces, fonts) so they blend into the new shell in both
// light and dark mode. Colours are read from the CSS variables on <html>, so
// main.css stays the only place they are defined.
import type { GlobalThemeOverrides } from 'naive-ui'

const HEX = /^#[0-9a-f]{6}$/i

function readTokens(): (name: string) => string {
  const styles = getComputedStyle(document.documentElement)
  return (name) => styles.getPropertyValue(`--${name}`).trim()
}

/** Mixes two #rrggbb colours; `weight` is the share of `other`. */
function mix(color: string, other: string, weight: number): string {
  if (!HEX.test(color) || !HEX.test(other)) return color
  const channel = (hex: string, i: number) => Number.parseInt(hex.slice(1 + i * 2, 3 + i * 2), 16)
  let out = '#'
  for (let i = 0; i < 3; i++) {
    const value = Math.round(channel(color, i) * (1 - weight) + channel(other, i) * weight)
    out += value.toString(16).padStart(2, '0')
  }
  return out
}

interface ColorSet {
  color: string
  hover: string
  pressed: string
}

/** Hover is lighter in dark mode and darker in light mode; pressed goes a step further. */
function shades(color: string, dark: boolean): ColorSet {
  return dark
    ? { color, hover: mix(color, '#ffffff', 0.18), pressed: mix(color, '#000000', 0.15) }
    : { color, hover: mix(color, '#000000', 0.12), pressed: mix(color, '#000000', 0.24) }
}

/** Naive UI theme overrides for the mode currently applied to <html>. */
export function naiveThemeOverrides(dark: boolean): GlobalThemeOverrides {
  const token = readTokens()
  // Without the stylesheet (should not happen) keep Naive UI's own theme.
  if (!HEX.test(token('primary'))) return {}

  const primary = shades(token('primary'), dark)
  const success = shades(token('success'), dark)
  const warning = shades(token('warning'), dark)
  const error = shades(token('destructive'), dark)
  const onPrimary = token('primary-foreground')
  const onError = token('destructive-foreground')
  const sans = token('font-sans')
  const mono = token('font-mono')

  return {
    common: {
      primaryColor: primary.color,
      primaryColorHover: primary.hover,
      primaryColorPressed: primary.pressed,
      primaryColorSuppl: primary.hover,
      infoColor: primary.color,
      infoColorHover: primary.hover,
      infoColorPressed: primary.pressed,
      infoColorSuppl: primary.hover,
      successColor: success.color,
      successColorHover: success.hover,
      successColorPressed: success.pressed,
      successColorSuppl: success.hover,
      warningColor: warning.color,
      warningColorHover: warning.hover,
      warningColorPressed: warning.pressed,
      warningColorSuppl: warning.hover,
      errorColor: error.color,
      errorColorHover: error.hover,
      errorColorPressed: error.pressed,
      errorColorSuppl: error.hover,
      bodyColor: token('background'),
      cardColor: token('card'),
      modalColor: token('card'),
      popoverColor: token('popover'),
      tableColor: token('card'),
      tableHeaderColor: token('muted'),
      inputColor: token('card'),
      actionColor: token('muted'),
      hoverColor: token('accent'),
      borderColor: token('input'),
      dividerColor: token('border'),
      textColorBase: token('foreground'),
      textColor1: token('foreground'),
      textColor2: token('foreground'),
      textColor3: token('muted-foreground'),
      placeholderColor: token('muted-foreground'),
      borderRadius: '8px',
      borderRadiusSmall: '6px',
      ...(sans ? { fontFamily: sans } : {}),
      ...(mono ? { fontFamilyMono: mono } : {}),
    },
    Button: {
      textColorPrimary: onPrimary,
      textColorHoverPrimary: onPrimary,
      textColorPressedPrimary: onPrimary,
      textColorFocusPrimary: onPrimary,
      textColorInfo: onPrimary,
      textColorHoverInfo: onPrimary,
      textColorPressedInfo: onPrimary,
      textColorFocusInfo: onPrimary,
      textColorError: onError,
      textColorHoverError: onError,
      textColorPressedError: onError,
      textColorFocusError: onError,
    },
    Card: {
      borderRadius: '10px',
      borderColor: token('border'),
    },
    DataTable: {
      borderColor: token('border'),
      thColor: token('muted'),
      thTextColor: token('muted-foreground'),
    },
  }
}
