// SPDX-License-Identifier: AGPL-3.0-only
//
// File actions shared by the table, its "more" menus, the bulk bar and the
// trash: deleting (to the trash, or for good), restoring and purging. They
// run as jobs the job dock follows; a job that could not do everything
// offers the way out (delete for good what the trash cannot take, replace
// what is in the way of a restore).
import { FILE_BATCH_MAX, deleteEntries, downloadUrl, emptyTrash, purgeTrash, restoreTrash, type TrashEntry } from '@/api/files'
import { followJobLog, type Job } from '@/api/jobs'
import { confirmAction } from '@/composables/useConfirmDialog'
import { absolutePath, baseName } from '@/files/paths'
import { i18n } from '@/i18n'
import { notifyInfo } from '@/lib/notify'
import { useFilesStore } from '@/stores/files'

/** What a row's "more" menu or the bulk bar asks the page to do. */
export type FileActionKey =
  | 'edit'
  | 'download'
  | 'extract'
  | 'rename'
  | 'copy'
  | 'move'
  | 'compress'
  | 'chmod'
  | 'chown'
  | 'delete'

/** Selections larger than one operation takes are refused up front. */
export function withinBatch(count: number): boolean {
  if (count <= FILE_BATCH_MAX) return true
  notifyInfo(i18n.global.t('files.selection.tooMany', { max: FILE_BATCH_MAX }))
  return false
}

/** Starts a download of an entry through a plain link. */
export function download(root: string, path: string): void {
  window.location.assign(downloadUrl(root, path))
}

/**
 * The entries a job left out because of `code`: all of them when the job
 * failed with that code, otherwise the ones its log names ("! <path>: <code>").
 */
async function failedWith(job: Job, code: string, all: readonly string[]): Promise<string[]> {
  if (job.state === 'failed' && job.error?.code === code) return [...all]
  if (job.state !== 'succeeded' && !(job.state === 'failed' && job.error?.code === 'op.failed')) return []
  try {
    const { log } = await followJobLog(job.id, 0, 0)
    const suffix = `: ${code}`
    return log
      .split('\n')
      .filter((line) => line.startsWith('! ') && line.endsWith(suffix))
      .map((line) => line.slice(2, -suffix.length))
  } catch {
    return []
  }
}

/** A short name for what is acted on: the entry's name, or "3 entries". */
export function entriesLabel(paths: readonly string[]): string {
  const { t } = i18n.global
  return paths.length === 1 ? baseName(paths[0] ?? '') : t('files.selection.count', { count: paths.length }, paths.length)
}

/**
 * Asks before deleting entries: to the trash by default, or for good
 * (step-up protected). Entries the trash cannot take (another file system)
 * are offered to be deleted for good afterwards.
 */
export async function askDelete(root: string, paths: string[], permanent = false): Promise<boolean> {
  if (paths.length === 0 || !withinBatch(paths.length)) return false
  const { t } = i18n.global
  const store = useFilesStore()
  const name = entriesLabel(paths)
  return confirmAction({
    title: t('files.delete.title', { name }),
    description: (forGood) => (forGood ? t('files.delete.permanentText') : t('files.delete.trashText')),
    confirmLabel: (forGood) => (forGood ? t('files.delete.submitPermanent') : t('files.delete.submit')),
    destructive: true,
    option: { label: t('files.delete.permanent'), checked: permanent },
    run: async (forGood) => {
      await store.runJob(() => deleteEntries(root, paths, forGood), {
        rethrow: true,
        onFinished: (job) => {
          if (!forGood) void offerPermanent(job, root, paths)
        },
      })
    },
  })
}

async function offerPermanent(job: Job, root: string, paths: string[]) {
  const left = await failedWith(job, 'file.cross_device', paths)
  if (left.length === 0) return
  const { t } = i18n.global
  await confirmAction({
    title: t('files.delete.crossDeviceTitle', { count: left.length }, left.length),
    description: t('files.delete.crossDeviceText'),
    confirmLabel: t('files.delete.submitPermanent'),
    destructive: true,
    run: async () => {
      await useFilesStore().runJob(() => deleteEntries(root, left, true), { rethrow: true })
    },
  })
}

/** Puts trash entries back; entries in the way at their old place are offered to be replaced. */
export async function restore(root: string, entries: readonly TrashEntry[]): Promise<void> {
  if (entries.length === 0 || !withinBatch(entries.length)) return
  const store = useFilesStore()
  const ids = entries.map((entry) => entry.id)
  await store.runJob(() => restoreTrash(root, ids, false), {
    onFinished: (job) => void offerReplace(job, root, entries),
  })
}

async function offerReplace(job: Job, root: string, entries: readonly TrashEntry[]) {
  const blocked = new Set(await failedWith(job, 'file.exists', entries.map((entry) => entry.path)))
  const ids = entries.filter((entry) => blocked.has(entry.path)).map((entry) => entry.id)
  if (ids.length === 0) return
  const { t } = i18n.global
  const first = entries.find((entry) => blocked.has(entry.path))
  await confirmAction({
    title: t('files.trash.existsTitle', { count: ids.length }, ids.length),
    description: t('files.trash.existsText', { path: first ? absolutePath(root, first.path) : '' }, ids.length),
    confirmLabel: t('files.trash.replace'),
    destructive: true,
    run: async () => {
      await useFilesStore().runJob(() => restoreTrash(root, ids, true), { rethrow: true })
    },
  })
}

/** Deletes trash entries for good (step-up protected). */
export function askPurge(root: string, entries: readonly TrashEntry[]): Promise<boolean> {
  const { t } = i18n.global
  if (entries.length === 0 || !withinBatch(entries.length)) return Promise.resolve(false)
  const ids = entries.map((entry) => entry.id)
  const name = entriesLabel(entries.map((entry) => entry.path))
  return confirmAction({
    title: t('files.trash.purgeTitle', { name }),
    description: t('files.trash.purgeText'),
    confirmLabel: t('files.trash.purge'),
    destructive: true,
    run: async () => {
      await useFilesStore().runJob(() => purgeTrash(root, ids), { rethrow: true })
    },
  })
}

/** Empties a root's trash (step-up protected). */
export function askEmpty(root: string, count: number): Promise<boolean> {
  const { t } = i18n.global
  return confirmAction({
    title: t('files.trash.emptyTitle'),
    description: t('files.trash.emptyText', { count }, count),
    confirmLabel: t('files.trash.empty'),
    destructive: true,
    run: async () => {
      await useFilesStore().runJob(() => emptyTrash(root), { rethrow: true })
    },
  })
}
