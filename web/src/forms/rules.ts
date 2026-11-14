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

export function usernameRules(field = 'username'): FormItemRule[] {
  return [
    requiredRule(field),
    checkRule(field, 'min_length', (v) => textLength(v) >= USERNAME_MIN, { min: USERNAME_MIN }),
    checkRule(field, 'max_length', (v) => textLength(v) <= USERNAME_MAX, { max: USERNAME_MAX }),
    checkRule(field, 'pattern', (v) => USERNAME_PATTERN.test(v)),
  ]
}

/** Rules for choosing a new password. */
export function newPasswordRules(field = 'password'): FormItemRule[] {
  return [
    requiredRule(field),
    checkRule(field, 'min_length', (v) => textLength(v) >= PASSWORD_MIN, { min: PASSWORD_MIN }),
  ]
}

/** Rules for repeating a password; `password` returns the value to match. */
export function confirmPasswordRules(password: () => string): FormItemRule[] {
  return [
    requiredRule('passwordConfirm'),
    checkRule('passwordConfirm', 'mismatch', (v) => v === password()),
  ]
}

export function totpCodeRules(): FormItemRule[] {
  return [requiredRule('code'), checkRule('code', 'pattern', (v) => TOTP_CODE_PATTERN.test(v))]
}

/** A list that must not be empty (checkbox groups, recipient lists). */
export function requiredListRule(field: string): FormItemRule {
  return {
    trigger: 'change',
    validator: (_rule, value: unknown) => Array.isArray(value) && value.length > 0,
    renderMessage: () => validationMessage(field, 'required'),
  }
}

/** Every entry of a list must pass `test`. */
export function listRule(field: string, rule: string, test: (value: string) => boolean): FormItemRule {
  return {
    trigger: 'change',
    validator: (_rule, value: unknown) =>
      !Array.isArray(value) || value.every((item) => test(String(item))),
    renderMessage: () => validationMessage(field, rule),
  }
}

/** A required whole number between `min` and `max`. */
export function rangeRule(field: string, min: number, max: number): FormItemRule {
  return {
    trigger: ['blur', 'change'],
    validator: (_rule, value: unknown) =>
      typeof value === 'number' && Number.isInteger(value) && value >= min && value <= max,
    renderMessage: () => validationMessage(field, 'range', { min, max }),
  }
}
