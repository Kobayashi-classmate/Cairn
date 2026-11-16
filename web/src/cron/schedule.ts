// SPDX-License-Identifier: AGPL-3.0-only
//
// Schedules are systemd OnCalendar expressions. The common ones are offered
// as presets; anything else is written by hand ("custom").
import { i18n } from '@/i18n'

export const PRESET_KINDS = ['minutes', 'hourly', 'daily', 'weekly', 'monthly', 'custom'] as const
export type PresetKind = (typeof PRESET_KINDS)[number]

/** Weekdays as systemd writes them. */
export const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as const
export type Weekday = (typeof WEEKDAYS)[number]

export interface Preset {
  kind: PresetKind
  /** Every so many minutes (minutes). */
  every: number
  hour: number
  minute: number
  weekday: Weekday
  /** Day of the month (monthly). */
  day: number
  /** The expression itself (custom). */
  custom: string
}

export const DEFAULT_PRESET: Preset = { kind: 'daily', every: 15, hour: 3, minute: 0, weekday: 'Mon', day: 1, custom: '' }

function two(n: number): string {
  return String(n).padStart(2, '0')
}

/** The OnCalendar expression of a preset. */
export function toExpression(preset: Preset): string {
  // Number fields are empty (null) while being cleared.
  const hour = Number(preset.hour) || 0
  const minute = Number(preset.minute) || 0
  const time = `${two(hour)}:${two(minute)}:00`
  switch (preset.kind) {
    case 'minutes':
      return `*:0/${Number(preset.every) || 1}`
    case 'hourly':
      return `*-*-* *:${two(minute)}:00`
    case 'daily':
      return `*-*-* ${time}`
    case 'weekly':
      return `${preset.weekday} *-*-* ${time}`
    case 'monthly':
      return `*-*-${Number(preset.day) || 1} ${time}`
    case 'custom':
      return preset.custom.trim()
  }
}

function inRange(value: number, min: number, max: number): boolean {
  return Number.isInteger(value) && value >= min && value <= max
}

/** The preset an expression was made from; anything else is custom. */
export function fromExpression(expr: string): Preset {
  const text = expr.trim()
  const preset: Preset = { ...DEFAULT_PRESET, custom: text }
  let match = /^\*:0\/(\d{1,2})$/.exec(text)
  if (match && inRange(Number(match[1]), 1, 59)) return { ...preset, kind: 'minutes', every: Number(match[1]) }
  match = /^\*-\*-\* \*:(\d{2}):00$/.exec(text)
  if (match && inRange(Number(match[1]), 0, 59)) return { ...preset, kind: 'hourly', minute: Number(match[1]) }
  match = /^(?:(Mon|Tue|Wed|Thu|Fri|Sat|Sun) )?\*-\*-(\*|\d{1,2}) (\d{2}):(\d{2}):00$/.exec(text)
  if (match) {
    const [, weekday, day, hour, minute] = match
    const time = { hour: Number(hour), minute: Number(minute) }
    if (!inRange(time.hour, 0, 23) || !inRange(time.minute, 0, 59)) return { ...preset, kind: 'custom' }
    if (weekday && day === '*') return { ...preset, ...time, kind: 'weekly', weekday: weekday as Weekday }
    if (!weekday && day === '*') return { ...preset, ...time, kind: 'daily' }
    if (!weekday && inRange(Number(day), 1, 31)) return { ...preset, ...time, kind: 'monthly', day: Number(day) }
  }
  return { ...preset, kind: 'custom' }
}

/** A schedule in words where it is a preset; the expression itself otherwise. */
export function describeSchedule(expr: string): string {
  const { t } = i18n.global
  const preset = fromExpression(expr)
  const time = `${two(preset.hour)}:${two(preset.minute)}`
  switch (preset.kind) {
    case 'minutes':
      return t('cron.schedule.describe.minutes', { every: preset.every }, preset.every)
    case 'hourly':
      return t('cron.schedule.describe.hourly', { minute: two(preset.minute) })
    case 'daily':
      return t('cron.schedule.describe.daily', { time })
    case 'weekly':
      return t('cron.schedule.describe.weekly', { weekday: t(`cron.weekday.${preset.weekday}`), time })
    case 'monthly':
      return t('cron.schedule.describe.monthly', { day: preset.day, time })
    default:
      return expr
  }
}

/** Characters systemd unit files cannot take in a schedule. */
export function isScheduleText(expr: string): boolean {
  return expr.trim() !== '' && expr.length <= 200 && !/[\0\n\r"'\\;$`%]/.test(expr)
}
