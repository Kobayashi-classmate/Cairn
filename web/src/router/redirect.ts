// SPDX-License-Identifier: AGPL-3.0-only

/**
 * Validates the `?redirect=` target used after signing in. Only paths inside
 * the panel are accepted, so the parameter cannot send users to another site.
 */
export function safeRedirect(value: unknown): string | null {
  if (typeof value !== 'string') return null
  if (!value.startsWith('/') || value.startsWith('//') || value.startsWith('/\\')) return null
  return value
}
