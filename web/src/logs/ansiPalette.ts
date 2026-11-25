// SPDX-License-Identifier: AGPL-3.0-only
//
// The terminal's 16 colours as CSS variables for log views, in the mode the
// panel shows: --ansi-0 … --ansi-15 for text, and --ansi-on-N, the text
// colour that reads best on colour N as a background. The colours are the
// web terminal's (terminal/theme.ts), checked as text on --card: log text
// sits on the card background.
import { computed, type ComputedRef } from 'vue'
import { useThemeStore } from '@/stores/theme'
import { ansiColors } from '@/terminal/theme'

/** Text on a coloured background: the darkest and the lightest of the palettes. */
const ON_DARK = '#18181b'
const ON_LIGHT = '#fafafa'

/** Relative luminance (WCAG 2.2) of "#rrggbb". */
function luminance(color: string): number {
  const n = Number.parseInt(color.slice(1), 16)
  const channel = (c: number) => {
    const v = c / 255
    return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel((n >> 16) & 255) + 0.7152 * channel((n >> 8) & 255) + 0.0722 * channel(n & 255)
}

function contrast(a: string, b: string): number {
  const [x, y] = [luminance(a), luminance(b)]
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05)
}

function paletteVars(dark: boolean): Record<string, string> {
  const vars: Record<string, string> = {}
  ansiColors(dark).forEach((color, index) => {
    vars[`--ansi-${index}`] = color
    vars[`--ansi-on-${index}`] = contrast(color, ON_DARK) >= contrast(color, ON_LIGHT) ? ON_DARK : ON_LIGHT
  })
  return vars
}

const VARS = { light: paletteVars(false), dark: paletteVars(true) }

/** The palette variables for the mode the panel shows, for a `style` binding. */
export function useAnsiPalette(): ComputedRef<Record<string, string>> {
  const theme = useThemeStore()
  return computed(() => (theme.isDark ? VARS.dark : VARS.light))
}
