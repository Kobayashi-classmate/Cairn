// SPDX-License-Identifier: AGPL-3.0-only
import type { BackupTarget } from '@/api/backups'
import { engineIdLabel } from '@/databases/labels'
import { absolutePath } from '@/files/paths'
import { i18n } from '@/i18n'

/** What a backup target is, in words: "Site example", "Database shop (MySQL/MariaDB)", "/srv/www/x". */
export function targetLabel(target: BackupTarget): string {
  const { t } = i18n.global
  switch (target.kind) {
    case 'site':
      return t('backups.target.site', { site: target.site ?? '' })
    case 'database':
      return t('backups.target.database', { database: target.database ?? '', engine: engineIdLabel(target.engine ?? '') })
    case 'directory':
      return absolutePath(target.root ?? '', target.path ?? '')
  }
}

/** The key the server names a target's backup files with (see BackupTarget.Key on the server). */
export function targetKey(target: BackupTarget): string {
  switch (target.kind) {
    case 'site':
      return `site-${target.site}`
    case 'database':
      return `db-${target.engine}-${target.database}`
    case 'directory': {
      const key = target.path ? `${target.root}-${target.path}` : (target.root ?? '')
      return `dir-${key.replace(/[/.]/g, '-')}`
    }
  }
}

/**
 * Whether a plan backs up folders outside the sites root (the backups or
 * the system root). Changing such a plan, downloading its backups and
 * restoring them read or write there as root: like opening those roots in
 * the file manager, it takes `files.admin`.
 */
export function reachesBeyondSites(plan: { targets: readonly BackupTarget[] }): boolean {
  return plan.targets.some((target) => target.kind === 'directory' && target.root !== 'sites')
}

/**
 * Whether a backup file holds a folder outside the sites root, by its name
 * (the target's key; see targetKey): its contents are as sensitive as the
 * folder, so downloading and restoring it take `files.admin`. Names that
 * are not a site, a database or a folder of the sites root count as such.
 */
export function fileReachesBeyondSites(name: string): boolean {
  return !['site-', 'db-', 'dir-sites-', 'dir-sites_'].some((prefix) => name.startsWith(prefix))
}

/** The file manager roots this account may back up or restore into: all of them with `files.admin`, else the sites. */
export function backupRoots(canAdminFiles: boolean): readonly string[] {
  return canAdminFiles ? ['sites', 'backups', 'system'] : ['sites']
}
