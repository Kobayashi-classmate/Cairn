// SPDX-License-Identifier: AGPL-3.0-only
//
// The databases page's data: the database servers with their databases and
// users. Shared by the service cards, the lists and the form sheets, and
// reloaded when a database job ends.
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { fetchDatabases, type DbEngine, type DbStatus } from '@/api/databases'
import type { Job } from '@/api/jobs'
import { launchJob } from '@/jobs/dock'
import { onAccountReset } from './accountReset'

export const useDatabasesStore = defineStore('databases', () => {
  const status = ref<DbStatus | null>(null)
  const loadError = ref<unknown>(null)
  const loading = ref(false)
  /** Bumped on every reload, so lists kept apart (dumps) refresh too. */
  const version = ref(0)

  const engines = computed<DbEngine[]>(() => status.value?.engines ?? [])

  /** Numbers the loads, so an older answer arriving late is dropped. */
  let seq = 0

  async function load(): Promise<void> {
    const mine = ++seq
    loading.value = true
    try {
      const loaded = await fetchDatabases()
      if (mine !== seq) return
      status.value = loaded
      loadError.value = null
      version.value++
    } catch (err) {
      if (mine === seq) loadError.value = err
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  function engine(id: string): DbEngine | null {
    return engines.value.find((e) => e.id === id) ?? null
  }

  /**
   * Starts a database job and hands it to the job dock; the servers reload
   * now and when it ends. Errors are shown as a toast unless `rethrow` asks
   * for them (forms and confirmations show them in place).
   */
  async function runJob(start: () => Promise<Job>, options: { rethrow?: boolean; onFinished?: (job: Job) => void } = {}) {
    const job = await launchJob(start, {
      rethrow: options.rethrow,
      onFinished: (done) => {
        void load()
        options.onFinished?.(done)
      },
    })
    if (job) void load()
    return job
  }

  /** Forgets everything (sign-out): the next account loads its own. */
  function reset(): void {
    seq++
    status.value = null
    loadError.value = null
    loading.value = false
  }
  onAccountReset(reset)

  return { status, loadError, loading, version, engines, load, engine, runJob }
})
