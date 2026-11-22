// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { isApiError } from '@/api/client'
import { confirmChange, rollbackChange, type ChangeResult, type FirewallPending } from '@/api/firewall'
import { errorMessage } from '@/i18n/errors'
import { notifySuccess } from '@/lib/notify'

/** Wait after the change before the first try: the server closes the old connection. */
const FIRST_TRY_MS = 1000
const RETRY_MS = 1500
/** After the change would be rolled back, give the server's timer a moment before reloading. */
const ROLLBACK_SETTLE_MS = 3000
/** How long a kept change can be undone from the banner. */
const UNDO_MS = 5 * 60 * 1000

/** Errors that mean "try again": not reachable yet, or the server asks to retry. */
function retryable(err: unknown): boolean {
  if (!isApiError(err)) return true
  return err.code === 'firewall.confirm_retry' || err.status === 0 || err.status >= 500
}

/** What the page knows about a change it made. */
export interface ChangeInfo {
  /** What the change does, in a few words ("Rule added: allow 8081 (TCP)"). */
  summary?: string
  /** Makes the opposite change, once this one was kept. */
  undo?: () => Promise<ChangeResult>
}

/** A change that was kept, for the "saved" banner. */
export interface KeptChange extends ChangeInfo {
  kind: string
}

/**
 * Confirms a firewall change. A change is rolled back unless confirmed over
 * a new connection before it expires, which proves the panel is still
 * reachable under the new rules: this tries until it succeeds or the change
 * expires, showing the time left. `onDone` runs either way (to reload).
 * Nothing is confirmed on its own after a page load: callers start it.
 *
 * `abandon` rolls the change back at once; when the server cannot be asked
 * now, it still rolls the change back when its time runs out.
 */
export function useFirewallConfirm(onDone: (confirmed: boolean) => void) {
  const { t } = useI18n()
  /** The change being confirmed. */
  const pending = ref<FirewallPending | null>(null)
  const info = ref<ChangeInfo | null>(null)
  /** A change no longer confirmed on purpose, while it is being rolled back. */
  const abandoned = ref<FirewallPending | null>(null)
  /** The last change kept from this page, while it can still be undone. */
  const kept = ref<KeptChange | null>(null)
  const now = ref(Date.now())
  let ticker: ReturnType<typeof setInterval> | undefined
  let settle: ReturnType<typeof setTimeout> | undefined
  let keptTimer: ReturnType<typeof setTimeout> | undefined
  let stopped = false
  let run = 0

  /** Seconds left before the change (being confirmed or abandoned) is rolled back. */
  const remaining = computed(() => {
    const change = pending.value ?? abandoned.value
    return change ? Math.max(0, Math.ceil(change.expires - now.value / 1000)) : 0
  })
  const checking = computed(() => pending.value !== null)
  /** A change is under way: confirming, or waiting to be rolled back. */
  const busy = computed(() => pending.value !== null || abandoned.value !== null)

  function sleep(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms))
  }

  function tick() {
    clearInterval(ticker)
    now.value = Date.now()
    ticker = setInterval(() => (now.value = Date.now()), 500)
  }

  function finish(confirmed: boolean) {
    pending.value = null
    clearInterval(ticker)
    if (!stopped) onDone(confirmed)
  }

  function dismissKept() {
    clearTimeout(keptTimer)
    kept.value = null
  }

  /** Runs the confirmation for a change; resolves true when it was kept. */
  async function confirm(change: FirewallPending, about: ChangeInfo = {}): Promise<boolean> {
    const mine = ++run
    clearTimeout(settle)
    abandoned.value = null
    dismissKept()
    pending.value = change
    info.value = about
    tick()
    await sleep(FIRST_TRY_MS)
    // Leaving the page does not stop it: an unconfirmed change would be rolled back.
    while (mine === run && Date.now() / 1000 < change.expires) {
      try {
        await confirmChange(change.id)
        // Kept, even if "roll back" was asked for meanwhile: say so, and
        // stop any other attempt for the same change.
        run++
        clearTimeout(settle)
        abandoned.value = null
        notifySuccess(t('firewall.confirm.kept'))
        kept.value = { kind: change.kind, ...about }
        keptTimer = setTimeout(dismissKept, UNDO_MS)
        finish(true)
        return true
      } catch (err) {
        if (isApiError(err) && err.code === 'firewall.no_pending') break
        if (!retryable(err)) {
          toast.error(errorMessage(err))
          break
        }
      }
      await sleep(RETRY_MS)
    }
    if (mine !== run) return false
    toast.warning(t('firewall.confirm.rolledBack'), { duration: 8000 })
    finish(false)
    return false
  }

  /** Stops confirming the current change and rolls it back now. */
  async function abandon() {
    const change = pending.value
    if (!change) return
    run++
    pending.value = null
    abandoned.value = change
    try {
      await rollbackChange(change.id)
      done()
      return
    } catch (err) {
      // Gone already (rolled back, or kept just before): show what is there.
      if (isApiError(err) && err.code === 'firewall.no_pending') {
        done(false)
        return
      }
    }
    // The server could not be asked: its timer rolls the change back.
    const wait = Math.max(0, change.expires * 1000 - Date.now()) + ROLLBACK_SETTLE_MS
    settle = setTimeout(() => {
      abandoned.value = null
      clearInterval(ticker)
      if (stopped) return
      toast.warning(t('firewall.confirm.undone'))
      onDone(false)
    }, wait)
  }

  /** The rollback finished: tell the page to reload. */
  function done(announce = true) {
    if (abandoned.value === null) return
    abandoned.value = null
    clearInterval(ticker)
    if (stopped) return
    if (announce) notifySuccess(t('firewall.confirm.undone'))
    onDone(false)
  }

  /** Rolls back a change made elsewhere (another tab, another user). */
  async function rollBackForeign(change: FirewallPending): Promise<void> {
    try {
      await rollbackChange(change.id)
      notifySuccess(t('firewall.confirm.undone'))
    } catch (err) {
      if (!isApiError(err) || err.code !== 'firewall.no_pending') toast.error(errorMessage(err))
    }
    if (!stopped) onDone(false)
  }

  // The page is gone: keep confirming, but stop updating it.
  onBeforeUnmount(() => {
    stopped = true
    clearInterval(ticker)
    clearTimeout(settle)
    clearTimeout(keptTimer)
  })

  return { pending, info, abandoned, kept, checking, busy, remaining, confirm, abandon, rollBackForeign, dismissKept }
}
