// SPDX-License-Identifier: AGPL-3.0-only
import type { JobState } from '@/api/jobs'
import { engineIdLabel } from '@/databases/labels'
import { i18n } from '@/i18n'

const FINAL_STATES: ReadonlySet<string> = new Set(['succeeded', 'failed', 'canceled', 'interrupted'])

/** True once a job can no longer change. */
export function isFinalState(state: string): boolean {
  return FINAL_STATES.has(state)
}

/**
 * Variant of a job (or audit) label for the params at hand. Some kinds read
 * better naming a value ("Stop the database server") or depend on whether
 * an optional param is set. The text lives under
 * <area>.variant.<kind>.<variant>; when there is no variant, or no text for
 * it, the plain label is used.
 */
export function labelVariant(kind: string, params: Record<string, unknown>): string | null {
  switch (kind) {
    case 'db.service':
      return typeof params.action === 'string' && /^[a-z]+$/.test(params.action) ? params.action : null
    case 'db.remote':
      return typeof params.enabled === 'boolean' ? (params.enabled ? 'enable' : 'disable') : null
    case 'db.dump':
      // Redis snapshots have no database.
      return typeof params.database === 'string' && params.database !== '' ? 'database' : null
    default:
      return null
  }
}

/**
 * Translated job type. With the job's params the label can name what it acts
 * on ("Install PHP 8.4"); without them a short name is used where the full
 * label needs params. A kind this build does not know is shown as is.
 */
export function jobKindLabel(kind: string, params?: Record<string, unknown>): string {
  const { t, te } = i18n.global
  const named: Record<string, string | number | boolean> = {}
  for (const [key, value] of Object.entries(params ?? {})) {
    if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
      named[key] = value
    }
  }
  const short = `jobs.kindName.${kind}`
  if (Object.keys(named).length === 0 && te(short)) return t(short)
  if (/^(db|redis)\./.test(kind) && typeof named.engine === 'string') named.engine = engineIdLabel(named.engine)
  const variant = labelVariant(kind, named)
  if (variant && te(`jobs.variant.${kind}.${variant}`)) return t(`jobs.variant.${kind}.${variant}`, named)
  const full = `jobs.kind.${kind}`
  return te(full) ? t(full, named) : kind
}

export function jobStateLabel(state: string): string {
  const key = `jobs.state.${state}`
  return i18n.global.te(key) ? i18n.global.t(key) : state
}

export function jobStateTagType(state: JobState | string): 'default' | 'info' | 'success' | 'error' | 'warning' {
  switch (state) {
    case 'running':
      return 'info'
    case 'succeeded':
      return 'success'
    case 'failed':
      return 'error'
    case 'interrupted':
      return 'warning'
    default:
      return 'default'
  }
}

/** Who started a job: the user, or "System" for jobs the panel starts itself. */
export function jobActorLabel(username: string): string {
  return username || i18n.global.t('audit.noUser')
}

/** Translated origin of a request (web, api, cli, system); unknown values as is. */
export function sourceLabel(source: string): string {
  const key = `audit.source.${source}`
  return i18n.global.te(key) ? i18n.global.t(key) : source
}

/** "PHP 8.4, nginx 1.26" for a list of {name, line} items (install sets). */
export function itemNames(items: unknown): string | null {
  if (!Array.isArray(items)) return null
  const names = items.map((item) => {
    if (typeof item !== 'object' || item === null) return String(item)
    const { name, software, line } = item as Record<string, unknown>
    return [name ?? software, line].filter((part) => typeof part === 'string' && part !== '').join(' ')
  })
  return names.join(', ')
}

/** A job or audit parameter as plain text. */
export function formatJobParam(value: unknown): string {
  if (typeof value === 'string') return value
  return itemNames(value) ?? JSON.stringify(value)
}
