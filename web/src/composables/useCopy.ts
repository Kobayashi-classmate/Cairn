// SPDX-License-Identifier: AGPL-3.0-only
import { useI18n } from 'vue-i18n'
import { useMessage } from 'naive-ui'

/** Copies text to the clipboard and reports the outcome as a toast. */
export function useCopy(): (text: string) => Promise<void> {
  const { t } = useI18n()
  const message = useMessage()
  return async (text) => {
    try {
      await navigator.clipboard.writeText(text)
      message.success(t('common.copy.done'))
    } catch {
      // The Clipboard API needs a secure context and permission.
      message.error(t('common.copy.failed'))
    }
  }
}
