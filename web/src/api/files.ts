// SPDX-License-Identifier: AGPL-3.0-only
//
// File manager (`files.read` to browse, `files.manage` to change,
// `files.admin` for the full-disk switch). Every path is relative to a root:
// "sites" (/srv/www), "backups" (the panel's backups) or "system" (/, only
// with full-disk access). Changes to many entries run as jobs.
import { API_BASE, api } from './client'
import type { Job } from './jobs'

export type FileRootId = 'sites' | 'backups' | 'system'
export type FileType = 'file' | 'dir' | 'link' | 'other'

export interface FileRoot {
  id: string
  /** Absolute path on the server. */
  path: string
  available: boolean
}

export interface AccessSettings {
  fullDisk: boolean
  rootTerminal: boolean
  /** The server's configuration forbids root access; both switches stay off. */
  locked?: boolean
}

/** Archive formats the server can read beyond the built-in ones. */
export interface Archivers {
  sevenZip: boolean
  rar: boolean
}

export interface FileRoots {
  roots: FileRoot[]
  settings: AccessSettings
  archivers: Archivers
}

export interface FileEntry {
  name: string
  /** Relative to the root. */
  path: string
  type: FileType
  size: number
  /** Permission bits with setgid and sticky (as a number; shown in octal). */
  mode: number
  owner: string
  group: string
  /** Unix seconds. */
  modTime: number
  /** A link's target as written in the link. */
  target?: string
}

export interface FileList {
  dir: FileEntry
  /** Directories first, then by name. */
  entries: FileEntry[]
  /** The directory has more entries than the server lists. */
  truncated?: boolean
}

/** A saved file and its new version, for the next save. */
export interface FileSaved {
  entry: FileEntry
  etag: string
}

export interface FileContent {
  entry: FileEntry
  content: string
  /** The version read; saving with a stale one fails with file.changed. */
  etag: string
}

/** A search hit: an entry, or a line of a text file (line > 0). */
export interface FileMatch {
  path: string
  type: FileType
  line?: number
  text?: string
}

export interface FileSearchResult {
  matches: FileMatch[]
  truncated?: boolean
}

export interface TrashEntry {
  id: string
  /** Where the entry was, relative to the root. */
  path: string
  type: FileType
  size: number
  deletedAt: number
  user?: string
}

export interface FileRef {
  root: string
  path: string
}

/** Formats the server writes archives in. */
export const ARCHIVE_FORMATS = ['zip', 'tar.gz', 'tar.zst', 'tar.xz'] as const
export type ArchiveFormat = (typeof ARCHIVE_FORMATS)[number]

/** Largest text file the editor opens or saves (bytes). */
export const FILE_READ_MAX = 4 << 20
/** Largest upload (bytes). */
export const UPLOAD_MAX = 32 * 1024 ** 3
/** Most entries one operation takes. */
export const FILE_BATCH_MAX = 1000

function query(params: Record<string, string | boolean | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === false) continue
    search.set(key, String(value))
  }
  return search.toString()
}

async function jobOf(request: Promise<{ job: Job }>): Promise<Job> {
  return (await request).job
}

export function fetchRoots(signal?: AbortSignal): Promise<FileRoots> {
  return api.get<FileRoots>('/files/roots', { signal })
}

/** Administrators only; step-up protected. */
export function setFullDisk(fullDisk: boolean): Promise<AccessSettings> {
  return api.put<AccessSettings>('/files/settings', { fullDisk })
}

export function listDir(root: string, path: string, hidden: boolean, signal?: AbortSignal): Promise<FileList> {
  return api.get<FileList>(`/files/list?${query({ root, path, hidden })}`, { signal })
}

export function statEntry(root: string, path: string): Promise<FileEntry> {
  return api.get<FileEntry>(`/files/stat?${query({ root, path })}`)
}

export function readFile(root: string, path: string, signal?: AbortSignal): Promise<FileContent> {
  return api.get<FileContent>(`/files/content?${query({ root, path })}`, { signal })
}

/** Saves a text file; an empty etag creates a new one. */
export function saveFile(root: string, path: string, content: string, etag: string): Promise<FileSaved> {
  return api.put<FileSaved>('/files/content', { root, path, content, etag })
}

/** Address of a download; with `inline`, images are shown in the browser. */
export function downloadUrl(root: string, path: string, inline = false): string {
  return `${API_BASE}/files/download?${query({ root, path, inline })}`
}

export function makeDir(root: string, dir: string, name: string): Promise<FileEntry> {
  return api.post<FileEntry>('/files/mkdir', { root, dir, name })
}

export function touchFile(root: string, dir: string, name: string): Promise<FileEntry> {
  return api.post<FileEntry>('/files/touch', { root, dir, name })
}

export function renameEntry(root: string, path: string, name: string): Promise<FileEntry> {
  return api.post<FileEntry>('/files/rename', { root, path, name })
}

export function searchFiles(
  root: string,
  path: string,
  q: string,
  options: { content: boolean; caseSensitive: boolean },
  signal?: AbortSignal,
): Promise<FileSearchResult> {
  const search = query({ root, path, q, content: options.content, case: options.caseSensitive })
  return api.get<FileSearchResult>(`/files/search?${search}`, { signal })
}

export function copyEntries(root: string, paths: string[], dest: string, overwrite: boolean): Promise<Job> {
  return jobOf(api.post('/files/copy', { root, paths, dest, overwrite }))
}

export function moveEntries(root: string, paths: string[], dest: string, overwrite: boolean): Promise<Job> {
  return jobOf(api.post('/files/move', { root, paths, dest, overwrite }))
}

/** To the trash, or with `permanent` (step-up protected) for good. */
export function deleteEntries(root: string, paths: string[], permanent: boolean): Promise<Job> {
  return jobOf(api.post('/files/delete', { root, paths, permanent }))
}

/** `mode` applies to files, `dirMode` to directories (with `recursive`, below each path too). */
export function chmodEntries(root: string, paths: string[], mode: number, dirMode: number, recursive: boolean): Promise<Job> {
  return jobOf(api.post('/files/chmod', { root, paths, mode, dirMode, recursive }))
}

/** Step-up protected; an empty user or group keeps it. */
export function chownEntries(root: string, paths: string[], user: string, group: string, recursive: boolean): Promise<Job> {
  return jobOf(api.post('/files/chown', { root, paths, user, group, recursive }))
}

export function compressEntries(root: string, dir: string, names: string[], format: ArchiveFormat, name: string): Promise<Job> {
  return jobOf(api.post('/files/compress', { root, dir, names, format, name }))
}

export function extractArchive(root: string, path: string, dest: string, overwrite: boolean): Promise<Job> {
  return jobOf(api.post('/files/extract', { root, path, dest, overwrite }))
}

/** Downloads a URL into a directory; `name` defaults to the URL's last part. */
export function fetchUrl(root: string, dir: string, url: string, name: string, overwrite: boolean): Promise<Job> {
  return jobOf(api.post('/files/fetch', { root, dir, url, ...(name ? { name } : {}), overwrite }))
}

export async function fetchTrash(root: string, signal?: AbortSignal): Promise<TrashEntry[]> {
  return (await api.get<{ entries: TrashEntry[] | null }>(`/files/trash?${query({ root })}`, { signal })).entries ?? []
}

export function restoreTrash(root: string, ids: string[], overwrite: boolean): Promise<Job> {
  return jobOf(api.post('/files/trash/restore', { root, ids, overwrite }))
}

/** Step-up protected. */
export function purgeTrash(root: string, ids: string[]): Promise<Job> {
  return jobOf(api.post('/files/trash/purge', { root, ids }))
}

/** Empties a root's trash (step-up protected). */
export function emptyTrash(root: string): Promise<Job> {
  // Everything deleted before now (plus a little clock leeway).
  const olderThan = Math.floor(Date.now() / 1000) + 60
  return jobOf(api.post('/files/trash/purge', { root, olderThan }))
}

export async function fetchFavorites(signal?: AbortSignal): Promise<FileRef[]> {
  return (await api.get<{ favorites: FileRef[] }>('/files/favorites', { signal })).favorites
}

export function addFavorite(root: string, path: string): Promise<void> {
  return api.post<void>('/files/favorites', { root, path })
}

export function removeFavorite(root: string, path: string): Promise<void> {
  return api.delete<void>(`/files/favorites?${query({ root, path })}`)
}
