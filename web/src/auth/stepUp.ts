// SPDX-License-Identifier: AGPL-3.0-only
//
// Re-authentication ("step-up") for sensitive actions. When the server answers
// `auth.step_up_required`, the API client calls `requestStepUp`, which opens
// StepUpDialog. Requests that need step-up at the same time share one dialog
// and are all retried (or all rejected) when it closes.
import { ref } from 'vue'
import { ApiError, STEP_UP_REQUIRED, serverNow, setStepUpHandler } from '@/api/client'
import { useSessionStore } from '@/stores/session'

interface Waiter {
  error: ApiError
  resolve: () => void
  reject: (reason: unknown) => void
}

let waiters: Waiter[] = []

/** Whether the re-authentication dialog is open. */
export const stepUpOpen = ref(false)

function requestStepUp(error: ApiError): Promise<void> {
  // The server asked for it, so the panel's switch is on (it may have been
  // turned on elsewhere since this page learned it).
  useSessionStore().setStepUpRequired(true)
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

/**
 * Makes sure the session is re-authenticated before a step-up protected
 * request the API client cannot retry itself, such as a download through a
 * plain link or an upload with progress. Does nothing while re-authentication
 * is turned off for the panel. Rejects when the user cancels.
 */
export async function ensureElevated(): Promise<void> {
  const session = useSessionStore()
  if (!session.stepUpRequired) return
  // Keep a margin so the elevation does not run out during the request;
  // elevatedUntil is on the server's clock.
  if (session.elevatedUntil > serverNow() / 1000 + 30) return
  await requestStepUp(new ApiError(403, STEP_UP_REQUIRED))
}
