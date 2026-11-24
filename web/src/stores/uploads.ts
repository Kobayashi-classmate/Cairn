// SPDX-License-Identifier: AGPL-3.0-only
//
// File uploads (tus 1.0). Uploads live in a store so they go on while the
// user moves around the panel. Once the upload is complete (an empty file
// at once) the server starts a job that moves the file into place; the
// upload is done when that job is.
import { defineStore } from 'pinia'
import { computed, ref, shallowRef } from 'vue'
import type { DetailedError, Upload } from 'tus-js-client'
import {
  API_BASE,
  ApiError,
  ClientErrorCode,
  apiErrorFromBody,
  authHeaders,
  isAbortError,
  reportUnauthorized,
} from '@/api/client'
import { followJobLog } from '@/api/jobs'
import { ensureElevated } from '@/auth/stepUp'
import { isFinalState } from '@/jobs/labels'
import { onAccountReset } from './accountReset'

export type UploadState = 'queued' | 'uploading' | 'placing' | 'done' | 'failed' | 'canceled'

export interface UploadItem {
  key: number
  root: string
  dir: string
  name: string
  size: number
  /** Bytes the server has. */
  sent: number
  state: UploadState
  error: ApiError | null
  jobId: string | null
}

/** Uploads running at the same time; the others wait their turn. */
const PARALLEL = 3
/** Size of one PATCH request, small enough for common proxy limits. */
const CHUNK_BYTES = 64 * 1024 * 1024

interface Pending {
  file: File
  overwrite: boolean
  upload: Upload | null
  /** Ends the running transfer as aborted (tus-js-client stays silent on abort). */
  stop: (() => void) | null
  /** Stops waiting for the placing job. */
  controller: AbortController
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

/** An upload failure as an ApiError, the way the API client reports errors. */
function toApiError(err: Error | DetailedError): ApiError {
  const response = 'originalResponse' in err ? err.originalResponse : null
  if (!response) return new ApiError(0, ClientErrorCode.network)
  const error = apiErrorFromBody(response.getStatus(), response.getBody())
  if (error.status === 401) reportUnauthorized(error)
  return error
}

export const useUploadsStore = defineStore('uploads', () => {
  const items = ref<UploadItem[]>([])
  /** Bumped whenever an upload lands; `lastPlaced` says where. */
  const placedVersion = ref(0)
  const lastPlaced = shallowRef<{ root: string; dir: string } | null>(null)
  const pending = new Map<number, Pending>()
  let nextKey = 1
  let unloadGuard = false

  const active = computed(() => items.value.filter((item) => ['queued', 'uploading', 'placing'].includes(item.state)))

  function item(key: number): UploadItem | undefined {
    return items.value.find((entry) => entry.key === key)
  }

  function guardUnload() {
    if (unloadGuard) return
    unloadGuard = true
    window.addEventListener('beforeunload', (event) => {
      // Leaving the page drops uploads that have not finished.
      if (items.value.some((entry) => entry.state === 'uploading' || entry.state === 'queued')) event.preventDefault()
    })
  }

  /** Queues files for upload into `dir` of `root`. */
  function add(root: string, dir: string, files: readonly File[], overwrite: (name: string) => boolean) {
    guardUnload()
    for (const file of files) {
      const key = nextKey++
      items.value.push({ key, root, dir, name: file.name, size: file.size, sent: 0, state: 'queued', error: null, jobId: null })
      pending.set(key, { file, overwrite: overwrite(file.name), upload: null, stop: null, controller: new AbortController() })
    }
    pump()
  }

  /** Starts queued uploads while fewer than PARALLEL run. */
  function pump() {
    let running = items.value.filter((entry) => entry.state === 'uploading').length
    for (const entry of items.value) {
      if (running >= PARALLEL) return
      if (entry.state !== 'queued') continue
      running++
      void run(entry.key)
    }
  }

  /** The state as it is now (TypeScript would keep it narrowed across awaits). */
  function stateOf(entry: UploadItem): UploadState {
    return entry.state
  }

  async function run(key: number) {
    const entry = item(key)
    const job = pending.get(key)
    if (!entry || !job) return
    entry.state = 'uploading'
    try {
      // Roots other than the sites take a recent re-authentication, which
      // the upload client cannot ask for and retry by itself.
      if (entry.root !== 'sites') await ensureElevated()
      if (stateOf(entry) === 'canceled') return
      const jobId = await transfer(entry, job)
      if (!jobId) throw new ApiError(0, ClientErrorCode.badResponse)
      entry.jobId = jobId
      entry.state = 'placing'
      pump()
      await waitForJob(entry, jobId, job.controller.signal)
    } catch (err) {
      if (stateOf(entry) === 'canceled' || isAbortError(err)) return
      entry.state = 'failed'
      entry.error = err instanceof ApiError ? err : new ApiError(0, ClientErrorCode.network)
    } finally {
      // Failed uploads keep their file so they can be tried again.
      if (stateOf(entry) === 'done') pending.delete(key)
      pump()
    }
  }

  async function transfer(entry: UploadItem, job: Pending): Promise<string | null> {
    // Cancelling while the upload code loads must stop it too: the stop
    // handle exists before the first await, and is checked after it.
    let stopped = false
    job.stop = () => {
      stopped = true
    }
    const tus = await import('tus-js-client')
    if (stopped || job.controller.signal.aborted) throw new DOMException('Upload canceled', 'AbortError')
    let jobId: string | null = null
    await new Promise<void>((resolve, reject) => {
      const metadata: Record<string, string> = { root: entry.root, dir: entry.dir, filename: entry.name }
      if (job.overwrite) metadata.overwrite = 'true'
      const instance: Upload = new tus.Upload(job.file, {
        // Absolute, so the upload's address (Location) resolves against it.
        endpoint: new URL(`${API_BASE}/files/uploads`, window.location.origin).toString(),
        // The session key, read for each request: an upload can outlast a
        // sign-in.
        onBeforeRequest: (request) => {
          for (const [name, value] of Object.entries(authHeaders(request.getMethod()))) request.setHeader(name, value)
        },
        metadata,
        chunkSize: CHUNK_BYTES,
        retryDelays: [0, 1000, 3000, 5000, 10000],
        // A resumed upload would keep its old target directory; start afresh.
        storeFingerprintForResuming: false,
        onProgress: (sent) => {
          entry.sent = sent
        },
        onAfterResponse: (_request, response) => {
          // The response that completes the upload names the job placing the
          // file: the last part's, or the creation's for an empty file.
          const id = response.getHeader('Cairn-Job')
          if (id) jobId = id
        },
        onShouldRetry: (err) => {
          const status = err.originalResponse?.getStatus() ?? 0
          // Terminating (DELETE, when cancelled) is not retried: a 409 there
          // means the upload is already being placed or gone.
          if (err.originalRequest?.getMethod() === 'DELETE') return status === 0
          // Network trouble, a conflict over the offset (resolved by asking
          // the server), or a server error other than a full disk.
          return status === 0 || status === 409 || status === 423 || (status >= 500 && status !== 507)
        },
        onError: (err) => reject(toApiError(err)),
        onSuccess: () => resolve(),
      })
      job.upload = instance
      job.stop = () => reject(new DOMException('Upload canceled', 'AbortError'))
      instance.start()
    })
    // Cancelled as the last part went: the file is placed anyway, but this
    // upload no longer follows it.
    if (job.controller.signal.aborted) throw new DOMException('Upload canceled', 'AbortError')
    entry.sent = entry.size
    return jobId
  }

  async function waitForJob(entry: UploadItem, jobId: string, signal: AbortSignal) {
    let offset = 0
    for (;;) {
      try {
        const chunk = await followJobLog(jobId, offset, 25, signal)
        offset = chunk.offset
        if (isFinalState(chunk.job.state)) {
          if (chunk.job.state === 'succeeded') {
            entry.state = 'done'
            lastPlaced.value = { root: entry.root, dir: entry.dir }
            placedVersion.value++
          } else {
            entry.state = 'failed'
            const error = chunk.job.error
            const fallback = chunk.job.state === 'canceled' ? 'op.canceled' : chunk.job.state === 'interrupted' ? 'op.interrupted' : 'op.failed'
            entry.error = new ApiError(0, error?.code ?? fallback, error?.params ?? {})
          }
          return
        }
      } catch (err) {
        if (isAbortError(err)) throw err
        if (err instanceof ApiError && err.status >= 400 && err.status < 500) throw err
        await sleep(3000)
      }
    }
  }

  /** Stops an upload and has the server drop what arrived. */
  async function cancel(key: number) {
    const entry = item(key)
    const job = pending.get(key)
    if (!entry || (entry.state !== 'queued' && entry.state !== 'uploading')) return
    entry.state = 'canceled'
    pending.delete(key)
    job?.controller.abort()
    job?.stop?.()
    try {
      await job?.upload?.abort(true)
    } catch {
      // The server drops abandoned uploads after a day anyway.
    }
    pump()
  }

  /** Tries a failed upload again from the start; `overwrite` replaces an existing file. */
  function retry(key: number, overwrite?: boolean) {
    const entry = item(key)
    if (!entry || entry.state !== 'failed') return
    const job = pending.get(key)
    if (!job) return
    if (overwrite !== undefined) job.overwrite = overwrite
    job.upload = null
    job.stop = null
    entry.jobId = null
    entry.state = 'queued'
    entry.error = null
    entry.sent = 0
    pump()
  }

  /** Removes finished, failed and canceled uploads from the list. */
  function clearFinished() {
    items.value = items.value.filter((entry) => ['queued', 'uploading', 'placing'].includes(entry.state))
    for (const key of [...pending.keys()]) {
      if (!items.value.some((entry) => entry.key === key)) pending.delete(key)
    }
  }

  /** Whether a failed upload can be tried again. */
  function canRetry(key: number): boolean {
    return pending.has(key)
  }

  /**
   * Forgets every upload (sign-out, the session ended): running ones stop,
   * and no file of this account can be retried under another one.
   */
  function reset() {
    const jobs = [...pending.values()]
    pending.clear()
    for (const entry of items.value) {
      if (entry.state === 'queued' || entry.state === 'uploading' || entry.state === 'placing') entry.state = 'canceled'
    }
    items.value = []
    for (const job of jobs) {
      job.controller.abort()
      job.stop?.()
      // Not terminated on the server: the session that owned it is gone; the
      // server drops abandoned uploads after a day.
      void job.upload?.abort(false).catch(() => {})
    }
  }

  onAccountReset(reset)

  return { items, active, placedVersion, lastPlaced, add, cancel, retry, canRetry, clearFinished, reset }
})
