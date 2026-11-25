// SPDX-License-Identifier: AGPL-3.0-only
//
// Log text as the panel shows it: the program's own ANSI colours
// (logs/ansi.ts), and for lines in default colours a few hints from what
// they say. A leading timestamp is muted; error lines (nginx's [error],
// apt's "E:", "Error:") take the destructive colour and warnings the
// warning colour; the command lines of a job log ("$ apt-get ...") stand
// out. The text itself is never changed beyond dropping control sequences,
// and each hint is in the words of the line, so colour is not the only
// signal.
//
// Logs are long (a job log keeps 1 MiB, the journal 5000 entries): runs of
// plain lines stay one string, and lines with escape sequences are parsed
// once and remembered.
import type { Tone } from '@/lib/tone'
import { hasControl, parseAnsiLine, segmentsText, splitSegments, type AnsiSegment, type AnsiStyle } from './ansi'

/** A line with styling. */
export interface LogLine {
  /** The leading timestamp (and journal source), shown muted. */
  readonly prefix: readonly AnsiSegment[]
  readonly body: readonly AnsiSegment[]
  /** From what the line says; null for the default colour. */
  readonly tone: Tone | null
  /** A command the job ran ("$ apt-get update"). */
  readonly command: boolean
}

/** A string is one or more plain lines with their line feeds. */
export type LogPiece = string | LogLine

/** What the hints found in a line's text. */
interface Hint {
  /** Characters of the muted prefix. */
  prefix: number
  tone: Tone | null
  command: boolean
}

/**
 * Leading timestamps: ISO 8601 (journalctl's short-iso, most programs),
 * nginx's "2026/11/14 10:00:00", syslog's "Nov 14 10:00:00" and PHP-FPM's
 * "14-Nov-2026 10:00:00", optionally in brackets.
 */
const STAMP =
  /^(\[?)(\d{4}([-/])\d{2}\3\d{2}([T ])\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+-]\d{2}:?\d{2})?|[A-Z][a-z]{2} [ \d]\d \d{2}:\d{2}:\d{2}|\d{2}-[A-Z][a-z]{2}-\d{4} \d{2}:\d{2}:\d{2}(?:\.\d+)?)(\]?)(?=\s|$)/

/** journalctl's "host unit[pid]:" after its timestamp. */
const JOURNAL_SOURCE = /^ [^\s[]+ [^\s[:]+\[\d+\]:(?=\s|$)/

/** nginx's level: "[error] 812#812: ..." in its log, "nginx: [emerg] ..." from nginx -t. */
const NGINX_LEVEL = /^(?:[\w.-]+: )?\[(emerg|alert|crit|error|warn)\]/

/** "Error: ...", "WARNING: ..." (dnf, PHP-FPM and most tools). */
const WORD_LEVEL = /^(error|fatal|warning|warn):/i

/** The length of a timestamp at the start of text, or 0. */
function stampLength(text: string): { length: number; journal: boolean } {
  const m = STAMP.exec(text)
  // Brackets come in pairs.
  if (!m || (m[1] === '[') !== (m[5] === ']')) return { length: 0, journal: false }
  // journalctl writes ISO time with a "T", syslog the month's name.
  return { length: m[0].length, journal: m[4] === 'T' || /^[A-Z]/.test(m[2] ?? '') }
}

function toneOf(message: string): Tone | null {
  if (message.startsWith('E: ')) return 'destructive'
  if (message.startsWith('W: ')) return 'warning'
  const nginx = NGINX_LEVEL.exec(message)
  if (nginx) return nginx[1] === 'warn' ? 'warning' : 'destructive'
  const word = WORD_LEVEL.exec(message)
  if (word) return word[1]?.toLowerCase().startsWith('warn') ? 'warning' : 'destructive'
  return null
}

/** The hints for a line's text; null when there are none. */
export function lineHint(text: string): Hint | null {
  if (text.startsWith('$ ')) return { prefix: 0, tone: null, command: true }
  let prefix = 0
  const stamp = stampLength(text)
  if (stamp.length > 0) {
    prefix = stamp.length
    if (stamp.journal) {
      const source = JOURNAL_SOURCE.exec(text.slice(prefix))
      if (source) prefix += source[0].length
    }
    // The program's own timestamp after the journal's.
    const inner = stampLength(text.slice(prefix).replace(/^ /, ''))
    if (inner.length > 0 && text[prefix] === ' ') prefix += 1 + inner.length
  }
  const message = prefix > 0 ? text.slice(prefix).trimStart() : text
  const tone = toneOf(message)
  if (prefix === 0 && tone === null) return null
  return { prefix, tone, command: false }
}

/** A line read: a plain string (control sequences dropped) or a styled line; and the style it ends in. */
interface Read {
  readonly line: string | LogLine
  readonly end: AnsiStyle | null
}

function styled(segments: readonly AnsiSegment[], text: string, hints: boolean): string | LogLine {
  const hint = hints ? lineHint(text) : null
  const plain = segments.every((s) => s.style === null)
  if (plain && !hint) return text
  const [prefix, body] = hint && hint.prefix > 0 ? splitSegments(segments, hint.prefix) : [[], segments]
  return { prefix, body, tone: hint?.tone ?? null, command: hint?.command ?? false }
}

/** Lines with escape sequences, read before; cleared when it grows too big. */
const cache = new Map<string, Read>()
const CACHE_MAX = 20000

function readLine(raw: string, start: AnsiStyle | null, hints: boolean): Read {
  if (!hasControl(raw)) {
    if (start === null) return { line: styled([{ text: raw, style: null }], raw, hints), end: null }
    // Colour carried over from the line before.
    return { line: styled(raw ? [{ text: raw, style: start }] : [], raw, hints), end: start }
  }
  // Only lines that start in the default style are remembered: that is
  // nearly all of them, and the key needs no style.
  const key = start === null ? (hints ? 'h' : 'p') + raw : ''
  const known = key ? cache.get(key) : undefined
  if (known) return known
  const parsed = parseAnsiLine(raw, start)
  const read = { line: styled(parsed.segments, segmentsText(parsed.segments), hints), end: parsed.end }
  if (key) {
    if (cache.size >= CACHE_MAX) cache.clear()
    cache.set(key, read)
  }
  return read
}

/**
 * Text as pieces to render: runs of plain lines as single strings, styled
 * lines on their own. `hints` adds the colours from what lines say; turn it
 * off where the text already has a colour (a journal entry's priority).
 */
export function logPieces(text: string, hints = true): LogPiece[] {
  // A short plain message, the common journal entry: nothing to do.
  if (!hints && !hasControl(text)) return text ? [text] : []
  const pieces: LogPiece[] = []
  const lines = text.split('\n')
  let run = ''
  let state: AnsiStyle | null = null
  for (let i = 0; i < lines.length; i++) {
    const read = readLine(lines[i] ?? '', state, hints)
    state = read.end
    const nl = i < lines.length - 1 ? '\n' : ''
    if (typeof read.line === 'string') {
      run += read.line + nl
      continue
    }
    if (run) pieces.push(run)
    pieces.push(read.line)
    run = nl
  }
  if (run) pieces.push(run)
  return pieces
}
