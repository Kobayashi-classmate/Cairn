// SPDX-License-Identifier: AGPL-3.0-only
//
// Scheduled task actions shared by the table, its "more" menu and the task
// drawer. Every change is a job the job dock follows; tasks that run as
// root are step-up protected.
import { deleteTask, runTask, updateTask, type CronTask, type TaskRequest } from '@/api/cron'
import type { Job } from '@/api/jobs'
import { confirmAction } from '@/composables/useConfirmDialog'
import { i18n } from '@/i18n'
import { useCronStore } from '@/stores/cron'
import { useSessionStore } from '@/stores/session'

/** Who or what a task runs: "Runs as cw-shop", the URL, "Backup plan Nightly". */
export function taskTarget(task: CronTask, planNames: Readonly<Record<string, string>>): string {
  const { t } = i18n.global
  if (task.type === 'shell') return t('cron.target.shell', { user: task.user ?? '' })
  if (task.type === 'url') return task.url ?? ''
  return t('cron.target.backup', { plan: planNames[task.plan ?? ''] ?? task.plan ?? '' })
}

/** Root tasks can only be changed with the root permission. */
export function canChange(task: CronTask): boolean {
  const session = useSessionStore()
  return session.can('cron.manage') && (task.type !== 'shell' || task.user !== 'root' || session.can('cron.root'))
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

export function runTaskNow(task: CronTask): Promise<Job | null> {
  return useCronStore().runJob(() => runTask(task.id))
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
