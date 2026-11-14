// SPDX-License-Identifier: AGPL-3.0-only
import { h } from 'vue'
import { useI18n } from 'vue-i18n'
import { safeHttpsUrl } from '@/markdown/parse'
import { useConfirm } from './useConfirm'

/**
 * Opens an https:// link from external content (official messages, release
 * notes) in a new tab, after showing the full address and asking to confirm.
 * Any other scheme is refused.
 */
export function useExternalLink(): (href: string) => Promise<void> {
  const { t } = useI18n()
  const confirm = useConfirm()
  return async (href) => {
    const url = safeHttpsUrl(href)
    if (!url) return
    const ok = await confirm({
      title: t('common.externalLink.title'),
      content: () =>
        h('div', [
          h('p', { style: 'margin: 0 0 8px' }, t('common.externalLink.content')),
          h(
            'code',
            { dir: 'ltr', style: 'display: block; overflow-wrap: anywhere; user-select: all' },
            url,
          ),
        ]),
      confirmText: t('common.externalLink.open'),
    })
    if (ok) window.open(url, '_blank', 'noopener,noreferrer')
  }
}
