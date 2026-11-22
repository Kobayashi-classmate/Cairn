// SPDX-License-Identifier: AGPL-3.0-only
import { useI18n } from 'vue-i18n'
import { safeHttpsUrl } from '@/markdown/parse'
import { confirmAction } from './useConfirmDialog'

/**
 * Opens an https:// link from external content (official messages, release
 * notes) in a new tab, after showing the full address and asking to confirm.
 * Any other scheme is refused.
 */
export function useExternalLink(): (href: string) => Promise<void> {
  const { t } = useI18n()
  return async (href) => {
    const url = safeHttpsUrl(href)
    if (!url) return
    const ok = await confirmAction({
      title: t('common.externalLink.title'),
      description: t('common.externalLink.content'),
      detail: url,
      confirmLabel: t('common.externalLink.open'),
    })
    if (ok) window.open(url, '_blank', 'noopener,noreferrer')
  }
}
