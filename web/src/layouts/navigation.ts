// SPDX-License-Identifier: AGPL-3.0-only
import type { Component } from 'vue'
import type { RouteLocationRaw } from 'vue-router'
import { SpeedometerOutline } from '@vicons/ionicons5'

/** One entry of the sider menu. Modules add their entries here. */
export interface NavItem {
  /** Matches `meta.navKey` of the routes that highlight this entry. */
  key: string
  /** i18n key of the label. */
  labelKey: string
  icon: Component
  to: RouteLocationRaw
}

export const navItems: NavItem[] = [
  { key: 'overview', labelKey: 'common.nav.overview', icon: SpeedometerOutline, to: { name: 'overview' } },
]
