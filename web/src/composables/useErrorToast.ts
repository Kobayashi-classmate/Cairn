// SPDX-License-Identifier: AGPL-3.0-only
import { useMessage } from 'naive-ui'
import { isAbortError, isStepUpRequired } from '@/api/client'
import { errorMessage } from '@/i18n/errors'

/**
 * Shows an API error as a toast. Cancelled requests and a declined
 * re-authentication prompt are not errors from the user's point of view and
 * are not shown.
 */
export function useErrorToast(): (err: unknown) => void {
  const message = useMessage()
  return (err: unknown) => {
    if (isAbortError(err) || isStepUpRequired(err)) return
    message.error(errorMessage(err))
  }
}
