// SPDX-License-Identifier: AGPL-3.0-only
//
// The sidebar menu comes from the server (GET /ui/nav). This file turns its
// entries into menu items the client can show: icon names are mapped through
// an allowlist, and entries pointing at routes this build does not know are
// left out, so a newer server never breaks an older UI.
import type { Component } from 'vue'
import type { RouteLocationRaw, Router } from 'vue-router'
import {
  Activity,
  Archive,
  Bell,
  Box,
  Clock,
  Database,
  FileText,
  Folder,
  Gauge,
  Globe,
  KeyRound,
  LayoutGrid,
  ListChecks,
  Lock,
  Megaphone,
  Package,
  ScrollText,
  Server,
  Settings,
  Shield,
  ShieldCheck,
  SquareTerminal,
  Users,
} from '@lucide/vue'
import type { NavEntry, NavGroup } from '@/api/nav'
import { useNavStore } from '@/stores/nav'

/** Icon names the server may use (Lucide icons). Unknown names get GENERIC_ICON. */
const ICONS: Record<string, Component> = {
  apps: Package,
  archive: Archive,
  cube: Box,
  database: Database,
  'document-text': FileText,
  folder: Folder,
  globe: Globe,
  grid: LayoutGrid,
  key: KeyRound,
  list: ListChecks,
  lock: Lock,
  megaphone: Megaphone,
  notifications: Bell,
  people: Users,
  pulse: Activity,
  reader: ScrollText,
  server: Server,
  settings: Settings,
  shield: Shield,
  'shield-checkmark': ShieldCheck,
  speedometer: Gauge,
  terminal: SquareTerminal,
  time: Clock,
}
const GENERIC_ICON = LayoutGrid

const GROUPS: readonly NavGroup[] = ['apps', 'ops', 'security', 'system']

/**
 * Entries reached from the user menu and the command palette instead of the
 * sidebar (interface spec, section 6).
 */
const USER_MENU_KEYS: ReadonlySet<string> = new Set(['messages'])

/** One entry of the menu, ready to render. */
export interface NavItem {
  /** Matches `meta.navKey` of the routes that highlight this entry. */
  key: string
  /** i18n key of the label. */
  labelKey: string
  icon: Component
  to: RouteLocationRaw
  /** Heading the entry is listed under; "" for entries above every heading. */
  group: NavGroup | ''
}

/** A run of menu items under one heading ("" has no heading). */
export interface NavSection {
  group: NavGroup | ''
  items: NavItem[]
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
      // A heading this build does not know is shown without a heading.
      group: entry.group && GROUPS.includes(entry.group) ? entry.group : '',
    }))
}

/**
 * Splits the menu into sections for the sidebar, in the server's order
 * (entries of one group are adjacent). Entries that live in the user menu
 * are left out.
 */
export function sidebarSections(items: readonly NavItem[]): NavSection[] {
  const sections: NavSection[] = []
  for (const item of items) {
    if (USER_MENU_KEYS.has(item.key)) continue
    const last = sections.at(-1)
    if (last && last.group === item.group) last.items.push(item)
    else sections.push({ group: item.group, items: [item] })
  }
  return sections
}

/** Where a signed-in user lands: the first menu entry, or settings. */
export function homeLocation(router: Router): RouteLocationRaw {
  return resolveNav(useNavStore().entries, router)[0]?.to ?? { name: 'settings' }
}
