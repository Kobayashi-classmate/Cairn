// SPDX-License-Identifier: AGPL-3.0-only
import { onBeforeUnmount, onMounted } from 'vue'

/**
 * Runs `task` now and then every `intervalMs` while the component is mounted
 * and the browser tab is visible. Runs never overlap: the next one is
 * scheduled after the previous one settles. Hiding the tab aborts the run in
 * flight and pauses polling; showing it again runs `task` immediately.
 *
 * `task` should handle its own errors; a rejection only ends that run.
 */
export function usePolling(task: (signal: AbortSignal) => Promise<void>, intervalMs: number): void {
  let mounted = false
  let timer: ReturnType<typeof setTimeout> | undefined
  let inFlight: AbortController | null = null

  const visible = () => document.visibilityState === 'visible'

  function clearTimer() {
    if (timer !== undefined) {
      clearTimeout(timer)
      timer = undefined
    }
  }

  async function run() {
    clearTimer()
    if (!mounted || inFlight || !visible()) return
    const controller = new AbortController()
    inFlight = controller
    try {
      await task(controller.signal)
    } catch {
      // Errors are the task's concern; aborts are expected.
    } finally {
      inFlight = null
    }
    if (mounted && visible()) {
      // An aborted run means the tab was hidden and is visible again: catch up now.
      timer = setTimeout(run, controller.signal.aborted ? 0 : intervalMs)
    }
  }

  function onVisibilityChange() {
    if (visible()) {
      void run()
    } else {
      clearTimer()
      inFlight?.abort()
    }
  }

  onMounted(() => {
    mounted = true
    document.addEventListener('visibilitychange', onVisibilityChange)
    void run()
  })

  onBeforeUnmount(() => {
    mounted = false
    document.removeEventListener('visibilitychange', onVisibilityChange)
    clearTimer()
    inFlight?.abort()
  })
}
