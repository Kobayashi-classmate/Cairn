// SPDX-License-Identifier: AGPL-3.0-only
//
// ANSI escape sequences in log text (ECMA-48). The colours and attributes
// of SGR sequences ("\x1b[1;31m") become styled segments; every other
// control sequence (cursor movement, OSC titles and links, ...) and every
// control character but the tab is dropped, so what is left is the text a
// terminal would have printed. Segments are rendered as text, never as
// HTML, and a colour is only ever a palette index (see baseColor): nothing
// from the log reaches a style sheet.

/** A colour of the 256-colour palette (0-15 the terminal's own), or "#rrggbb". */
export type AnsiColor = number | string

/** Attributes set by SGR; a missing one is the default. */
export interface AnsiStyle {
  readonly fg?: AnsiColor
  readonly bg?: AnsiColor
  readonly bold?: true
  readonly dim?: true
  readonly italic?: true
  readonly underline?: true
}

export interface AnsiSegment {
  readonly text: string
  /** null: the surrounding text's own style. */
  readonly style: AnsiStyle | null
}

export interface AnsiLine {
  readonly segments: readonly AnsiSegment[]
  /** The style at the end of the line; SGR state carries over to the next one. */
  readonly end: AnsiStyle | null
}

/**
 * One control sequence or character per match:
 * - CSI: ESC [ (or U+009B), parameters, intermediates and a final byte,
 *   which may be missing at the end of a line (groups 1 and 2);
 * - OSC, DCS, SOS, PM and APC strings, up to BEL or ST or the end of the line;
 * - any other escape sequence;
 * - a single C0 or C1 control character other than tab and line feed.
 */
// eslint-disable-next-line no-control-regex
const CONTROL = /(?:\x1b\[|\x9b)([0-?]*)[ -/]*([@-~]?)|(?:\x1b[\]PX^_]|[\x90\x98\x9d-\x9f])[^\x07\x1b\x9c\n]*(?:\x07|\x1b\\|\x9c)?|\x1b[ -/]*[0-~]?|[\x00-\x08\x0b-\x1f\x7f-\x9f]/g

/** Whether text has anything for CONTROL to match. */
// eslint-disable-next-line no-control-regex
const HAS_CONTROL = /[\x00-\x08\x0b-\x1f\x7f-\x9f]/

export function hasControl(text: string): boolean {
  return HAS_CONTROL.test(text)
}

/** The text without escape sequences and control characters (tab and line feed stay). */
export function stripAnsi(text: string): string {
  return hasControl(text) ? text.replace(CONTROL, '') : text
}

/** SGR state while a line is read. */
interface State {
  fg?: AnsiColor
  bg?: AnsiColor
  bold: boolean
  dim: boolean
  italic: boolean
  underline: boolean
}

function stateOf(style: AnsiStyle | null): State {
  return {
    fg: style?.fg,
    bg: style?.bg,
    bold: !!style?.bold,
    dim: !!style?.dim,
    italic: !!style?.italic,
    underline: !!style?.underline,
  }
}

function styleOf(s: State): AnsiStyle | null {
  if (s.fg === undefined && s.bg === undefined && !s.bold && !s.dim && !s.italic && !s.underline) return null
  const style: { -readonly [K in keyof AnsiStyle]: AnsiStyle[K] } = {}
  if (s.fg !== undefined) style.fg = s.fg
  if (s.bg !== undefined) style.bg = s.bg
  if (s.bold) style.bold = true
  if (s.dim) style.dim = true
  if (s.italic) style.italic = true
  if (s.underline) style.underline = true
  return style
}

/** A key that is equal for equal styles. */
export function styleKey(style: AnsiStyle | null): string {
  if (!style) return ''
  return `${style.fg ?? ''}/${style.bg ?? ''}/${style.bold ? 'b' : ''}${style.dim ? 'd' : ''}${style.italic ? 'i' : ''}${style.underline ? 'u' : ''}`
}

/** A parameter as a number; an empty one is 0, as in a terminal. */
function num(param: string | undefined): number {
  if (!param) return 0
  return /^\d{1,3}$/.test(param) ? Number(param) : -1
}

function byte(param: string | undefined): number | undefined {
  const n = num(param)
  return n >= 0 && n <= 255 ? n : undefined
}

function hex(r: number, g: number, b: number): string {
  return `#${((1 << 24) | (r << 16) | (g << 8) | b).toString(16).slice(1)}`
}

/** A 38/48 colour from its arguments ("5;n" or "2;r;g;b"), with how many it used; undefined when invalid. */
function extended(args: readonly (string | undefined)[], at: number): { color: AnsiColor | undefined; used: number } {
  const kind = num(args[at])
  if (kind === 5) return { color: byte(args[at + 1]), used: 2 }
  if (kind === 2) {
    const [r, g, b] = [byte(args[at + 1]), byte(args[at + 2]), byte(args[at + 3])]
    return { color: r === undefined || g === undefined || b === undefined ? undefined : hex(r, g, b), used: 4 }
  }
  return { color: undefined, used: 1 }
}

/** A colour in the colon form ("38:5:n", "38:2::r:g:b", "38:2:r:g:b"). */
function extendedColon(sub: readonly string[]): AnsiColor | undefined {
  if (num(sub[1]) === 2 && sub.length >= 6) return extended(['2', sub[3], sub[4], sub[5]], 0).color
  return extended(sub, 1).color
}

/** Applies the parameters of an SGR sequence to the state. */
function applySgr(s: State, params: string): void {
  const args = params === '' ? ['0'] : params.split(';')
  for (let i = 0; i < args.length; i++) {
    const arg = args[i] ?? ''
    if (arg.includes(':')) {
      const sub = arg.split(':')
      const code = num(sub[0])
      if (code === 38) s.fg = extendedColon(sub)
      else if (code === 48) s.bg = extendedColon(sub)
      else if (code === 4) s.underline = num(sub[1]) !== 0
      continue
    }
    const code = num(arg)
    if (code === 0) Object.assign(s, stateOf(null))
    else if (code === 1) s.bold = true
    else if (code === 2) s.dim = true
    else if (code === 3) s.italic = true
    else if (code === 4 || code === 21) s.underline = true
    else if (code === 22) s.bold = s.dim = false
    else if (code === 23) s.italic = false
    else if (code === 24) s.underline = false
    else if (code >= 30 && code <= 37) s.fg = code - 30
    else if (code >= 90 && code <= 97) s.fg = code - 90 + 8
    else if (code === 39) s.fg = undefined
    else if (code >= 40 && code <= 47) s.bg = code - 40
    else if (code >= 100 && code <= 107) s.bg = code - 100 + 8
    else if (code === 49) s.bg = undefined
    else if (code === 38 || code === 48 || code === 58) {
      const { color, used } = extended(args, i + 1)
      if (code === 38) s.fg = color
      else if (code === 48) s.bg = color
      i += used
    }
    // Blink, inverse, conceal, strike-through, fonts...: shown as plain text.
  }
}

/**
 * One line (without its line feed) as styled segments, starting in the
 * style the previous line ended in. Adjacent text in the same style is one
 * segment; a line without escape sequences is a single segment.
 */
export function parseAnsiLine(line: string, start: AnsiStyle | null = null): AnsiLine {
  if (!hasControl(line)) return { segments: line ? [{ text: line, style: start }] : [], end: start }
  const segments: AnsiSegment[] = []
  const state = stateOf(start)
  let style = start
  let key = styleKey(style)
  let text = ''
  let last = 0
  const push = (piece: string) => {
    if (piece) text += piece
  }
  const flush = () => {
    if (!text) return
    const prev = segments[segments.length - 1]
    if (prev && styleKey(prev.style) === key) segments[segments.length - 1] = { text: prev.text + text, style: prev.style }
    else segments.push({ text, style })
    text = ''
  }
  CONTROL.lastIndex = 0
  for (let m = CONTROL.exec(line); m; m = CONTROL.exec(line)) {
    push(line.slice(last, m.index))
    last = m.index + m[0].length
    // An SGR sequence; private ones ("\x1b[>4;2m") set other things.
    const params = m[1] ?? ''
    if (m[2] === 'm' && !/^[<=>?]/.test(params)) {
      applySgr(state, params)
      const next = styleOf(state)
      const nextKey = styleKey(next)
      if (nextKey !== key) {
        flush()
        style = next
        key = nextKey
      }
    }
  }
  push(line.slice(last))
  flush()
  return { segments, end: style }
}

/** The text of segments. */
export function segmentsText(segments: readonly AnsiSegment[]): string {
  return segments.length === 1 && segments[0] ? segments[0].text : segments.map((s) => s.text).join('')
}

/** Segments split at a character offset of their text. */
export function splitSegments(segments: readonly AnsiSegment[], at: number): [AnsiSegment[], AnsiSegment[]] {
  const head: AnsiSegment[] = []
  const tail: AnsiSegment[] = []
  let offset = 0
  for (const segment of segments) {
    const end = offset + segment.text.length
    if (end <= at) head.push(segment)
    else if (offset >= at) tail.push(segment)
    else {
      head.push({ text: segment.text.slice(0, at - offset), style: segment.style })
      tail.push({ text: segment.text.slice(at - offset), style: segment.style })
    }
    offset = end
  }
  return [head, tail]
}

/** The terminal's 16 colours as xterm defines them, to find the nearest one. */
const BASE_RGB: readonly (readonly [number, number, number])[] = [
  [0, 0, 0],
  [205, 0, 0],
  [0, 205, 0],
  [205, 205, 0],
  [0, 0, 238],
  [205, 0, 205],
  [0, 205, 205],
  [229, 229, 229],
  [127, 127, 127],
  [255, 0, 0],
  [0, 255, 0],
  [255, 255, 0],
  [92, 92, 255],
  [255, 0, 255],
  [0, 255, 255],
  [255, 255, 255],
]

const CUBE = [0, 95, 135, 175, 215, 255]

/** The RGB value of a 256-colour index (16-231 the colour cube, 232-255 greys). */
function rgbOf(color: AnsiColor): readonly [number, number, number] {
  if (typeof color === 'string') {
    const n = Number.parseInt(color.slice(1), 16)
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
  }
  if (color < 16) return BASE_RGB[color] ?? [0, 0, 0]
  if (color < 232) {
    const n = color - 16
    const level = (i: number) => CUBE[i] ?? 0
    return [level(Math.floor(n / 36)), level(Math.floor(n / 6) % 6), level(n % 6)]
  }
  const grey = 8 + (color - 232) * 10
  return [grey, grey, grey]
}

const nearest = new Map<AnsiColor, number>()

/**
 * The terminal colour (0-15) shown for a colour. The 16 are checked for
 * contrast in both modes (terminal/theme.ts); 256-colour and true-colour
 * values are not, so they take the nearest of them ("redmean" distance).
 */
export function baseColor(color: AnsiColor): number {
  if (typeof color === 'number' && color < 16) return color
  const known = nearest.get(color)
  if (known !== undefined) return known
  const [r, g, b] = rgbOf(color)
  let best = 0
  let bestDistance = Infinity
  BASE_RGB.forEach(([br, bg, bb], index) => {
    const mean = (r + br) / 2
    const distance = (2 + mean / 256) * (r - br) ** 2 + 4 * (g - bg) ** 2 + (2 + (255 - mean) / 256) * (b - bb) ** 2
    if (distance < bestDistance) {
      best = index
      bestDistance = distance
    }
  })
  if (nearest.size > 4096) nearest.clear()
  nearest.set(color, best)
  return best
}
