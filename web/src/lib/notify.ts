// SPDX-License-Identifier: AGPL-3.0-only
//
// Short toasts for pages built with the new components (interface spec,
// section 7: bottom right, 3 seconds). Pages still on Naive UI keep using
// useErrorToast and useCopy.
import { toast } from 'vue-sonner'
import { isAbortError, isStepUpRequired } from '@/api/client'
import { i18n } from '@/i18n'
import { errorMessage } from '@/i18n/errors'

/**
 * Shows an API error. Cancelled requests and a declined re-authentication
 * are not errors from the user's point of view and stay silent.
 */
export function notifyError(err: unknown): void {
  if (isAbortError(err) || isStepUpRequired(err)) return
  toast.error(errorMessage(err))
}

/** Shows a problem found before any request was made (a file that cannot be uploaded). */
export function notifyErrorText(message: string): void {
  toast.error(message)
}

export function notifySuccess(message: string): void {
  toast.success(message)
}

export function notifyInfo(message: string): void {
  toast(message)
}

/** Copies text to the clipboard and reports the outcome. */
export async function copyText(text: string): Promise<void> {
  const { t } = i18n.global
  try {
    await navigator.clipboard.writeText(text)
    toast.success(t('common.copy.done'))
  } catch {
    // The Clipboard API needs a secure context and permission.
    toast.error(t('common.copy.failed'))
  }
}
