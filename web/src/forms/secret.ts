// SPDX-License-Identifier: AGPL-3.0-only

/** What to do with a write-only secret when saving. */
export type SecretMode = 'keep' | 'set' | 'clear'

/**
 * Value to send for a secret field: undefined (omit the field, keeping the
 * stored value), the new value, or "" to remove it.
 */
export function secretPatch(mode: SecretMode, value: string): string | undefined {
  if (mode === 'clear') return ''
  if (mode === 'set' && value !== '') return value
  return undefined
}
