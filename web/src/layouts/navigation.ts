// SPDX-License-Identifier: AGPL-3.0-only
import type { Component } from 'vue'
import type { RouteLocationRaw } from 'vue-router'
import {
  DocumentTextOutline,
  MegaphoneOutline,
  PeopleOutline,
  SettingsOutline,
  SpeedometerOutline,
} from '@vicons/ionicons5'
import type { Permission } from '@/auth/permissions'

/** One entry of the sider menu. Modules add their entries here. */
export interface NavItem {
  /** Matches `meta.navKey` of the routes that highlight this entry. */
  key: string
  /** i18n key of the label. */
  labelKey: string
  icon: Component
  to: RouteLocationRaw
  /** Shown only to users with this permission (the route checks it as well). */
  permission?: Permission
}

export const navItems: NavItem[] = [
  {
    key: 'overview',
    labelKey: 'common.nav.overview',
    icon: SpeedometerOutline,
    to: { name: 'overview' },
    permission: 'system.read',
  },
  {
    key: 'users',
    labelKey: 'common.nav.users',
    icon: PeopleOutline,
    to: { name: 'users' },
    permission: 'users.manage',
  },
  { key: 'messages', labelKey: 'common.nav.messages', icon: MegaphoneOutline, to: { name: 'messages' } },
  {
    key: 'audit',
    labelKey: 'common.nav.audit',
    icon: DocumentTextOutline,
    to: { name: 'audit' },
    permission: 'audit.read',
  },
  { key: 'settings', labelKey: 'common.nav.settings', icon: SettingsOutline, to: { name: 'settings' } },
]

type Can = (permission: Permission) => boolean

/** Sider entries the user may open. */
export function visibleNavItems(can: Can): NavItem[] {
  return navItems.filter((item) => !item.permission || can(item.permission))
}

/** Where a signed-in user lands: the first page their role may open. */
export function homeLocation(can: Can): RouteLocationRaw {
  return visibleNavItems(can)[0]?.to ?? { name: 'settings' }
}
