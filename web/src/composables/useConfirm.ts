// SPDX-License-Identifier: AGPL-3.0-only
import { useI18n } from 'vue-i18n'
import { useDialog } from 'naive-ui'

export interface ConfirmOptions {
  title: string
  content: string
  /** Label of the confirming button. */
  confirmText: string
  /** Style the confirming button as destructive. */
  danger?: boolean
}

/** Asks for confirmation; resolves true when confirmed, false when dismissed in any way. */
export function useConfirm(): (options: ConfirmOptions) => Promise<boolean> {
  const dialog = useDialog()
  const { t } = useI18n()
  return (options) =>
    new Promise<boolean>((resolve) => {
      dialog.warning({
        title: options.title,
        content: options.content,
        positiveText: options.confirmText,
        negativeText: t('common.action.cancel'),
        positiveButtonProps: options.danger ? { type: 'error' } : undefined,
        onPositiveClick: () => resolve(true),
        // Covers the cancel button, the close icon, Esc and the mask.
        onAfterLeave: () => resolve(false),
      })
    })
}
