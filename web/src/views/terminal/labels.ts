// SPDX-License-Identifier: AGPL-3.0-only
import { i18n } from '@/i18n'

/** Whom a terminal runs as: "cw-shop (shop)" for a site owner, the name otherwise. */
export function userLabel(user: string, site?: string): string {
  return site ? i18n.global.t('terminal.user.site', { user, site }) : user
}

/** Lines of a saved command. */
export function lineCount(command: string): number {
  return command.split('\n').filter((line, index, lines) => line !== '' || index < lines.length - 1).length
}
