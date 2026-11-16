// SPDX-License-Identifier: AGPL-3.0-only
//
// Paths in the file manager are relative to a root ("" is the root itself)
// and use "/" between names, without a leading or trailing slash.
import { reactive } from 'vue'
import { i18n } from '@/i18n'

/** Absolute paths of the roots, as the server reported them last. */
const rootPaths = reactive<Record<string, string>>({ sites: '/srv/www', system: '/' })

/** Remembers where the roots are, for showing absolute paths. */
export function rememberRoots(roots: readonly { id: string; path: string }[]): void {
  for (const root of roots) rootPaths[root.id] = root.path
}

/** Translated name of a root; unknown ids are shown as is. */
export function rootLabel(root: string): string {
  const key = `files.root.${root}`
  return i18n.global.te(key) ? i18n.global.t(key) : root
}

/** The path as it is on the server ("/srv/www/example/index.php"). */
export function absolutePath(root: string, path: string): string {
  const base = rootPaths[root]
  // Until the roots are known (the backups root moves with the data directory).
  if (base === undefined) return path ? `${rootLabel(root)}: /${path}` : rootLabel(root)
  if (!path) return base
  return base.endsWith('/') ? base + path : `${base}/${path}`
}

export function joinPath(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name
}

/** The directory holding `path` ("" for entries of the root). */
export function parentPath(path: string): string {
  const index = path.lastIndexOf('/')
  return index < 0 ? '' : path.slice(0, index)
}

export function baseName(path: string): string {
  return path.slice(path.lastIndexOf('/') + 1)
}

/** Each directory from the root down to `path`, for breadcrumbs. */
export function pathSegments(path: string): { name: string; path: string }[] {
  if (!path) return []
  const names = path.split('/')
  return names.map((name, index) => ({ name, path: names.slice(0, index + 1).join('/') }))
}

/** A name a directory entry can have. */
export function isFileName(name: string): boolean {
  return name !== '' && name !== '.' && name !== '..' && !name.includes('/') && !name.includes('\0') && new TextEncoder().encode(name).length <= 255
}

/** A path relative to a root ("" is the root). */
export function isFilePath(path: string): boolean {
  if (path === '') return true
  if (path.length > 4096 || path.startsWith('/') || path.endsWith('/')) return false
  return path.split('/').every(isFileName)
}

/** Lower-case extension including archive double extensions ("tar.gz"). */
export function extensionOf(name: string): string {
  const lower = name.toLowerCase()
  const double = /\.(tar\.(?:gz|zst|xz|bz2))$/.exec(lower)
  if (double) return double[1] ?? ''
  const index = lower.lastIndexOf('.')
  return index > 0 ? lower.slice(index + 1) : ''
}

const IMAGES = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'avif', 'bmp', 'ico', 'svg'])

/** Images the server shows in the browser. */
export function isImage(name: string): boolean {
  return IMAGES.has(extensionOf(name))
}

const BUILT_IN_ARCHIVES = new Set(['zip', 'tar', 'tar.gz', 'tgz', 'tar.zst', 'tar.xz', 'tar.bz2', 'gz', 'zst', 'xz', 'bz2'])

/** Archives the server can extract: built in, or "7z" / "rar" with a system tool. */
export function archiveKind(name: string): 'builtin' | '7z' | 'rar' | null {
  const ext = extensionOf(name)
  if (BUILT_IN_ARCHIVES.has(ext)) return 'builtin'
  if (ext === '7z' || ext === 'rar') return ext
  return null
}

/** The name without its archive extension ("site.tar.gz" gives "site"). */
export function archiveStem(name: string): string {
  const ext = extensionOf(name)
  return ext && name.length > ext.length + 1 ? name.slice(0, name.length - ext.length - 1) : name
}
