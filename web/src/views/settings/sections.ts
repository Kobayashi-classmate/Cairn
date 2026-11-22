// SPDX-License-Identifier: AGPL-3.0-only
//
// The settings categories of the left navigation ("settings" skeleton,
// interface spec section 6), one child route each. Personal settings come
// first; panel-wide ones need their permission (the routes check it too).
import type { Component } from 'vue'
import {
  BellRing,
  BookOpen,
  Cloud,
  KeyRound,
  MonitorSmartphone,
  Package,
  Palette,
  RefreshCw,
  ShieldCheck,
  SlidersHorizontal,
  UserRound,
} from '@lucide/vue'
import type { Permission } from '@/auth/permissions'

export type SettingsGroup = 'personal' | 'panel'

export interface SettingsSection {
  /** Name of the child route. */
  name: string
  /** Last part of `settings.nav.<key>`. */
  key: string
  group: SettingsGroup
  icon: Component
  permission?: Permission
}

export const SETTINGS_SECTIONS: readonly SettingsSection[] = [
  { name: 'settings-account', key: 'account', group: 'personal', icon: UserRound },
  { name: 'settings-appearance', key: 'appearance', group: 'personal', icon: Palette },
  { name: 'settings-security', key: 'security', group: 'personal', icon: ShieldCheck },
  { name: 'settings-sessions', key: 'sessions', group: 'personal', icon: MonitorSmartphone },
  { name: 'settings-tokens', key: 'tokens', group: 'personal', icon: KeyRound },
  { name: 'settings-api', key: 'api', group: 'personal', icon: BookOpen },
  { name: 'settings-panel', key: 'panel', group: 'panel', icon: SlidersHorizontal, permission: 'settings.read' },
  { name: 'settings-updates', key: 'updates', group: 'panel', icon: RefreshCw, permission: 'settings.read' },
  { name: 'settings-cloud', key: 'cloud', group: 'panel', icon: Cloud, permission: 'settings.read' },
  { name: 'settings-modules', key: 'modules', group: 'panel', icon: Package, permission: 'settings.read' },
  { name: 'settings-notifications', key: 'notifications', group: 'panel', icon: BellRing, permission: 'notify.manage' },
]
