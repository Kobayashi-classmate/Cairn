// SPDX-License-Identifier: AGPL-3.0-only
import type { JobState } from '@/api/jobs'
import { engineIdLabel } from '@/databases/labels'
import { absolutePath } from '@/files/paths'
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
  const many = typeof params.count === 'number' && params.count > 1
  switch (kind) {
    case 'db.service':
    case 'software.service':
      return typeof params.action === 'string' && /^[a-z]+$/.test(params.action) ? params.action : null
    case 'db.remote':
    case 'settings.step_up':
      return typeof params.enabled === 'boolean' ? (params.enabled ? 'enable' : 'disable') : null
    case 'db.dump':
      // Redis snapshots have no database.
      return typeof params.database === 'string' && params.database !== '' ? 'database' : null
    case 'file.copy':
    case 'file.move':
    case 'file.chmod':
    case 'file.chown':
    case 'file.restore':
      return many ? 'many' : null
    case 'file.delete':
      if (params.permanent === true) return many ? 'manyPermanent' : 'permanent'
      return many ? 'many' : null
    case 'file.purge':
      // Emptying the trash names no entries.
      return params.count === 0 ? 'all' : many ? 'many' : null
    case 'file.save':
      return params.new === true ? 'new' : null
    case 'backup.restore':
      // Database backups become a dump of their engine instead of files.
      if (typeof params.dest === 'string') return 'dest'
      return typeof params.database === 'string' ? 'database' : null
    case 'backup.plan_delete':
      return params.files === true ? 'files' : null
    case 'files.full_disk':
    case 'terminal.root':
    case 'host.ntp':
    case 'host.updates_auto':
      return typeof params.enabled === 'boolean' ? (params.enabled ? 'enable' : 'disable') : null
    case 'host.swap':
      return params.sizeMb === 0 ? 'remove' : null
    case 'host.updates_install':
      return params.securityOnly === true ? 'security' : null
    case 'firewall.ping':
      return typeof params.block === 'boolean' ? (params.block ? 'block' : 'allow') : null
    case 'firewall.address_add':
      return params.action === 'allow' || params.action === 'deny' ? params.action : null
    case 'firewall.forward_add':
    case 'firewall.forward_update':
      // Forwards to another host name it; those to this host have no address.
      return typeof params.toAddress === 'string' && params.toAddress !== '' ? 'remote' : null
    default:
      return null
  }
}

/**
 * Job and audit parameters as people read them: engine IDs become product
 * names, file paths become absolute paths, sizes are formatted by the caller.
 */
export function decorateParams(kind: string, named: Record<string, string | number | boolean>): void {
  const { t, te } = i18n.global
  const translate = (prefix: string, value: string | number | boolean | undefined) =>
    typeof value === 'string' && te(`${prefix}.${value}`) ? t(`${prefix}.${value}`) : value
  if ((/^(db|redis)\./.test(kind) || kind === 'backup.restore') && typeof named.engine === 'string') named.engine = engineIdLabel(named.engine)
  // Firewall rules read "Allow 80,443 (TCP)".
  if (kind === 'firewall.rule_add' || kind === 'firewall.rule_update') {
    const action = translate('firewall.action', named.action)
    const protocol = translate('firewall.protocol', named.protocol)
    if (action !== undefined) named.action = action
    if (protocol !== undefined) named.protocol = protocol
  }
  // Port forwards read "TCP 8090 to 10.0.0.5:80".
  if (kind === 'firewall.forward_add' || kind === 'firewall.forward_update') {
    const protocol = translate('firewall.protocol', named.protocol)
    if (protocol !== undefined) named.protocol = protocol
  }
  if (kind.startsWith('host.sources_')) {
    const role = translate('host.sources.role', named.role)
    if (role !== undefined) named.role = role
  }
  if ((kind.startsWith('file.') || kind === 'backup.restore') && typeof named.root === 'string') {
    if (typeof named.path === 'string') named.path = absolutePath(named.root, named.path)
    if (typeof named.dest === 'string') named.dest = absolutePath(named.root, named.dest)
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
  // The variant reads the raw values; decorating translates some of them.
  const variant = labelVariant(kind, named)
  decorateParams(kind, named)
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
