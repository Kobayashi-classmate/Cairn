// SPDX-License-Identifier: AGPL-3.0-only
//
// Scheduled task actions shared by the table, its "more" menu and the task
// drawer. Every change is a job the job dock follows; shell tasks that run
// as root or as anyone but a site owner are step-up protected.
import { reactive } from 'vue'
import { deleteTask, runTask, updateTask, type CronTask, type TaskRequest } from '@/api/cron'
import type { Job } from '@/api/jobs'
import { confirmAction } from '@/composables/useConfirmDialog'
import { i18n } from '@/i18n'
import { onAccountReset } from '@/stores/accountReset'
import { useCronStore } from '@/stores/cron'
import { useSessionStore } from '@/stores/session'

/** Who or what a task runs: "Runs as cw-shop", the URL, "Backup plan Nightly". */
export function taskTarget(task: CronTask, planNames: Readonly<Record<string, string>>): string {
  const { t } = i18n.global
  if (task.type === 'shell') return t('cron.target.shell', { user: task.user ?? '' })
  if (task.type === 'url') return task.url || t('cron.target.urlHidden')
  return t('cron.target.backup', { plan: planNames[task.plan ?? ''] ?? task.plan ?? '' })
}

/**
 * The server leaves out what a task runs (script or URL: they can hold
 * passwords and keys) for those who may not change it.
 */
export function contentHidden(task: CronTask): boolean {
  return (task.type === 'shell' && !task.script) || (task.type === 'url' && !task.url)
}

/**
 * Whether this account may change, run and delete a task. Shell tasks that
 * run as root or as anyone but a site owner (who may be able to use sudo)
 * take `cron.root`; without it, only users the server offers (site owners)
 * count, so a task whose user it does not list stays out of reach.
 */
export function canChange(task: CronTask): boolean {
  const session = useSessionStore()
  if (!session.can('cron.manage') || contentHidden(task)) return false
  if (task.type !== 'shell' || session.can('cron.root')) return true
  if (task.user === 'root') return false
  return useCronStore().users?.users.some((user) => user.name === task.user && user.kind === 'site') ?? false
}

/** Site owners are cw-<code> (and never end in -run, which is a site's runner). */
const SITE_OWNER = /^cw-[a-z][a-z0-9-]*$/

/**
 * Whether this account may delete a task: those it may change, and, without
 * `cron.root`, also tasks of a site owner that no longer exists (its site
 * was deleted): deleting stops something and starts nothing.
 */
export function canDelete(task: CronTask): boolean {
  if (canChange(task)) return true
  const session = useSessionStore()
  if (!session.can('cron.manage') || task.type !== 'shell' || !task.user) return false
  const users = useCronStore().users
  if (!users || users.users.some((user) => user.name === task.user)) return false
  return SITE_OWNER.test(task.user) && !task.user.endsWith('-run')
}

/** The task as a request, for saving it with a change. */
export function requestOf(task: CronTask): TaskRequest {
  return {
    name: task.name,
    type: task.type,
    schedule: task.schedule,
    timeoutMin: task.timeoutMin,
    enabled: task.enabled,
    ...(task.type === 'shell' ? { user: task.user ?? '', script: task.script ?? '' } : {}),
    ...(task.type === 'url' ? { url: task.url ?? '' } : {}),
    ...(task.type === 'backup' ? { plan: task.plan ?? '' } : {}),
  }
}

/** Tasks whose "run now" job, started from this tab, has not finished: the list shows them running only after a refresh. */
const runs = reactive(new Set<string>())
onAccountReset(() => runs.clear())

/** Whether a run started from this tab is still being started. */
export function taskStarting(id: string): boolean {
  return runs.has(id)
}

export async function runTaskNow(task: CronTask): Promise<Job | null> {
  if (runs.has(task.id)) return null
  runs.add(task.id)
  try {
    const job = await useCronStore().runJob(() => runTask(task.id), { onFinished: () => runs.delete(task.id) })
    if (!job) runs.delete(task.id)
    return job
  } catch (err) {
    runs.delete(task.id)
    throw err
  }
}

/** Switches a task on or off (saving it whole). */
export function setEnabled(task: CronTask, enabled: boolean): Promise<Job | null> {
  return useCronStore().runJob(() => updateTask(task.id, { ...requestOf(task), enabled }))
}

/** Asks before deleting a task; resolves true once the job started. */
export function askDeleteTask(task: CronTask): Promise<boolean> {
  const { t } = i18n.global
  const store = useCronStore()
  return confirmAction({
    title: t('cron.task.deleteTitle', { name: task.name }),
    description: t('cron.task.deleteText'),
    confirmLabel: t('cron.task.delete'),
    destructive: true,
    run: async () => {
      await store.runJob(() => deleteTask(task.id), { rethrow: true })
    },
  })
}
