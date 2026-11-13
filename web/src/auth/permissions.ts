// SPDX-License-Identifier: AGPL-3.0-only
import { i18n } from '@/i18n'

export const ROLES = ['admin', 'operator', 'auditor'] as const
export type Role = (typeof ROLES)[number]

export type Permission =
  | 'system.read'
  | 'audit.read'
  | 'users.manage'
  | 'settings.read'
  | 'settings.manage'

/** Translated role name; a role this build does not know is shown as is. */
export function roleLabel(role: string): string {
  const key = `common.role.${role}`
  return i18n.global.te(key) ? i18n.global.t(key) : role
}
