// SPDX-License-Identifier: AGPL-3.0-only
//
// Client-side form checks mirroring the server's validation rules. Messages
// are rendered on display, so a language switch updates visible errors.
import type { FormItemRule } from 'naive-ui'
import { validationMessage } from '@/i18n/errors'

export const USERNAME_MIN = 3
export const USERNAME_MAX = 32
export const USERNAME_PATTERN = /^[a-z][a-z0-9_.-]*$/
export const PASSWORD_MIN = 12
export const TOTP_CODE_PATTERN = /^\d{6}$/

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
