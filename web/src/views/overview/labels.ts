// SPDX-License-Identifier: AGPL-3.0-only
//
// Words for the overview summary: the server sends codes and parameters
// (overview.check.*, overview.notice.*), this file turns them into text and
// links. Codes this build does not know are left out.
import type { RouteLocationRaw } from 'vue-router'
import type { OverviewCheck, OverviewService, ServiceState } from '@/api/overview'
import { i18n } from '@/i18n'
import { formatDate, formatNumber } from '@/i18n/format'
import type { Tone } from '@/lib/tone'

/** Order of the security card's rows, by the area before the dot of a code. */
const CHECK_ORDER = ['firewall', 'ssh', 'certs', 'auth']

/** Parameters holding Unix times, shown as dates. */
const DATE_PARAMS = new Set(['notAfter', 'soonest'])

function areaOf(code: string): string {
  return code.split('.')[0] ?? code
}

function named(params: Record<string, unknown> | undefined): Record<string, string | number> {
  const out: Record<string, string | number> = {}
  for (const [key, value] of Object.entries(params ?? {})) {
    if (typeof value === 'number') out[key] = DATE_PARAMS.has(key) ? formatDate(value) : value
    else if (typeof value === 'string') out[key] = value
    else if (Array.isArray(value)) out[key] = value.map((item) => String(item)).join(', ')
    else if (typeof value === 'boolean') out[key] = String(value)
  }
  return out
}

/** Plural form chosen by `count` when the finding has one. */
function translate(key: string, params: Record<string, unknown> | undefined): string | null {
  const { t, te } = i18n.global
  if (!te(key)) return null
  const values = named(params)
  const count = typeof params?.count === 'number' ? params.count : undefined
  if (count !== undefined) values.count = formatNumber(count)
  return count !== undefined ? t(key, values, count) : t(key, values)
}

export interface CheckRow {
  check: OverviewCheck
  title: string
  detail: string
}

/** The security card's rows in a fixed order; unknown codes are skipped. */
export function checkRows(checks: readonly OverviewCheck[]): CheckRow[] {
  const { t, te } = i18n.global
  const rows: CheckRow[] = []
  for (const check of checks) {
    const area = areaOf(check.code)
    const detail = translate(`overview.check.${check.code}`, check.params)
    if (detail === null || !te(`overview.area.${area}`)) continue
    rows.push({ check, title: t(`overview.area.${area}`), detail })
  }
  const rank = (row: CheckRow) => {
    const index = CHECK_ORDER.indexOf(areaOf(row.check.code))
    return index < 0 ? CHECK_ORDER.length : index
  }
  return rows.sort((a, b) => rank(a) - rank(b))
}

/** A notice's sentence, or null for a code this build does not know. */
export function noticeText(notice: OverviewCheck): string | null {
  return translate(`overview.notice.${notice.code}`, notice.params)
}

/** The action of a notice ("View updates"). */
export function noticeAction(notice: OverviewCheck): string {
  const { t, te } = i18n.global
  const key = `overview.noticeAction.${areaOf(notice.code)}`
  return te(key) ? t(key) : t('overview.noticeAction.default')
}

export const CHECK_TONE: Record<OverviewCheck['status'], Tone> = {
  ok: 'success',
  warn: 'warning',
  bad: 'destructive',
}

export function checkStatusLabel(status: OverviewCheck['status']): string {
  return i18n.global.t(`overview.checkStatus.${status}`)
}

/**
 * Where a finding is dealt with, when that page is in the user's menu.
 * Two-step verification lives on the user's own security settings.
 */
export function findingLocation(check: OverviewCheck, navRoutes: ReadonlySet<string>): RouteLocationRaw | null {
  if (check.code.startsWith('auth.')) return { name: 'settings-security' }
  if (!check.route) return null
  if (check.route === 'settings') return { name: 'settings' }
  return navRoutes.has(check.route) ? { name: check.route } : null
}

/** Product names of the software that runs services (not translated). */
const SERVICE_NAMES: Record<string, string> = {
  nginx: 'nginx',
  php: 'PHP-FPM',
  mariadb: 'MariaDB',
  mysql: 'MySQL',
  postgresql: 'PostgreSQL',
  redis: 'Redis',
  valkey: 'Valkey',
}

/** "PHP-FPM 8.4", "PostgreSQL 17", "nginx". */
export function serviceName(service: OverviewService): string {
  const name = SERVICE_NAMES[service.software] ?? service.software
  return /^\d/.test(service.line) ? `${name} ${service.line}` : name
}

/** The package version without its epoch ("1:10.11.6-0+deb12u1" reads "10.11.6-0+deb12u1"). */
export function serviceVersion(service: OverviewService): string {
  return (service.version ?? '').replace(/^\d+:/, '')
}

export const SERVICE_TONE: Record<ServiceState, Tone> = {
  running: 'success',
  stopped: 'neutral',
  failed: 'destructive',
  unknown: 'neutral',
}

/** Translated module names for "could not be checked". */
export function moduleNames(ids: readonly string[]): string {
  const { t, te } = i18n.global
  return ids.map((id) => (te(`modules.name.${id}`) ? t(`modules.name.${id}`) : id)).join(', ')
}
