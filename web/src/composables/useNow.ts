// SPDX-License-Identifier: AGPL-3.0-only
import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'

/** Current Unix time in seconds, refreshed every `intervalMs` while mounted. */
export function useNow(intervalMs = 1000): Readonly<Ref<number>> {
  const now = ref(Math.floor(Date.now() / 1000))
  let timer: ReturnType<typeof setInterval> | undefined
  onMounted(() => {
    timer = setInterval(() => {
      now.value = Math.floor(Date.now() / 1000)
    }, intervalMs)
  })
  onBeforeUnmount(() => clearInterval(timer))
  return now
}
