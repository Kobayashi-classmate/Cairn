// SPDX-License-Identifier: AGPL-3.0-only
import { i18n } from '@/i18n'

export const ROLES = ['admin', 'operator', 'auditor'] as const
export type Role = (typeof ROLES)[number]

/** Permissions built into the panel core. */
export type KnownPermission =
  | 'system.read'
  | 'audit.read'
  | 'users.manage'
  | 'settings.read'
  | 'settings.manage'
  | 'notify.manage'
  | 'software.read'
  | 'software.manage'

/**
 * A permission name. Modules define their own at startup, so any string the
 * server sends is valid; the known names only help with autocompletion.
 */
export type Permission = KnownPermission | (string & {})

/** Translated role name; a role this build does not know is shown as is. */
export function roleLabel(role: string): string {
  const key = `common.role.${role}`
  return i18n.global.te(key) ? i18n.global.t(key) : role
}

/** Translated permission name; an unknown permission is shown as is. */
export function permissionLabel(permission: string): string {
  const key = `common.permission.${permission}`
  return i18n.global.te(key) ? i18n.global.t(key) : permission
}
