// SPDX-License-Identifier: AGPL-3.0-only
//
// Client-side form checks mirroring the server's validation rules. Messages
// are rendered on display, so a language switch updates visible errors. The
// server stays the authority; these checks only give faster feedback.
import type { FormItemRule } from 'naive-ui'
import { validationMessage } from '@/i18n/errors'

export const USERNAME_MIN = 3
export const USERNAME_MAX = 32
export const USERNAME_PATTERN = /^[a-z][a-z0-9_.-]*$/
export const PASSWORD_MIN = 12
export const TOTP_CODE_PATTERN = /^\d{6}$/
export const PORT_MIN = 1
export const PORT_MAX = 65535

const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+$/
const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/
const IPV6 = /^[0-9a-f:.]+$/i

export function isEmail(value: string): boolean {
  return EMAIL_PATTERN.test(value)
}

/** An absolute http:// or https:// URL. */
export function isHttpUrl(value: string): boolean {
  try {
    const url = new URL(value)
    return url.protocol === 'http:' || url.protocol === 'https:'
  } catch {
    return false
  }
}

/** An absolute https:// URL. */
export function isHttpsUrl(value: string): boolean {
  return isHttpUrl(value) && value.trim().toLowerCase().startsWith('https://')
}

/** An IPv4/IPv6 address, optionally with a CIDR prefix length. IPv6 is checked loosely. */
export function isIpOrCidr(value: string): boolean {
  const [address = '', prefix, extra] = value.split('/')
  if (extra !== undefined) return false
  const v4 = IPV4.test(address)
  const v6 = !v4 && address.includes(':') && IPV6.test(address)
  if (!v4 && !v6) return false
  if (prefix === undefined) return true
  if (!/^\d{1,3}$/.test(prefix)) return false
  return Number(prefix) <= (v4 ? 32 : 128)
}

/** Length in Unicode code points, the way the server counts it. */
export function textLength(value: string): number {
  return [...value].length
}

export function requiredRule(field: string): FormItemRule {
  return {
    required: true,
    trigger: 'blur',
    renderMessage: () => validationMessage(field, 'required'),
  }
}

/** A rule checked while typing; empty values are left to `requiredRule`. */
export function checkRule(
  field: string,
  rule: string,
  test: (value: string) => boolean,
  params: Record<string, number> = {},
): FormItemRule {
  return {
    trigger: ['blur', 'input'],
    validator: (_rule, value: string) => value === '' || test(value),
    renderMessage: () => validationMessage(field, rule, params),
  }
}

export function totpCodeRules(): FormItemRule[] {
  return [requiredRule('code'), checkRule('code', 'pattern', (v) => TOTP_CODE_PATTERN.test(v))]
}
