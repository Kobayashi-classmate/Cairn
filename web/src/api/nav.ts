// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './client'

/** A sider entry as the server describes it. */
export interface NavEntry {
  key: string
  /** i18n key of the label, for example "common.nav.jobs". */
  labelKey: string
  /** Icon name, mapped to an icon component by the client (unknown names get a generic icon). */
  icon: string
  /** Name of the client route to open. */
  route: string
  permission?: string
  /** Menu heading the entry belongs to; empty or missing for entries above every heading. */
  group?: NavGroup | ''
  order: number
}

/** Menu headings, in the order the server sorts them. */
export type NavGroup = 'apps' | 'ops' | 'security' | 'system'

/**
 * Menu entries of the enabled modules the caller may open, already filtered
 * and sorted by the server.
 */
export async function fetchNav(): Promise<NavEntry[]> {
  return (await api.get<{ items: NavEntry[] }>('/ui/nav')).items
}
