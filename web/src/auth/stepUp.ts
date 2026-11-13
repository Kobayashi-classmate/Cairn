// SPDX-License-Identifier: AGPL-3.0-only
//
// Re-authentication ("step-up") for sensitive actions. When the server answers
// `auth.step_up_required`, the API client calls `requestStepUp`, which opens
// StepUpDialog. Requests that need step-up at the same time share one dialog
// and are all retried (or all rejected) when it closes.
import { ref } from 'vue'
import { setStepUpHandler, type ApiError } from '@/api/client'

interface Waiter {
  error: ApiError
  resolve: () => void
  reject: (reason: unknown) => void
}

let waiters: Waiter[] = []

/** Whether the re-authentication dialog is open. */
export const stepUpOpen = ref(false)

function requestStepUp(error: ApiError): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    waiters.push({ error, resolve, reject })
    stepUpOpen.value = true
  })
}

/** Closes the dialog; on success the waiting requests are retried, otherwise they fail. */
export function finishStepUp(elevated: boolean): void {
  const pending = waiters
  waiters = []
  stepUpOpen.value = false
  for (const waiter of pending) {
    if (elevated) waiter.resolve()
    else waiter.reject(waiter.error)
  }
}

export function installStepUp(): void {
  setStepUpHandler(requestStepUp)
}
