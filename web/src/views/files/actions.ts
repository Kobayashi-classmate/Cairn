// SPDX-License-Identifier: AGPL-3.0-only
//
// File actions shared by the table, its "more" menus, the bulk bar and the
// trash: deleting (to the trash, or for good), restoring and purging. They
// run as jobs the job dock follows; a job that could not do everything
// offers the way out (delete for good what the trash cannot take, replace
// what is in the way of a restore).
import { FILE_BATCH_MAX, deleteEntries, downloadLink, emptyTrash, purgeTrash, restoreTrash, type TrashEntry } from '@/api/files'
import { followJobLog, type Job } from '@/api/jobs'
import { ensureElevated } from '@/auth/stepUp'
import { confirmAction } from '@/composables/useConfirmDialog'
import { absolutePath, baseName } from '@/files/paths'
import { i18n } from '@/i18n'
import { notifyError, notifyInfo } from '@/lib/notify'
import { useFilesStore } from '@/stores/files'
import { useSessionStore } from '@/stores/session'

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

/**
 * Whether reading a file of a root through a link the browser follows
 * (download, image preview) needs a recent re-authentication: everything
 * but the sites root holds backups or the whole disk. Never while
 * re-authentication is turned off for the panel.
 */
export function linkNeedsStepUp(root: string): boolean {
  return root !== 'sites' && useSessionStore().stepUpRequired
}

/**
 * Saves a URL of this origin through a `download` link. The page stays: a
 * navigation would ask about unsaved edits and running uploads, and an
 * error answer would replace the app.
 */
function saveLink(url: string, name: string): void {
  const link = document.createElement('a')
  link.href = url
  link.download = name
  link.hidden = true
  document.body.append(link)
  link.click()
  link.remove()
}

/**
 * Starts a download of an entry; from roots other than the sites, after
 * re-authenticating (otherwise the browser would save the server's
 * refusal). The browser fetches the file by itself, without the session
 * key, so the address is made for this one download right before it. Does
 * nothing when the user cancels.
 */
export async function download(root: string, path: string): Promise<void> {
  try {
    if (linkNeedsStepUp(root)) await ensureElevated()
    saveLink(await downloadLink(root, path), baseName(path))
  } catch (err) {
    // A declined re-authentication stays silent.
    notifyError(err)
  }
}

/**
 * Most of a finished job's log read to find the entries it left out. A
 * batch names at most FILE_BATCH_MAX entries, so this is far more than
 * their lines take; a longer log is cut here.
 */
const LOG_READ_MAX = 16 << 20

/** A finished job's log, read chunk by chunk (the server sends 256 KiB at a time) up to LOG_READ_MAX. */
async function wholeLog(id: string): Promise<string> {
  const parts: string[] = []
  let offset = 0
  for (;;) {
    const chunk = await followJobLog(id, offset, 0)
    parts.push(chunk.log)
    if (chunk.offset <= offset || chunk.offset >= chunk.logSize || chunk.offset >= LOG_READ_MAX) break
    offset = chunk.offset
  }
  return parts.join('')
}

/**
 * The entries a job left out because of `code`: all of them when the job
 * failed with that code, otherwise the ones its log names ("! <path>: <code>").
 */
async function failedWith(job: Job, code: string, all: readonly string[]): Promise<string[]> {
  if (job.state === 'failed' && job.error?.code === code) return [...all]
  if (job.state !== 'succeeded' && !(job.state === 'failed' && job.error?.code === 'op.failed')) return []
  try {
    const log = await wholeLog(job.id)
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

/**
 * Empties a root's trash (step-up protected): the entries listed, which the
 * dialog counts, and nothing deleted after the newest of them.
 */
export function askEmpty(root: string, entries: readonly TrashEntry[]): Promise<boolean> {
  const { t } = i18n.global
  const count = entries.length
  if (count === 0) return Promise.resolve(false)
  // The server purges entries deleted strictly before olderThan.
  const olderThan = Math.max(...entries.map((entry) => entry.deletedAt)) + 1
  return confirmAction({
    title: t('files.trash.emptyTitle'),
    description: t('files.trash.emptyText', { count }, count),
    confirmLabel: t('files.trash.empty'),
    destructive: true,
    run: async () => {
      await useFilesStore().runJob(() => emptyTrash(root, olderThan), { rethrow: true })
    },
  })
}
