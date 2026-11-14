// SPDX-License-Identifier: AGPL-3.0-only
import { onBeforeUnmount, onMounted } from 'vue'
import { useAnnouncementsStore } from '@/stores/announcements'

const POLL_INTERVAL_MS = 5 * 60_000
/** Returning to the window refreshes at most this often. */
const FOCUS_MIN_AGE_MS = 30_000

/** Keeps official messages fresh while the signed-in layout is shown. */
export function useAnnouncementPolling(): void {
  const store = useAnnouncementsStore()
  let timer: ReturnType<typeof setInterval> | undefined

  const onFocus = () => store.refreshIfStale(FOCUS_MIN_AGE_MS)

  onMounted(() => {
    void store.refresh()
    timer = setInterval(() => void store.refresh(), POLL_INTERVAL_MS)
    window.addEventListener('focus', onFocus)
  })

  onBeforeUnmount(() => {
    clearInterval(timer)
    window.removeEventListener('focus', onFocus)
    // The layout goes away when the user signs out.
    store.reset()
  })
}
