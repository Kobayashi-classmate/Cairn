// SPDX-License-Identifier: AGPL-3.0-only
//
// Audit entries as people read them: the action in words with its
// parameters, a few extra facts as tags, the result and where it came from.
// Unknown actions show their code, so new server actions stay readable.
import type { AuditEntry } from '@/api/audit'
import { roleLabel } from '@/auth/permissions'
import { i18n, localeNativeName } from '@/i18n'
import { errorText } from '@/i18n/errors'
import { formatBytes } from '@/i18n/format'
import { decorateParams, itemNames, jobKindLabel, labelVariant } from '@/jobs/labels'
import { siteTypeLabel } from '@/sites/labels'

/** Action code prefixes the server filters on, in menu order. */
export const AUDIT_CATEGORIES = [
  'auth',
  'users',
  'settings',
  'notify',
  'update',
  'software',
  'site',
  'stream',
  'db',
  'redis',
  'file',
  'files',
  'terminal',
  'cron',
  'backup',
  'firewall',
  'ssh',
  'host',
  'cert',
  'dns',
  'job',
  'module',
  'setup',
] as const

/**
 * Parameters prepared for display: role and language codes become names.
 * `providerNames` maps DNS provider ids to their product names.
 */
function displayParams(entry: AuditEntry, providerNames: Readonly<Record<string, string>>): Record<string, string | number | boolean> {
  const { t, te } = i18n.global
  const params = { ...entry.params }
  if (typeof params.role === 'string') params.role = roleLabel(params.role)
  if (typeof params.locale === 'string') params.locale = localeNativeName(params.locale)
  // Jobs are named by their translated type; failed cancels may only carry the id.
  if (entry.action.startsWith('job.')) {
    params.kind = typeof params.kind === 'string' ? jobKindLabel(params.kind) : String(params.id ?? '')
  }
  decorateParams(entry.action, params)
  if (entry.action.startsWith('file.') && typeof params.size === 'number') params.size = formatBytes(params.size)
  if (typeof params.module === 'string') {
    const key = `modules.name.${params.module}`
    params.module = te(key) ? t(key) : params.module
  }
  // Older install/remove entries only carry the catalog id, not the product name.
  if (entry.action.startsWith('software.') && typeof params.name !== 'string') {
    params.name = String(params.software ?? '')
  }
  // Certificates and DNS accounts are named; older entries only carry their id.
  if (entry.action === 'cert.panel') {
    const name = typeof params.name === 'string' ? params.name : ''
    params.name = name || (params.cert ? String(params.cert) : t('certs.panel.selfSigned'))
  } else if (entry.action.startsWith('cert.') && !params.name && params.cert) {
    params.name = String(params.cert)
  }
  if (entry.action.startsWith('dns.')) {
    if (!params.name && params.account) params.name = String(params.account)
    if (typeof params.provider === 'string') params.provider = providerNames[params.provider] ?? params.provider
  }
  // Domain lists read as text.
  const domains = (entry.params as Record<string, unknown>).domains
  if (Array.isArray(domains)) params.domains = domains.join(', ')
  // Install sets list their lines ("PHP 8.4, nginx 1.26").
  const names = itemNames((entry.params as Record<string, unknown>).items)
  if (names !== null) params.names = names
  if (entry.action === 'setup.wizard' && typeof params.state === 'string') {
    const key = `wizard.state.${params.state}`
    params.state = te(key) ? t(key) : params.state
  }
  if (typeof params.downloadPolicy === 'string') {
    const key = `software.policy.${params.downloadPolicy}.short`
    params.downloadPolicy = te(key) ? t(key) : params.downloadPolicy
  }
  return params
}

/** The audit.variant key that describes an entry better than its plain text, if any. */
function variantKey(entry: AuditEntry): string | null {
  const { te } = i18n.global
  const variant = labelVariant(entry.action, entry.params)
  const key = variant ? `audit.variant.${entry.action}.${variant}` : ''
  return key && te(key) ? key : null
}

/** Translated description of an action; unknown actions show their code. */
export function describeAction(entry: AuditEntry, providerNames: Readonly<Record<string, string>> = {}): string {
  const { t, te } = i18n.global
  const plain = `audit.action.${entry.action}`
  if (!te(plain)) return entry.action
  const key = variantKey(entry) ?? plain
  const params = displayParams(entry, providerNames)
  return typeof params.count === 'number' ? t(key, params, params.count) : t(key, params)
}

/** Extra attributes shown as tags next to the description. */
export function actionDetails(entry: AuditEntry): string[] {
  const { t, te } = i18n.global
  const type = entry.params.type
  if (entry.action.startsWith('notify.') && typeof type === 'string') {
    const key = `notify.type.${type}`
    return [te(key) ? t(key) : type]
  }
  if (entry.action === 'settings.cloud') {
    const list: string[] = []
    if (typeof entry.params.enabled === 'boolean') {
      list.push(t(entry.params.enabled ? 'audit.detail.enabled' : 'audit.detail.disabled'))
    }
    if (typeof entry.params.channel === 'string') {
      const key = `updates.channel.${entry.params.channel}`
      list.push(t('audit.detail.channel', { channel: te(key) ? t(key) : entry.params.channel }))
    }
    // The service the panel trusts for messages and updates.
    if (typeof entry.params.baseUrl === 'string' && entry.params.baseUrl) {
      list.push(t('audit.detail.baseUrl', { url: entry.params.baseUrl }))
    }
    return list
  }
  // Service actions the descriptions do not name yet are shown as they are.
  if (entry.action === 'db.service' && typeof entry.params.action === 'string' && !variantKey(entry)) {
    return [entry.params.action]
  }
  if (entry.action.startsWith('stream.')) {
    const { protocol, port } = entry.params
    return typeof protocol === 'string' && port !== undefined ? [`${protocol.toUpperCase()} ${port}`] : []
  }
  if (entry.action === 'site.restart' && entry.params.reason === 'health') {
    return [t('audit.detail.healthRestart')]
  }
  if (entry.action.startsWith('site.')) {
    const list: string[] = []
    if (typeof entry.params.type === 'string') list.push(siteTypeLabel(entry.params.type))
    if (entry.action === 'site.delete' && typeof entry.params.deleteFiles === 'boolean') {
      list.push(t(entry.params.deleteFiles ? 'audit.detail.filesDeleted' : 'audit.detail.filesKept'))
    }
    return list
  }
  // The download source chosen for a runtime, as its candidate id.
  if (entry.action === 'software.install' && typeof entry.params.source === 'string') {
    return [t('audit.detail.source', { source: entry.params.source })]
  }
  if (entry.action !== 'users.update') return []
  const list: string[] = []
  if (typeof entry.params.role === 'string') {
    list.push(t('audit.detail.role', { role: roleLabel(entry.params.role) }))
  }
  if (typeof entry.params.disabled === 'boolean') {
    list.push(t(entry.params.disabled ? 'audit.detail.disabled' : 'audit.detail.enabled'))
  }
  return list
}

/** "Succeeded", or the error the action failed with in words (its code when unknown). */
export function resultText(entry: AuditEntry): string {
  const { t } = i18n.global
  return entry.result === 'ok' ? t('audit.result.ok') : (errorText(entry.result) ?? entry.result)
}

export function sourceText(source: string): string {
  const { t, te } = i18n.global
  const key = `audit.source.${source}`
  return te(key) ? t(key) : source
}

/** Category of an action code ("users.create" → "users"), if it is one the filter offers. */
export function categoryOf(action: string): string | null {
  const prefix = action.split('.')[0] ?? ''
  return (AUDIT_CATEGORIES as readonly string[]).includes(prefix) ? prefix : null
}

/** A parameter value as text: strings as they are, everything else as JSON. */
export function paramText(value: unknown): string {
  return typeof value === 'string' ? value : JSON.stringify(value)
}
