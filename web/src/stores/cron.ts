// SPDX-License-Identifier: AGPL-3.0-only
//
// The scheduled tasks page's data: the tasks with their next and last runs,
// the names of the backup plans backup tasks run, and whom this account may
// run shell tasks as. Shared by the table, the task drawer and the form
// sheet, and reloaded when a task job ends.
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { fetchPlans } from '@/api/backups'
import { fetchCronUsers, fetchTasks, type CronList, type CronTask } from '@/api/cron'
import type { Job } from '@/api/jobs'
import type { TermUsers } from '@/api/terminal'
import { launchJob } from '@/jobs/dock'
import { onAccountReset } from './accountReset'
import { useSessionStore } from './session'

/** How often the list refreshes while a task is being applied or runs. */
const BUSY_REFRESH_MS = 3000
/** How long the backup plan names and the users are reused. */
const PLANS_REFRESH_MS = 60_000

export const useCronStore = defineStore('cron', () => {
  const list = ref<CronList | null>(null)
  const loadError = ref<unknown>(null)
  const loading = ref(false)
  /** Backup plan names by id, for backup tasks. */
  const planNames = ref<Record<string, string>>({})
  /**
   * Whom shell tasks may run as for this account (site owners only without
   * `cron.root`); null until read, and not read for those who may not
   * change tasks or may change them all.
   */
  const users = ref<TermUsers | null>(null)

  let watchers = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  /** When the plan names were last read; the list refreshes often while tasks run. */
  let plansAt = 0
  let usersAt = 0

  const tasks = computed<CronTask[]>(() => list.value?.tasks ?? [])

  async function loadPlans() {
    if (!useSessionStore().can('backups.read') || Date.now() - plansAt < PLANS_REFRESH_MS) return
    plansAt = Date.now()
    try {
      planNames.value = Object.fromEntries((await fetchPlans()).map((plan) => [plan.id, plan.name]))
    } catch {
      // Backup tasks then show without their plan's name, until the next read.
      plansAt = 0
    }
  }

  /** A fresher list read elsewhere (the task sheet) replaces the cached one. */
  function setUsers(list: TermUsers): void {
    users.value = list
    usersAt = Date.now()
  }

  async function loadUsers() {
    const session = useSessionStore()
    if (!session.can('cron.manage') || session.can('cron.root') || Date.now() - usersAt < PLANS_REFRESH_MS) return
    usersAt = Date.now()
    try {
      users.value = await fetchCronUsers()
    } catch {
      // Shell tasks then stay unchangeable until the next read.
      usersAt = 0
    }
  }

  /** Numbers the loads, so an older answer arriving late is dropped. */
  let seq = 0

  async function load(): Promise<void> {
    clearTimeout(timer)
    const mine = ++seq
    loading.value = true
    try {
      const [tasksList] = await Promise.all([fetchTasks(), loadPlans(), loadUsers()])
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

  /** Forgets everything (sign-out): the next account reads its own tasks, plans and users. */
  function reset(): void {
    seq++
    clearTimeout(timer)
    list.value = null
    loadError.value = null
    loading.value = false
    planNames.value = {}
    users.value = null
    plansAt = 0
    usersAt = 0
  }

  onAccountReset(reset)

  return { list, tasks, loadError, loading, planNames, users, load, task, runJob, watch, unwatch, setUsers, reset }
})
