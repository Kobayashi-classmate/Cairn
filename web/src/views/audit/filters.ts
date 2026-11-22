// SPDX-License-Identifier: AGPL-3.0-only
//
// The audit log's filters, kept in the address so a filtered view can be
// bookmarked and linked to: `?user=<name>&category=<prefix>&result=ok|failed&range=24h|7d|30d`.
// The server applies all of them.
import type { LocationQuery, LocationQueryRaw } from 'vue-router'
import { AUDIT_CATEGORIES } from './describe'

export const RESULTS = ['ok', 'failed'] as const
export type ResultFilter = (typeof RESULTS)[number] | ''

export const RANGES = ['24h', '7d', '30d'] as const
export type RangeFilter = (typeof RANGES)[number] | ''

const RANGE_SECONDS: Record<(typeof RANGES)[number], number> = { '24h': 86400, '7d': 7 * 86400, '30d': 30 * 86400 }

export interface AuditFilters {
  user: string
  category: string
  result: ResultFilter
  range: RangeFilter
}

function text(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

export function filtersFrom(query: LocationQuery): AuditFilters {
  const category = text(query.category)
  const result = text(query.result)
  const range = text(query.range)
  return {
    user: text(query.user).trim(),
    category: (AUDIT_CATEGORIES as readonly string[]).includes(category) ? category : '',
    result: (RESULTS as readonly string[]).includes(result) ? (result as ResultFilter) : '',
    range: (RANGES as readonly string[]).includes(range) ? (range as RangeFilter) : '',
  }
}

/** The address query for `filters`, keeping the other parameters of `base` (an open entry). */
export function filtersQuery(filters: AuditFilters, base: LocationQuery): LocationQueryRaw {
  const query: LocationQueryRaw = { ...base }
  for (const key of ['user', 'category', 'result', 'range'] as const) {
    const value = filters[key].trim()
    if (value) query[key] = value
    else delete query[key]
  }
  return query
}

/** Unix seconds from which entries are shown; 0 for any time. */
export function rangeStart(range: RangeFilter, nowSeconds: number): number {
  return range ? nowSeconds - RANGE_SECONDS[range] : 0
}
