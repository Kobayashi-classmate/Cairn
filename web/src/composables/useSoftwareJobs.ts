// SPDX-License-Identifier: AGPL-3.0-only
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { isApiError } from '@/api/client'
import { fetchJob, fetchJobs, type Job } from '@/api/jobs'
import { isFinalState } from '@/jobs/labels'

/** Job kinds that change what is installed. */
const SOFTWARE_KINDS: ReadonlySet<string> = new Set(['software.install', 'software.remove'])
const POLL_MS = 3000

export interface RunningSoftwareJob {
  id: string
  kind: string
  /** Catalog id of the software the job acts on. */
  software: string
  line: string
}

/**
 * Tracks install and remove jobs that are still running, including ones
 * started elsewhere (another tab or user), and reports each one when it ends
 * so the page can reload what is installed.
 */
export function useSoftwareJobs(onFinished: (job: Job) => void) {
  /** Running jobs by job id. */
  const running = ref<Record<string, RunningSoftwareJob>>({})
  let timer: ReturnType<typeof setTimeout> | undefined
  let unmounted = false

  function track(job: Job) {
    const software = job.params.software
    if (!SOFTWARE_KINDS.has(job.kind) || typeof software !== 'string' || isFinalState(job.state)) return
    running.value = {
      ...running.value,
      [job.id]: { id: job.id, kind: job.kind, software, line: String(job.params.line ?? '') },
    }
    schedule()
  }

  function untrack(id: string) {
    const next = { ...running.value }
    delete next[id]
    running.value = next
  }

  /** The running job for a software entry, if any. */
  function runningFor(software: string): RunningSoftwareJob | null {
    return Object.values(running.value).find((job) => job.software === software) ?? null
  }

  function schedule() {
    if (unmounted || timer !== undefined || Object.keys(running.value).length === 0) return
    timer = setTimeout(poll, POLL_MS)
  }

  async function poll() {
    timer = undefined
    for (const entry of Object.values(running.value)) {
      try {
        const job = await fetchJob(entry.id)
        if (isFinalState(job.state)) {
          untrack(entry.id)
          onFinished(job)
        }
      } catch (err) {
        // A job that disappeared is no longer running; other errors retry next round.
        if (isApiError(err) && err.status === 404) untrack(entry.id)
      }
      if (unmounted) return
    }
    schedule()
  }

  /** Finds software jobs that were already running when the page opened. */
  async function discover() {
    try {
      const page = await fetchJobs({ limit: 50 })
      for (const job of page.jobs) track(job)
    } catch {
      // Without the list the buttons are simply not locked; the helper still
      // runs one package operation at a time.
    }
  }

  onMounted(discover)
  onBeforeUnmount(() => {
    unmounted = true
    clearTimeout(timer)
  })

  return { running, track, runningFor }
}
