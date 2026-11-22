// SPDX-License-Identifier: AGPL-3.0-only
import { onBeforeUnmount, ref } from 'vue'
import { isAbortError, isApiError } from '@/api/client'
import { followJobLog, type Job } from '@/api/jobs'
import { isFinalState } from '@/jobs/labels'

/** Longest wait the server allows for one long-poll request. */
const WAIT_SECONDS = 25
/** Pause before retrying after a failed request. */
const RETRY_MS = 3000
/** Keep at most this many characters of log in memory; older output is dropped. */
const MAX_LOG_CHARS = 1024 * 1024

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const onAbort = () => {
      clearTimeout(timer)
      resolve()
    }
    const timer = setTimeout(() => {
      // The signal lives as long as the follow: do not pile up listeners on it.
      signal.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

/**
 * Follows a job's state and log with long polling until the job is final and
 * its log complete, or until stopped (also when the component unmounts).
 * The log is plain text; only its most recent part is kept in memory.
 */
export function useJobFollow() {
  const job = ref<Job | null>(null)
  const log = ref('')
  /** Earlier output was dropped to stay within the memory cap. */
  const trimmed = ref(false)
  const loadError = ref<unknown>(null)
  /** The last request failed; following keeps retrying. */
  const reconnecting = ref(false)

  let controller: AbortController | null = null

  function append(chunk: string) {
    if (!chunk) return
    let next = log.value + chunk
    if (next.length > MAX_LOG_CHARS) {
      next = next.slice(next.length - MAX_LOG_CHARS)
      trimmed.value = true
    }
    log.value = next
  }

  async function run(id: string, signal: AbortSignal) {
    let offset = 0
    let first = true
    while (!signal.aborted) {
      const started = Date.now()
      try {
        const chunk = await followJobLog(id, offset, first ? 0 : WAIT_SECONDS, signal)
        first = false
        reconnecting.value = false
        loadError.value = null
        const changed = chunk.log !== '' || chunk.job.state !== job.value?.state
        job.value = chunk.job
        append(chunk.log)
        offset = chunk.offset
        if (isFinalState(chunk.job.state) && offset >= chunk.logSize) return
        // Guard against a server that answers immediately without news.
        if (!changed && Date.now() - started < 500) await sleep(1000, signal)
      } catch (err) {
        if (isAbortError(err)) return
        if (isApiError(err) && (err.status === 404 || err.status === 401 || err.status === 403)) {
          loadError.value = err
          return
        }
        // Network trouble or a restarting panel: keep trying quietly.
        reconnecting.value = true
        if (!job.value) loadError.value = err
        await sleep(RETRY_MS, signal)
      }
    }
  }

  /** Starts following a job, dropping whatever was followed before. */
  function start(id: string) {
    controller?.abort()
    const current = new AbortController()
    controller = current
    job.value = null
    log.value = ''
    trimmed.value = false
    loadError.value = null
    reconnecting.value = false
    void run(id, current.signal)
  }

  function stop() {
    controller?.abort()
    controller = null
  }

  onBeforeUnmount(stop)

  return { job, log, trimmed, loadError, reconnecting, start, stop }
}
