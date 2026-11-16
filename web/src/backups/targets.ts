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
