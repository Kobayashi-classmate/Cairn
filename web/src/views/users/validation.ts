// SPDX-License-Identifier: AGPL-3.0-only
//
// Checks of the account forms, mirroring the server's rules for faster
// feedback; the server stays the authority.
import { PASSWORD_MIN, USERNAME_MAX, USERNAME_MIN, USERNAME_PATTERN, textLength } from '@/forms/rules'
import { validationMessage } from '@/i18n/errors'

export function usernameError(value: string): string | null {
  if (!value) return validationMessage('username', 'required')
  if (textLength(value) < USERNAME_MIN) return validationMessage('username', 'min_length', { min: USERNAME_MIN })
  if (textLength(value) > USERNAME_MAX) return validationMessage('username', 'max_length', { max: USERNAME_MAX })
  if (!USERNAME_PATTERN.test(value)) return validationMessage('username', 'pattern')
  return null
}

/** Errors of a new password and its repetition; `field` names the first one in messages. */
export function passwordErrors(
  password: string,
  confirm: string,
  field: 'password' | 'newPassword',
): { password?: string; confirm?: string } {
  const errors: { password?: string; confirm?: string } = {}
  if (!password) errors.password = validationMessage(field, 'required')
  else if (textLength(password) < PASSWORD_MIN) errors.password = validationMessage(field, 'min_length', { min: PASSWORD_MIN })
  if (!confirm) errors.confirm = validationMessage('passwordConfirm', 'required')
  else if (confirm !== password) errors.confirm = validationMessage('passwordConfirm', 'mismatch')
  return errors
}
