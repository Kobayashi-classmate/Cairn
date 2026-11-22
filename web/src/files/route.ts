// SPDX-License-Identifier: AGPL-3.0-only
//
// Where the Files page is, in the address: `?tab=<root>&path=<folder>` for a
// root's folder, `?tab=trash&root=<root>` for a root's trash. Older links
// used `?root=<root>&path=<folder>` and `?root=<root>&trash=1`.
import type { LocationQuery } from 'vue-router'

export type FilesTab = 'sites' | 'backups' | 'system' | 'trash'
export const FILES_TABS: readonly FilesTab[] = ['sites', 'backups', 'system', 'trash']

/** The Files page in the old form of the address, rewritten; null when it is already new. */
export function legacyFilesQuery(query: LocationQuery): LocationQuery | null {
  if (query.tab !== undefined) return null
  const root = typeof query.root === 'string' ? query.root : ''
  const trash = query.trash === '1'
  if (!root && !trash) return null
  const rest: LocationQuery = { ...query }
  delete rest.root
  delete rest.trash
  delete rest.path
  if (trash) return { ...rest, tab: 'trash', ...(root ? { root } : {}) }
  const path = typeof query.path === 'string' ? query.path : ''
  return { ...rest, tab: root, ...(path ? { path } : {}) }
}
