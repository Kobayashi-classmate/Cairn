// SPDX-License-Identifier: AGPL-3.0-only
//
// The scheduled tasks page's data: the tasks with their next and last runs,
// and the names of the backup plans backup tasks run. Shared by the table,
// the task drawer and the form sheet, and reloaded when a task job ends.
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { fetchPlans } from '@/api/backups'
import { fetchTasks, type CronList, type CronTask } from '@/api/cron'
import type { Job } from '@/api/jobs'
import { launchJob } from '@/jobs/dock'
import { useSessionStore } from './session'

/** How often the list refreshes while a task is being applied or runs. */
const BUSY_REFRESH_MS = 3000
/** How long the backup plan names are reused. */
const PLANS_REFRESH_MS = 60_000

export const useCronStore = defineStore('cron', () => {
  const list = ref<CronList | null>(null)
  const loadError = ref<unknown>(null)
  const loading = ref(false)
  /** Backup plan names by id, for backup tasks. */
  const planNames = ref<Record<string, string>>({})

  let watchers = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  /** When the plan names were last read; the list refreshes often while tasks run. */
  let plansAt = 0

  const tasks = computed<CronTask[]>(() => list.value?.tasks ?? [])

  async function loadPlans() {
    if (!useSessionStore().can('backups.read') || Date.now() - plansAt < PLANS_REFRESH_MS) return
    plansAt = Date.now()
    try {
      planNames.value = Object.fromEntries((await fetchPlans()).map((plan) => [plan.id, plan.name]))
    } catch {
      // Backup tasks then show without their plan's name.
    }
  }

  /** Numbers the loads, so an older answer arriving late is dropped. */
  let seq = 0

  async function load(): Promise<void> {
    clearTimeout(timer)
    const mine = ++seq
    loading.value = true
    try {
      const [tasksList] = await Promise.all([fetchTasks(), loadPlans()])
      if (mine !== seq) return
      list.value = tasksList
      loadError.value = null
    } catch (err) {
      if (mine === seq) loadError.value = err
    } finally {
      if (mine === seq) loading.value = false
    }
    if (mine !== seq) return
    if (watchers > 0 && tasks.value.some((task) => task.state === 'applying' || task.running)) {
      timer = setTimeout(() => void load(), BUSY_REFRESH_MS)
    }
  }

  function task(id: string): CronTask | null {
    return tasks.value.find((item) => item.id === id) ?? null
  }

  /** Starts a task job and hands it to the job dock; the list reloads now and when it ends. */
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

  function watch(): void {
    watchers++
  }

  function unwatch(): void {
    watchers = Math.max(0, watchers - 1)
    if (watchers === 0) clearTimeout(timer)
  }

  return { list, tasks, loadError, loading, planNames, load, task, runJob, watch, unwatch }
})
