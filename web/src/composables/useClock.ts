// SPDX-License-Identifier: AGPL-3.0-only
import { readonly, ref, type Ref } from 'vue'

/** Refresh interval of the shared clock; relative times change at most this often. */
const TICK_MS = 30_000

const now = ref(Math.floor(Date.now() / 1000))
let timer: ReturnType<typeof setInterval> | undefined

/**
 * Current Unix time in seconds from one clock shared by every relative time
 * on the page, refreshed twice a minute. Cheaper than a timer per row.
 */
export function useClock(): Readonly<Ref<number>> {
  if (timer === undefined) {
    timer = setInterval(() => {
      now.value = Math.floor(Date.now() / 1000)
    }, TICK_MS)
  }
  return readonly(now)
}
