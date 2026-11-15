// SPDX-License-Identifier: AGPL-3.0-only
//
// Renders API errors in the current language. The server only sends an error
// code and parameters; the message for code `a.b` lives at `errors.a.b`.
import { isApiError, type ErrorParams } from '@/api/client'
import { providerName } from '@/api/software'
import { i18n } from './index'
import { formatBytes, formatDuration } from './format'

type NamedParams = Record<string, string | number | boolean>

const UNKNOWN_KEY = 'errors.unknown'

/** Field names the server shares between forms, labelled by the rule that failed. */
const FIELD_BY_RULE: Record<string, string> = { site_code: 'siteCode' }

/** Translated label of a form field; unknown fields are shown by name. */
export function fieldLabel(field: string): string {
  const key = `common.field.${field}`
  return i18n.global.te(key) ? i18n.global.t(key) : field
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
  const key = `errors.validation.rule.${rule}`
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
  const detailed = `errors.detailed.${code}`
  if (Object.keys(params).length > 0 && te(detailed)) {
    return t(detailed, decorateFor(code, params))
  }
  const key = `errors.${code}`
  return te(key) ? t(key, decorate(params)) : null
}

/** Parameters with code-specific additions, such as a translated `reason`. */
function decorateFor(code: string, params: ErrorParams): NamedParams {
  const named = decorate(params)
  if (code === 'pkg.not_available' && typeof params.reason === 'string') {
    const key = `software.reason.${params.reason}`
    named.reasonText = i18n.global.te(key) ? i18n.global.t(key) : params.reason
  }
  // Providers of prebuilt runtimes are shown by name (not translated).
  if (typeof params.provider === 'string') named.provider = providerName(params.provider)
  return named
}

/** Turns anything thrown by an API call into a message in the current language. */
export function errorMessage(err: unknown): string {
  const text = isApiError(err) ? errorText(err.code, err.params) : null
  return text ?? i18n.global.t(UNKNOWN_KEY)
}
