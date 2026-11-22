// SPDX-License-Identifier: AGPL-3.0-only
//
// Renders API errors in the current language. The server only sends an error
// code and parameters; the message for code `a.b` lives at `errors.a.b`.
import { isApiError, type ErrorParams } from '@/api/client'
import { providerName } from '@/api/software'
import { rootLabel } from '@/files/paths'
import { i18n } from './index'
import { formatBytes, formatDuration } from './format'

type NamedParams = Record<string, string | number | boolean>

const UNKNOWN_KEY = 'errors.unknown'

/** Field names the server shares between forms, labelled by the rule that failed. */
const FIELD_BY_RULE: Record<string, string> = { site_code: 'siteCode', cert_id: 'certificate' }

/**
 * Translated label of a form field; unknown fields are shown by name.
 * A field that also groups sub-fields ("ca" next to "ca.directory") keeps
 * its own label under `.self`; DNS credential fields have labels of their own.
 */
export function fieldLabel(field: string): string {
  const { t, te } = i18n.global
  for (const key of [
    `common.field.${field}`,
    `common.field.${field}.self`,
    `dns.field.${field.replace(/^fields\./, '')}`,
  ]) {
    if (te(key)) return t(key)
  }
  return field
}

/**
 * Message for a failed validation rule, shared by client-side form checks and
 * server-side `validation.invalid` errors so both read the same.
 */
export function validationMessage(
  field: string,
  rule: string,
  params: NamedParams = {},
): string {
  const { t, te } = i18n.global
  const label = fieldLabel(field)
  // A range the server does not spell out reads as "out of the allowed range".
  const bounded = rule !== 'range' || params.min !== undefined || params.max !== undefined
  const key = `errors.validation.rule.${bounded ? rule : 'out_of_range'}`
  if (!te(key)) return t('errors.validation.field', { field: label })
  const named = { ...params, field: label }
  // Length limits pick the plural form from the limit itself.
  const count = rule === 'min_length' ? params.min : rule === 'max_length' ? params.max : undefined
  return typeof count === 'number' ? t(key, named, count) : t(key, named)
}

/**
 * Adds localized forms of well-known parameter kinds, by naming convention:
 * `<name>Seconds` (a duration) also provides `{<name>}`, and `<name>Bytes`
 * (a size) also provides `{<name>}`.
 */
function decorate(params: ErrorParams): NamedParams {
  const named: NamedParams = { ...params }
  for (const [key, value] of Object.entries(params)) {
    if (typeof value !== 'number') continue
    if (key.endsWith('Seconds') && key.length > 7) {
      named[key.slice(0, -7)] = formatDuration(value)
    } else if (key.endsWith('Bytes') && key.length > 5) {
      named[key.slice(0, -5)] = formatBytes(value)
    }
  }
  return named
}

/**
 * Message for an error code, or null when this build has no translation for
 * it. Used directly where the raw code is a better fallback than a generic
 * message (for example in the audit log).
 */
export function errorText(code: string, params: ErrorParams = {}): string | null {
  const { t, te } = i18n.global
  if (code === 'validation.invalid') {
    const { field, rule } = params
    if (typeof field !== 'string' || field === '') return t('errors.validation.invalid')
    const ruleName = typeof rule === 'string' ? rule : ''
    return validationMessage(FIELD_BY_RULE[ruleName] ?? field, ruleName, decorate(params))
  }
  // Codes whose message names its parameters have two texts: `errors.<code>`
  // reads well on its own (the audit log only has the code), and
  // `errors.detailed.<code>` is used when the parameters are at hand.
  // No free app port left in the panel's range.
  if (code === 'app.port_taken' && params.range !== undefined) return t('errors.app.port_range_full')
  // An archive with more entries than the server extracts.
  if (code === 'file.too_large' && typeof params.entries === 'number') return t('errors.file.too_many_entries', { max: params.entries })
  // Some codes read differently depending on a parameter.
  if (code === 'ssh.conflict' && params.key === 'Include') return t('errors.ssh.conflict_include', decorate(params))
  if (code === 'ssh.selinux_port' && (params.type === 'semanage_missing' || params.type === 'semanage_failed')) {
    return t(`errors.ssh.selinux_${params.type}`, decorate(params))
  }
  // A line without a service because the system does not run systemd.
  if (code === 'pkg.service_none' && params.reason === 'no_systemd') return t('errors.pkg.service_no_systemd')
  // A file operation where every entry failed (the job log lists them).
  if (code === 'op.failed' && typeof params.failed === 'number') return t('errors.op.all_failed', { failed: params.failed }, params.failed)
  const detailed = `errors.detailed.${code}`
  if (Object.keys(params).length > 0 && te(detailed)) {
    return withOthers(t(detailed, decorateFor(code, params)), code, params)
  }
  const key = `errors.${code}`
  return te(key) ? withOthers(t(key, decorate(params)), code, params) : null
}

/**
 * A file operation where every entry failed for the same reason names the
 * first one; `failed` counts them all, so the others are mentioned too.
 */
function withOthers(text: string, code: string, params: ErrorParams): string {
  const others = typeof params.failed === 'number' ? params.failed - 1 : 0
  if (others < 1 || !code.startsWith('file.')) return text
  return `${text} ${i18n.global.t('errors.sameCause', { count: others }, others)}`
}

/** Where the texts of a code's `reason` parameter live. */
const REASON_TEXTS: Record<string, string> = {
  'firewall.unsupported': 'firewall.unsupportedReason',
  'host.swap_unsupported': 'host.swap.unsupported',
  'syssrc.unsupported': 'host.sources.unsupportedReason',
}

/** Parameters with code-specific additions, such as a translated `reason`. */
function decorateFor(code: string, params: ErrorParams): NamedParams {
  const named = decorate(params)
  if (code === 'pkg.not_available' && typeof params.reason === 'string') {
    const key = `software.reason.${params.reason}`
    named.reasonText = i18n.global.te(key) ? i18n.global.t(key) : params.reason
  }
  // Why something is not possible here, translated where this build knows the reason.
  const reasons = REASON_TEXTS[code]
  if (reasons && typeof params.reason === 'string') {
    const key = `${reasons}.${params.reason}`
    named.reasonText = i18n.global.te(key) ? i18n.global.t(key, named) : params.reason
  }
  if (code === 'app.port_taken' || code === 'stream.port_taken') named.owner = portOwner(params)
  if (code === 'db.upload_too_large' && typeof params.max === 'number') named.size = formatBytes(params.max)
  if (code === 'file.too_large' && typeof params.max === 'number') named.size = formatBytes(params.max)
  if (code === 'pkg.config_too_large' && typeof params.max === 'number') named.size = formatBytes(params.max)
  // Older helpers name the failed services `units`.
  if (code === 'pkg.service_failed' && params.service === undefined && typeof params.units === 'string') named.service = params.units
  if (code === 'file.no_space' && typeof params.size === 'number') named.size = formatBytes(params.size)
  if (code === 'file.root_unavailable' && typeof params.root === 'string') named.root = rootLabel(params.root)
  if (code === 'file.user_unknown') named.name = String(params.user ?? params.group ?? '')
  // Providers of prebuilt runtimes are shown by name (not translated).
  if (typeof params.provider === 'string') named.provider = providerName(params.provider)
  return named
}

/**
 * Who uses a port, from `site`, `stream` or `panel` parameters, or from
 * `by` ("site <code>", "stream <code>", "panel").
 */
function portOwner(params: ErrorParams): string {
  const { t } = i18n.global
  let kind = ''
  let name = ''
  if (typeof params.site === 'string') [kind, name] = ['site', params.site]
  else if (typeof params.stream === 'string') [kind, name] = ['stream', params.stream]
  else if (params.panel === true) kind = 'panel'
  else if (typeof params.by === 'string') {
    const [first = '', ...rest] = params.by.split(' ')
    ;[kind, name] = [first, rest.join(' ')]
  }
  if (kind === 'site') return t('errors.owner.site', { name })
  if (kind === 'stream') return t('errors.owner.stream', { name })
  if (kind === 'panel') return t('errors.owner.panel')
  return typeof params.by === 'string' ? params.by : ''
}

/** Turns anything thrown by an API call into a message in the current language. */
export function errorMessage(err: unknown): string {
  const text = isApiError(err) ? errorText(err.code, err.params) : null
  return text ?? i18n.global.t(UNKNOWN_KEY)
}
