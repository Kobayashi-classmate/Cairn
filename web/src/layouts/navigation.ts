// SPDX-License-Identifier: AGPL-3.0-only
//
// The sider menu comes from the server (GET /ui/nav). This file turns its
// entries into menu items the client can show: icon names are mapped through
// an allowlist, and entries pointing at routes this build does not know are
// left out, so a newer server never breaks an older UI.
import type { Component } from 'vue'
import type { RouteLocationRaw, Router } from 'vue-router'
import {
  AppsOutline,
  CubeOutline,
  DocumentTextOutline,
  FolderOutline,
  GlobeOutline,
  GridOutline,
  ListOutline,
  MegaphoneOutline,
  NotificationsOutline,
  PeopleOutline,
  PulseOutline,
  ServerOutline,
  SettingsOutline,
  ShieldOutline,
  SpeedometerOutline,
  TerminalOutline,
  TimeOutline,
} from '@vicons/ionicons5'
import type { NavEntry } from '@/api/nav'
import { useNavStore } from '@/stores/nav'

/** Icon names the server may use. Unknown names get GENERIC_ICON. */
const ICONS: Record<string, Component> = {
  apps: AppsOutline,
  cube: CubeOutline,
  'document-text': DocumentTextOutline,
  folder: FolderOutline,
  globe: GlobeOutline,
  grid: GridOutline,
  list: ListOutline,
  megaphone: MegaphoneOutline,
  notifications: NotificationsOutline,
  people: PeopleOutline,
  pulse: PulseOutline,
  server: ServerOutline,
  settings: SettingsOutline,
  shield: ShieldOutline,
  speedometer: SpeedometerOutline,
  terminal: TerminalOutline,
  time: TimeOutline,
}
const GENERIC_ICON = GridOutline

/** One entry of the sider menu, ready to render. */
export interface NavItem {
  /** Matches `meta.navKey` of the routes that highlight this entry. */
  key: string
  /** i18n key of the label. */
  labelKey: string
  icon: Component
  to: RouteLocationRaw
}

/** Menu items for the server's entries, keeping only routes the router knows. */
export function resolveNav(entries: readonly NavEntry[], router: Router): NavItem[] {
  return entries
    .filter((entry) => router.hasRoute(entry.route))
    .map((entry) => ({
      key: entry.key,
      labelKey: entry.labelKey,
      icon: ICONS[entry.icon] ?? GENERIC_ICON,
      to: { name: entry.route },
    }))
}

/** Where a signed-in user lands: the first menu entry, or settings. */
export function homeLocation(router: Router): RouteLocationRaw {
  return resolveNav(useNavStore().entries, router)[0]?.to ?? { name: 'settings' }
}
