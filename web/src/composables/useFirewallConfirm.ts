// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage } from 'naive-ui'
import { isApiError } from '@/api/client'
import { confirmChange, type FirewallPending } from '@/api/firewall'
import { errorMessage } from '@/i18n/errors'

/** Wait after the change before the first try: the server closes the old connection. */
const FIRST_TRY_MS = 1000
const RETRY_MS = 1500

/** Errors that mean "try again": not reachable yet, or the server asks to retry. */
function retryable(err: unknown): boolean {
  if (!isApiError(err)) return true
  return err.code === 'firewall.confirm_retry' || err.status === 0 || err.status >= 500
}

/**
 * Confirms a firewall change. A change is rolled back unless confirmed over
 * a new connection before it expires, which proves the panel is still
 * reachable under the new rules: this tries until it succeeds or the change
 * expires, showing the time left. `onDone` runs either way (to reload).
 * Nothing is confirmed on its own after a page load: callers start it.
 */
export function useFirewallConfirm(onDone: (confirmed: boolean) => void) {
  const { t } = useI18n()
  const message = useMessage()
  const pending = ref<FirewallPending | null>(null)
  const now = ref(Date.now())
  let ticker: ReturnType<typeof setInterval> | undefined
  let stopped = false
  let run = 0

  /** Seconds left before the change is rolled back. */
  const remaining = computed(() => (pending.value ? Math.max(0, Math.ceil(pending.value.expires - now.value / 1000)) : 0))
  const checking = computed(() => pending.value !== null)

  function sleep(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms))
  }

  function finish(confirmed: boolean) {
    pending.value = null
    clearInterval(ticker)
    if (!stopped) onDone(confirmed)
  }

  /** Runs the confirmation for a change; resolves true when it was kept. */
  async function confirm(change: FirewallPending): Promise<boolean> {
    const mine = ++run
    pending.value = change
    now.value = Date.now()
    clearInterval(ticker)
    ticker = setInterval(() => (now.value = Date.now()), 500)
    await sleep(FIRST_TRY_MS)
    // Leaving the page does not stop it: an unconfirmed change would be rolled back.
    while (mine === run && Date.now() / 1000 < change.expires) {
      try {
        await confirmChange(change.id)
        message.success(t('firewall.confirm.kept'))
        finish(true)
        return true
      } catch (err) {
        if (isApiError(err) && err.code === 'firewall.no_pending') break
        if (!retryable(err)) {
          message.error(errorMessage(err))
          break
        }
      }
      await sleep(RETRY_MS)
    }
    if (mine !== run) return false
    message.warning(t('firewall.confirm.rolledBack'), { duration: 8000 })
    finish(false)
    return false
  }

  // The page is gone: keep confirming, but stop updating it.
  onBeforeUnmount(() => {
    stopped = true
    clearInterval(ticker)
  })

  return { pending, checking, remaining, confirm }
}
