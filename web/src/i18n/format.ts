// SPDX-License-Identifier: AGPL-3.0-only
//
// Locale-aware formatting built on Intl. Every helper reads the current UI
// locale when called, so templates that use them re-render on a language switch.
import { i18n } from './index'

// Intl formatters are costly to create, and charts format many values per frame.
const formatters = new Map<string, unknown>()

function cached<T>(kind: string, options: object, create: (locale: string) => T): T {
  const locale = i18n.global.locale.value
  const key = `${kind}|${locale}|${JSON.stringify(options)}`
  let format = formatters.get(key) as T | undefined
  if (format === undefined) {
    format = create(locale)
    formatters.set(key, format)
  }
  return format
}

function numberFormat(options: Intl.NumberFormatOptions): Intl.NumberFormat {
  return cached('number', options, (locale) => new Intl.NumberFormat(locale, options))
}

export function formatNumber(value: number, maximumFractionDigits = 0): string {
  return numberFormat({ maximumFractionDigits }).format(value)
}

/** Formats a percentage given on a 0–100 scale. */
export function formatPercent(percent: number, maximumFractionDigits = 1): string {
  return numberFormat({ style: 'percent', maximumFractionDigits }).format(percent / 100)
}

/** IEC binary units; the symbols are the same in every language. */
const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB'] as const

export function formatBytes(bytes: number): string {
  let value = Math.max(0, bytes)
  let unit = 0
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024
    unit++
  }
  const digits = unit === 0 || value >= 100 ? 0 : 1
  return i18n.global.t('common.format.bytes', {
    value: formatNumber(value, digits),
    unit: BYTE_UNITS[unit],
  })
}

/** Formats a transfer rate given in bytes per second. */
export function formatByteRate(bytesPerSecond: number): string {
  return i18n.global.t('common.format.rate', { value: formatBytes(bytesPerSecond) })
}

const DURATION_UNITS = [
  { unit: 'day', seconds: 86400 },
  { unit: 'hour', seconds: 3600 },
  { unit: 'minute', seconds: 60 },
  { unit: 'second', seconds: 1 },
] as const

/**
 * Formats an amount with a unit, with a space between the number and the
 * unit in every language (interface spec section 8), where Intl writes
 * some languages without one (Chinese hours, for example).
 */
function unitAmount(amount: number, unit: string, unitDisplay: 'long' | 'short'): string {
  const parts = numberFormat({ style: 'unit', unit, unitDisplay }).formatToParts(amount)
  let out = ''
  parts.forEach((part, i) => {
    const prev = parts[i - 1]
    if (prev && prev.type !== 'literal' && part.type !== 'literal' && (prev.type === 'unit') !== (part.type === 'unit')) {
      out += ' '
    }
    out += part.value
  })
  return out
}

/**
 * Formats a duration such as "3 days, 4 hours", keeping at most `maxParts`
 * adjacent units starting from the largest non-zero one.
 */
export function formatDuration(totalSeconds: number, maxParts = 2): string {
  let rest = Math.max(0, Math.floor(totalSeconds))
  const parts: string[] = []
  let started = false
  let used = 0
  for (const { unit, seconds } of DURATION_UNITS) {
    const amount = Math.floor(rest / seconds)
    rest -= amount * seconds
    if (amount > 0) started = true
    if (!started) continue
    if (amount > 0) parts.push(unitAmount(amount, unit, 'long'))
    if (++used >= maxParts) break
  }
  if (parts.length === 0) parts.push(unitAmount(0, 'second', 'long'))
  const options: Intl.ListFormatOptions = { type: 'unit', style: 'short' }
  const joined = cached('list', options, (locale) => new Intl.ListFormat(locale, options)).format(parts)
  // Languages whose unit lists have no separator (Chinese) get a space.
  return joined === parts.join('') ? parts.join(' ') : joined
}

/** Formats a time span in minutes with a short unit, for example "15 min". */
export function formatMinutes(minutes: number): string {
  return unitAmount(minutes, 'minute', 'short')
}

/** Formats a Unix timestamp (seconds) as a time of day. */
export function formatTime(unixSeconds: number, withSeconds = true): string {
  const options: Intl.DateTimeFormatOptions = withSeconds
    ? { hour: '2-digit', minute: '2-digit', second: '2-digit' }
    : { hour: '2-digit', minute: '2-digit' }
  return cached('time', options, (locale) => new Intl.DateTimeFormat(locale, options)).format(
    unixSeconds * 1000,
  )
}

/** Formats a Unix timestamp (seconds) as a date and time. */
export function formatDateTime(unixSeconds: number): string {
  const options: Intl.DateTimeFormatOptions = { dateStyle: 'medium', timeStyle: 'short' }
  return cached('datetime', options, (locale) => new Intl.DateTimeFormat(locale, options)).format(
    unixSeconds * 1000,
  )
}

/** Formats a Unix timestamp in milliseconds as a short date and a time with seconds (log lines). */
export function formatLogTime(unixMs: number): string {
  const options: Intl.DateTimeFormatOptions = {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  }
  return cached('logtime', options, (locale) => new Intl.DateTimeFormat(locale, options)).format(unixMs)
}

/** Formats a calendar date given as YYYY-MM-DD; other text is returned unchanged. */
export function formatIsoDate(date: string): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date)
  if (!match) return date
  const [, year, month, day] = match.map(Number) as [number, number, number, number]
  const options: Intl.DateTimeFormatOptions = { dateStyle: 'medium', timeZone: 'UTC' }
  return cached('date', options, (locale) => new Intl.DateTimeFormat(locale, options)).format(
    Date.UTC(year, month - 1, day),
  )
}

/**
 * Formats a Unix timestamp with the date, the time with seconds and the time
 * zone, for tooltips next to relative times (interface spec, section 8).
 */
export function formatFullDateTime(unixSeconds: number): string {
  const options: Intl.DateTimeFormatOptions = { dateStyle: 'full', timeStyle: 'long' }
  return cached('fulldatetime', options, (locale) => new Intl.DateTimeFormat(locale, options)).format(
    unixSeconds * 1000,
  )
}

/** Formats a Unix timestamp (seconds) as a date without the time. */
export function formatDate(unixSeconds: number): string {
  const options: Intl.DateTimeFormatOptions = { dateStyle: 'medium' }
  return cached('dateonly', options, (locale) => new Intl.DateTimeFormat(locale, options)).format(
    unixSeconds * 1000,
  )
}

/** Calendar days from the local day of `fromSeconds` to that of `toSeconds`. */
function calendarDays(fromSeconds: number, toSeconds: number): number {
  const from = new Date(fromSeconds * 1000)
  const to = new Date(toSeconds * 1000)
  const a = Date.UTC(from.getFullYear(), from.getMonth(), from.getDate())
  const b = Date.UTC(to.getFullYear(), to.getMonth(), to.getDate())
  return Math.round((b - a) / 86_400_000)
}

/** Within this many seconds a time reads as relative ("3 hours ago"). */
const RELATIVE_WINDOW_SECONDS = 6 * 3600

/**
 * A time as people say it (interface spec, section 8): "just now",
 * "3 hours ago", "today 02:00", "yesterday 21:14", "tomorrow 09:00", and the
 * date with the time further away.
 */
export function formatWhen(unixSeconds: number, nowSeconds: number): string {
  const { t } = i18n.global
  const size = Math.abs(unixSeconds - nowSeconds)
  if (size < 45) return t('common.time.justNow')
  if (size < RELATIVE_WINDOW_SECONDS) return formatRelativeTime(unixSeconds, nowSeconds)
  const time = formatTime(unixSeconds, false)
  switch (calendarDays(nowSeconds, unixSeconds)) {
    case 0:
      return t('common.time.today', { time })
    case -1:
      return t('common.time.yesterday', { time })
    case 1:
      return t('common.time.tomorrow', { time })
    default:
      return formatDateTime(unixSeconds)
  }
}

const RELATIVE_UNITS: Array<{ unit: Intl.RelativeTimeFormatUnit; seconds: number }> = [
  { unit: 'day', seconds: 86400 },
  { unit: 'hour', seconds: 3600 },
  { unit: 'minute', seconds: 60 },
  { unit: 'second', seconds: 1 },
]

/** Formats a Unix timestamp relative to `nowSeconds`, for example "6 hours ago". */
export function formatRelativeTime(unixSeconds: number, nowSeconds: number): string {
  const diff = unixSeconds - nowSeconds
  const size = Math.abs(diff)
  const { unit, seconds } = RELATIVE_UNITS.find((u) => size >= u.seconds) ?? RELATIVE_UNITS[3]!
  const options: Intl.RelativeTimeFormatOptions = { numeric: 'auto' }
  return cached('relative', options, (locale) => new Intl.RelativeTimeFormat(locale, options)).format(
    Math.round(diff / seconds),
    unit,
  )
}
