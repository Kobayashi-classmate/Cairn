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
    if (amount > 0) {
      parts.push(numberFormat({ style: 'unit', unit, unitDisplay: 'long' }).format(amount))
    }
    if (++used >= maxParts) break
  }
  if (parts.length === 0) {
    parts.push(numberFormat({ style: 'unit', unit: 'second', unitDisplay: 'long' }).format(0))
  }
  const options: Intl.ListFormatOptions = { type: 'unit', style: 'short' }
  return cached('list', options, (locale) => new Intl.ListFormat(locale, options)).format(parts)
}

/** Formats a time span in minutes with a short unit, for example "15 min". */
export function formatMinutes(minutes: number): string {
  return numberFormat({ style: 'unit', unit: 'minute', unitDisplay: 'short' }).format(minutes)
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
