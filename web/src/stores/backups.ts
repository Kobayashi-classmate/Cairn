// SPDX-License-Identifier: AGPL-3.0-only
//
// The backups page's data: plans, storage and the scheduled tasks that run
// the plans (for their schedule and next run). Shared by the tables, the
// plan drawer and the form sheets, and reloaded when a backup job ends.
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { fetchPlans, fetchStorages, type BackupPlan, type StorageView } from '@/api/backups'
import { fetchTasks, type CronTask } from '@/api/cron'
import type { Job } from '@/api/jobs'
import { launchJob } from '@/jobs/dock'
import { onAccountReset } from './accountReset'
import { useSessionStore } from './session'

export const useBackupsStore = defineStore('backups', () => {
  const plans = ref<BackupPlan[] | null>(null)
  const storages = ref<StorageView[] | null>(null)
  /** Scheduled tasks by id; empty without `cron.read`. */
  const tasks = ref<Record<string, CronTask>>({})
  const loadError = ref<unknown>(null)
  const loading = ref(false)

  const loaded = computed(() => plans.value !== null && storages.value !== null)

  /** Numbers the loads, so an older answer arriving late is dropped. */
  let seq = 0

  async function loadTasks(mine: number) {
    if (!useSessionStore().can('cron.read')) return
    try {
      const list = await fetchTasks()
      if (mine !== seq) return
      tasks.value = Object.fromEntries(list.tasks.map((task) => [task.id, task]))
    } catch {
      // Plans then show without their schedule.
    }
  }

  async function load(): Promise<void> {
    const mine = ++seq
    loading.value = true
    try {
      const [planList, storageList] = await Promise.all([fetchPlans(), fetchStorages(), loadTasks(mine)])
      if (mine !== seq) return
      plans.value = planList
      storages.value = storageList
      loadError.value = null
    } catch (err) {
      if (mine === seq) loadError.value = err
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  function plan(id: string): BackupPlan | null {
    return plans.value?.find((p) => p.id === id) ?? null
  }

  function storageName(id: string | undefined): string | null {
    if (!id) return null
    return storages.value?.find((s) => s.id === id)?.name ?? id
  }

  /** The scheduled tasks that run a plan, as far as they are known. */
  function tasksOf(p: BackupPlan): CronTask[] {
    return p.tasks.map((id) => tasks.value[id]).filter((task): task is CronTask => task !== undefined)
  }

  /** Starts a backup job and hands it to the job dock; plans reload now and when it ends. */
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
    plans.value = null
    storages.value = null
    tasks.value = {}
    loadError.value = null
    loading.value = false
  }

  onAccountReset(reset)

  return { plans, storages, tasks, loadError, loading, loaded, load, plan, storageName, tasksOf, runJob, reset }
})
