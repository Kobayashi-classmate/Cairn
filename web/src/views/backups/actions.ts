// SPDX-License-Identifier: AGPL-3.0-only
//
// Backup actions shared by the tables, their "more" menus and the plan
// drawer. Runs, restores and deletions of backups are jobs the job dock
// follows; deleting asks for the plan's name and for the password.
import { reactive } from 'vue'
import { isStepUpRequired } from '@/api/client'
import {
  backupFileLink,
  deleteBackupFiles,
  deletePlan,
  deleteStorage,
  runPlan,
  testStorage,
  type BackupFile,
  type BackupPlan,
  type StorageView,
} from '@/api/backups'
import type { CronTask } from '@/api/cron'
import type { Job } from '@/api/jobs'
import { ensureElevated } from '@/auth/stepUp'
import { fileReachesBeyondSites, reachesBeyondSites, targetLabel } from '@/backups/targets'
import { confirmAction } from '@/composables/useConfirmDialog'
import { describeSchedule } from '@/cron/schedule'
import { i18n } from '@/i18n'
import { notifyError, notifyInfo, notifySuccess } from '@/lib/notify'
import { onAccountReset } from '@/stores/accountReset'
import { useBackupsStore } from '@/stores/backups'
import { useSessionStore } from '@/stores/session'
import { startDownload } from '@/utils/download'

/** What a plan backs up, in a few words: "Site shop, Database shop (MySQL/MariaDB) and 2 more". */
export function targetsSummary(plan: BackupPlan): string {
  const { t } = i18n.global
  const labels = plan.targets.map(targetLabel)
  if (labels.length <= 2) return labels.join(t('backups.plan.separator'))
  return t('backups.plan.targetsMore', { first: labels.slice(0, 2).join(t('backups.plan.separator')), count: labels.length - 2 })
}

/** The schedule of a plan's tasks in words; "By hand" without any. */
export function scheduleText(tasks: readonly CronTask[]): string {
  const { t } = i18n.global
  const enabled = tasks.filter((task) => task.enabled)
  if (enabled.length === 0) return t('backups.plan.manualOnly')
  const first = describeSchedule(enabled[0]!.schedule)
  return enabled.length === 1 ? first : t('backups.plan.scheduleMore', { first, count: enabled.length - 1 })
}

/** The soonest next run of a plan's enabled tasks (Unix seconds). */
export function nextRun(tasks: readonly CronTask[]): number | null {
  const times = tasks.filter((task) => task.enabled && task.nextRun).map((task) => task.nextRun!)
  return times.length > 0 ? Math.min(...times) : null
}

/** Whether this account may change a plan: plans that back up beyond the sites root also take `files.admin`. */
export function canHandlePlan(plan: BackupPlan): boolean {
  const session = useSessionStore()
  return session.can('backups.manage') && (!reachesBeyondSites(plan) || session.can('files.admin'))
}

/**
 * Whether this account may download and restore a backup: one of a folder
 * beyond the sites root (by its name) also takes `files.admin`.
 */
export function canTakeBackup(file: BackupFile): boolean {
  const session = useSessionStore()
  return session.can('backups.manage') && (!fileReachesBeyondSites(file.name) || session.can('files.admin'))
}

/** Plans whose "run now" job, started from this tab, has not finished: one run at a time. */
const runs = reactive(new Set<string>())
onAccountReset(() => runs.clear())

/** Whether a run started from this tab is still going. */
export function planRunning(id: string): boolean {
  return runs.has(id)
}

export async function runPlanNow(plan: BackupPlan): Promise<Job | null> {
  if (runs.has(plan.id)) return null
  runs.add(plan.id)
  try {
    const job = await useBackupsStore().runJob(() => runPlan(plan.id), { onFinished: () => runs.delete(plan.id) })
    if (!job) runs.delete(plan.id)
    return job
  } catch (err) {
    runs.delete(plan.id)
    throw err
  }
}

/**
 * Asks before deleting a plan (step-up protected): the plan's name is typed
 * and its backups can go with it. A plan that scheduled tasks still run
 * cannot be deleted. Resolves true once it is gone.
 */
export async function askDeletePlan(plan: BackupPlan): Promise<boolean> {
  const { t } = i18n.global
  if (plan.tasks.length > 0) {
    notifyInfo(t('errors.backup.plan_in_use'))
    return false
  }
  const store = useBackupsStore()
  return confirmAction({
    title: t('backups.plan.deleteTitle', { name: plan.name }),
    description: (files) => (files ? t('backups.plan.deleteTextFiles') : t('backups.plan.deleteText')),
    confirmLabel: t('backups.plan.delete'),
    destructive: true,
    typeToConfirm: plan.name,
    option: { label: t('backups.plan.deleteFiles') },
    run: async (files) => {
      await deletePlan(plan.id, files)
      notifySuccess(t('backups.plan.deleted', { name: plan.name }))
      void store.load()
    },
  })
}

/** Deletes backups of a plan (step-up protected) after the plan's name is typed. */
export function askDeleteFiles(plan: BackupPlan, names: string[], onFinished?: () => void): Promise<boolean> {
  const { t } = i18n.global
  const store = useBackupsStore()
  const remote = !!plan.storageId
  return confirmAction({
    title: t('backups.files.deleteTitle', { count: names.length }, names.length),
    description: (withRemote) =>
      remote && withRemote ? t('backups.files.deleteTextRemote') : t('backups.files.deleteText'),
    confirmLabel: t('backups.files.deleteConfirm'),
    destructive: true,
    typeToConfirm: plan.name,
    option: remote ? { label: t('backups.files.deleteRemote'), checked: true } : undefined,
    run: async (withRemote) => {
      await store.runJob(() => deleteBackupFiles(plan.id, names, remote && withRemote), {
        rethrow: true,
        onFinished: () => onFinished?.(),
      })
    },
  })
}

/**
 * Downloads a backup after re-authenticating: asks where from first (the
 * panel's copy, or a short-lived link to the bucket's), so errors show as a
 * toast, then lets the browser download it from there.
 */
export async function downloadBackup(plan: BackupPlan, file: BackupFile): Promise<void> {
  let url: string
  try {
    await ensureElevated()
    url = await backupFileLink(plan.id, file.name)
  } catch (err) {
    if (!isStepUpRequired(err)) notifyError(err)
    return
  }
  startDownload(url)
}

export async function testSavedStorage(storage: StorageView): Promise<void> {
  const { t } = i18n.global
  try {
    await testStorage(storage.id)
    notifySuccess(t('backups.storage.testOk'))
  } catch (err) {
    notifyError(err)
  }
}

/** Deleting storage keeps the backups in the bucket; plans that use it refuse. */
export function askDeleteStorage(storage: StorageView): Promise<boolean> {
  const { t } = i18n.global
  const store = useBackupsStore()
  return confirmAction({
    title: t('backups.storage.deleteTitle', { name: storage.name }),
    description: t('backups.storage.deleteText'),
    confirmLabel: t('backups.storage.delete'),
    destructive: true,
    run: async () => {
      await deleteStorage(storage.id)
      notifySuccess(t('backups.storage.deleted', { name: storage.name }))
      void store.load()
    },
  })
}
