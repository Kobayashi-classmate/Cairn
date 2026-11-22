// SPDX-License-Identifier: AGPL-3.0-only
//
// The software catalog as offered on this system, shared by the software
// page, the software settings drawer (also opened from the databases page)
// and the install sheet. Jobs that change what is installed or how it runs
// lock the buttons of their software until they end, including jobs started
// elsewhere (another tab or user), and reload the catalog when they end.
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { isApiError, type ErrorParams } from '@/api/client'
import { fetchJob, fetchJobs, type Job } from '@/api/jobs'
import { fetchSoftware, type Repository, type SoftwareCatalog, type SoftwareEntry } from '@/api/software'
import { launchJob } from '@/jobs/dock'
import { isFinalState } from '@/jobs/labels'
import { onAccountReset } from './accountReset'

/** Job kinds that change what is installed or how it runs. */
const SOFTWARE_KINDS: ReadonlySet<string> = new Set([
  'software.install',
  'software.remove',
  'software.service',
  'software.extensions',
  'software.config',
])
const POLL_MS = 3000

export interface RunningSoftwareJob {
  id: string
  kind: string
  /** Catalog id of the software the job acts on. */
  software: string
  line: string
}

export const useSoftwareStore = defineStore('software', () => {
  const catalog = ref<SoftwareCatalog | null>(null)
  const loadError = ref<unknown>(null)
  const loading = ref(false)
  /** Running jobs by job id. */
  const running = ref<Record<string, RunningSoftwareJob>>({})

  /** Why the last service job of a line ("php@8.4") failed, with the service's output. */
  const serviceFailures = ref<Record<string, { code: string; params?: ErrorParams }>>({})

  /** The install sheet: the software and the line to start with. */
  const installOpen = ref(false)
  const installTarget = ref<{ software: string; line?: string } | null>(null)

  let timer: ReturnType<typeof setTimeout> | undefined
  let loadPromise: Promise<void> | null = null
  let loadAgain = false
  /** Bumped on reset, so answers read for the previous account are dropped. */
  let epoch = 0

  const software = computed<SoftwareEntry[]>(() => catalog.value?.software ?? [])
  const repos = computed<ReadonlyMap<string, Repository>>(
    () => new Map((catalog.value?.repos ?? []).map((repo) => [repo.id, repo])),
  )

  /** Loads the catalog; a request while one is on its way loads once more after it. */
  function load(): Promise<void> {
    if (loadPromise) {
      loadAgain = true
      return loadPromise
    }
    const mine = epoch
    loadPromise = (async () => {
      loading.value = true
      try {
        const loaded = await fetchSoftware()
        // Read for an account that has signed out since: dropped.
        if (mine === epoch) {
          catalog.value = loaded
          loadError.value = null
        }
      } catch (err) {
        if (mine === epoch) loadError.value = err
      } finally {
        loading.value = false
        loadPromise = null
      }
      if (loadAgain) {
        loadAgain = false
        await load()
      }
    })()
    return loadPromise
  }

  /** Loads the catalog unless it is already known. */
  function ensure(): Promise<void> {
    return catalog.value ? Promise.resolve() : load()
  }

  function entry(id: string): SoftwareEntry | null {
    return software.value.find((item) => item.id === id) ?? null
  }

  function track(job: Job) {
    const id = job.params.software
    if (!SOFTWARE_KINDS.has(job.kind) || typeof id !== 'string' || isFinalState(job.state)) return
    running.value = { ...running.value, [job.id]: { id: job.id, kind: job.kind, software: id, line: String(job.params.line ?? '') } }
    schedule()
  }

  /** Removes a finished job; true when it was being followed. */
  function untrack(id: string): boolean {
    if (!(id in running.value)) return false
    const next = { ...running.value }
    delete next[id]
    running.value = next
    return true
  }

  /** The running job of a software entry, if any (package operations run one at a time). */
  function runningFor(software: string): RunningSoftwareJob | null {
    return Object.values(running.value).find((job) => job.software === software) ?? null
  }

  function schedule() {
    if (timer !== undefined || Object.keys(running.value).length === 0) return
    timer = setTimeout(() => void poll(), POLL_MS)
  }

  async function poll() {
    timer = undefined
    let ended = false
    for (const item of Object.values(running.value)) {
      try {
        const job = await fetchJob(item.id)
        if (isFinalState(job.state)) ended = untrack(item.id) || ended
      } catch (err) {
        // A job that disappeared is no longer running; other errors retry next round.
        if (isApiError(err) && err.status === 404) ended = untrack(item.id) || ended
      }
    }
    if (ended) void load()
    schedule()
  }

  /** Finds software jobs that were already running (started elsewhere). */
  async function discover() {
    try {
      const page = await fetchJobs({ limit: 50 })
      for (const job of page.jobs) track(job)
    } catch {
      // Without the list the buttons are simply not locked; the helper still
      // runs one package operation at a time.
    }
  }

  /**
   * Starts a software job, hands it to the job dock and locks the software
   * until it ends; the catalog reloads then. Errors are shown as a toast
   * unless `rethrow` asks for them.
   */
  async function runJob(start: () => Promise<Job>, options: { rethrow?: boolean; onFinished?: (job: Job) => void } = {}) {
    const job = await launchJob(start, {
      rethrow: options.rethrow,
      onFinished: (done) => {
        untrack(done.id)
        void load()
        options.onFinished?.(done)
      },
    })
    if (job) track(job)
    return job
  }

  function setServiceFailure(key: string, error: { code: string; params?: ErrorParams } | null) {
    const next = { ...serviceFailures.value }
    if (error) next[key] = error
    else delete next[key]
    serviceFailures.value = next
  }

  function openInstall(software: string, line?: string) {
    installTarget.value = { software, line }
    installOpen.value = true
  }

  /** Forgets everything (sign-out): the next account loads its own. */
  onAccountReset(() => {
    epoch++
    clearTimeout(timer)
    timer = undefined
    catalog.value = null
    loadError.value = null
    running.value = {}
    serviceFailures.value = {}
    installOpen.value = false
    installTarget.value = null
  })

  return {
    catalog,
    loadError,
    loading,
    software,
    repos,
    running,
    serviceFailures,
    setServiceFailure,
    installOpen,
    installTarget,
    load,
    ensure,
    entry,
    track,
    runningFor,
    discover,
    runJob,
    openInstall,
  }
})
