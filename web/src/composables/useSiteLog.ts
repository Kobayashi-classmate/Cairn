// SPDX-License-Identifier: AGPL-3.0-only
import { onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { isAbortError } from '@/api/client'
import { fetchSiteLog, type SiteLogKind } from '@/api/sites'

/** Pause between reads while following. */
const POLL_MS = 2000
/** Lines kept; older ones are dropped. */
const MAX_LINES = 1000

const byteLength = (text: string) => new TextEncoder().encode(text).length

/**
 * Reads a site's access or error log: first its last part, then, while
 * following, what was added since (by offset). A file that shrank was
 * rotated and is read again from its start. Only whole lines are kept, at
 * most MAX_LINES of them.
 */
export function useSiteLog(code: Ref<string>, kind: Ref<SiteLogKind>, follow: Ref<boolean>) {
  const lines = ref<string[]>([])
  const error = ref<unknown>(null)
  const loading = ref(false)
  /** Older lines were dropped to stay within the limit. */
  const dropped = ref(false)

  /** Where the next read continues; null until the first read. */
  let offset: number | null = null
  /** Text after the last newline, completed by the next read. */
  let partial = ''
  let timer: ReturnType<typeof setTimeout> | undefined
  let controller: AbortController | null = null
  let stopped = false

  function append(data: string) {
    const parts = (partial + data).split('\n')
    partial = parts.pop() ?? ''
    const next = [...lines.value, ...parts.filter((line) => line !== '')]
    if (next.length > MAX_LINES) dropped.value = true
    lines.value = next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next
  }

  function reset() {
    clearTimeout(timer)
    controller?.abort()
    controller = null
    offset = null
    partial = ''
    lines.value = []
    dropped.value = false
    error.value = null
  }

  function schedule() {
    clearTimeout(timer)
    if (!stopped && follow.value && code.value) timer = setTimeout(() => void read(), POLL_MS)
  }

  async function read() {
    clearTimeout(timer)
    if (!code.value) return
    controller?.abort()
    const request = new AbortController()
    controller = request
    const requested = offset ?? -1
    if (offset === null) loading.value = true
    try {
      const chunk = await fetchSiteLog(code.value, kind.value, requested, request.signal)
      if (controller !== request) return
      if (requested >= 0 && chunk.size < requested) {
        // The file was rotated: start again from its beginning.
        partial = ''
        offset = 0
        timer = setTimeout(() => void read(), 0)
        return
      }
      let data = chunk.data
      // The last part of a file usually starts in the middle of a line.
      if (requested < 0 && chunk.offset - byteLength(data) > 0) {
        const newline = data.indexOf('\n')
        data = newline >= 0 ? data.slice(newline + 1) : ''
      }
      append(data)
      offset = chunk.offset
      error.value = null
    } catch (err) {
      if (isAbortError(err) || controller !== request) return
      error.value = err
    } finally {
      if (controller === request) {
        controller = null
        loading.value = false
      }
    }
    schedule()
  }

  function restart() {
    reset()
    void read()
  }

  watch([code, kind], restart, { immediate: true })

  watch(follow, (on) => {
    if (on) void read()
    else clearTimeout(timer)
  })

  onBeforeUnmount(() => {
    stopped = true
    clearTimeout(timer)
    controller?.abort()
  })

  return { lines, error, loading, dropped, retry: restart, read }
}
