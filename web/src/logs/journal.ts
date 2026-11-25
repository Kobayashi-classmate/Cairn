// SPDX-License-Identifier: AGPL-3.0-only
//
// The system journal on the Logs page: its filters as they are kept in the
// address (`?unit=nginx.service&priority=4&since=-1h&grep=…&boot=1&limit=500`),
// so other pages can link to a unit's messages, and how entries are shown.
import type { LocationQuery } from 'vue-router'
import { JOURNAL_MAX, JOURNAL_TIME, type JournalEntry, type JournalQuery } from '@/api/logs'
import { truncateBytes } from '@/forms/rules'
import type { Tone } from '@/lib/tone'
import { stripAnsi } from './ansi'

/** Minimum levels offered (1 alert … 7 debug); none shows everything. */
export const PRIORITIES = [1, 2, 3, 4, 5, 6, 7] as const
export const LIMITS = [100, 200, 500, JOURNAL_MAX] as const
export const DEFAULT_LIMIT = 200
/** The longest search text the server takes, in UTF-8 bytes. */
export const GREP_MAX_BYTES = 200

export interface JournalFilters {
  unit: string
  /** "" for every level. */
  priority: string
  since: string
  until: string
  grep: string
  boot: boolean
  limit: number
}

/** Address parameters of the filters. */
export const FILTER_QUERY = ['unit', 'priority', 'since', 'until', 'grep', 'boot', 'limit'] as const

function text(query: LocationQuery, name: string): string {
  const value = query[name]
  return typeof value === 'string' ? value : ''
}

/** The filters in an address; values the server would refuse are left out. */
export function filtersFrom(query: LocationQuery): JournalFilters {
  const priority = text(query, 'priority')
  const limit = Number.parseInt(text(query, 'limit'), 10)
  const time = (value: string) => (JOURNAL_TIME.test(value) ? value : '')
  return {
    unit: text(query, 'unit').slice(0, 256),
    priority: (PRIORITIES as readonly number[]).includes(Number(priority)) ? priority : '',
    since: time(text(query, 'since')),
    until: time(text(query, 'until')),
    grep: truncateBytes(text(query, 'grep'), GREP_MAX_BYTES),
    boot: text(query, 'boot') === '1',
    limit: (LIMITS as readonly number[]).includes(limit) ? limit : DEFAULT_LIMIT,
  }
}

/** The address parameters of the filters (defaults left out), on top of `base`. */
export function filtersQuery(filters: JournalFilters, base: LocationQuery): LocationQuery {
  const query: LocationQuery = { ...base }
  for (const name of FILTER_QUERY) delete query[name]
  if (filters.unit) query.unit = filters.unit
  if (filters.priority) query.priority = filters.priority
  if (filters.since) query.since = filters.since
  if (filters.until) query.until = filters.until
  if (filters.grep) query.grep = filters.grep
  if (filters.boot) query.boot = '1'
  if (filters.limit !== DEFAULT_LIMIT) query.limit = String(filters.limit)
  return query
}

/** A unit typed without a suffix ("nginx") is a service; the server wants the full name. */
export function unitName(name: string): string {
  return /\.(service|socket|timer|scope|mount|target|path)$/.test(name) ? name : `${name}.service`
}

/** The request for the filters. */
export function journalQuery(filters: JournalFilters): JournalQuery {
  return {
    unit: filters.unit ? unitName(filters.unit) : undefined,
    priority: filters.priority ? Number(filters.priority) : undefined,
    since: filters.since || undefined,
    until: filters.until || undefined,
    grep: filters.grep || undefined,
    boot: filters.boot || undefined,
  }
}

/** Errors and worse are bad, warnings warn, notices and info are normal, debug is quiet. */
export function priorityTone(priority: number): Tone | null {
  if (priority <= 3) return 'destructive'
  if (priority === 4) return 'warning'
  if (priority >= 7) return 'neutral'
  return null
}

/** "nginx.service[812]": the unit (or the program, without one) and its process. */
export function sourceOf(entry: JournalEntry): string {
  const name = entry.unit || entry.identifier || ''
  return entry.pid ? `${name}[${entry.pid}]` : name
}

/** One entry as a line of text (copying the log), without the message's escape sequences. */
export function entryText(entry: JournalEntry, time: string, level: string): string {
  return `${time} ${level} ${sourceOf(entry)}: ${stripAnsi(entry.message)}`
}
