// SPDX-License-Identifier: AGPL-3.0-only
//
// The Files page's data: the roots and the access settings, the user's
// favourite folders, the folder being shown and the trash of each root.
// Shared by the table, the trash, the sheets and the editor; jobs started
// here reload the folder and the trash when they end.
import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import { isAbortError } from '@/api/client'
import {
  addFavorite,
  fetchFavorites,
  fetchRoots,
  fetchTrash,
  listDir,
  removeFavorite,
  type FileList,
  type FileRef,
  type FileRoots,
  type TrashEntry,
} from '@/api/files'
import type { Job } from '@/api/jobs'
import { rememberRoots } from '@/files/paths'
import { launchJob } from '@/jobs/dock'
import { onAccountReset } from './accountReset'

/** Remembers whether hidden files are shown (kept from the first file manager). */
const HIDDEN_KEY = 'cairn.files.hidden'

function readHidden(): boolean {
  try {
    return window.localStorage.getItem(HIDDEN_KEY) === '1'
  } catch {
    return false
  }
}

export const useFilesStore = defineStore('files', () => {
  const roots = ref<FileRoots | null>(null)
  const rootsError = ref<unknown>(null)
  const favorites = ref<FileRef[]>([])

  /** The folder shown: a root and a path in it. */
  const where = ref<{ root: string; path: string } | null>(null)
  const list = ref<FileList | null>(null)
  const listError = ref<unknown>(null)
  const listLoading = ref(false)
  const hidden = ref(readHidden())

  /** Each root's trash, once read. */
  const trash = ref<Record<string, TrashEntry[]>>({})
  const trashErrors = ref<Record<string, unknown>>({})
  const trashLoading = ref<Record<string, boolean>>({})

  const settings = computed(() => roots.value?.settings ?? null)
  const available = computed(() => roots.value?.roots.filter((root) => root.available) ?? [])

  /** Entries in the trash of every root read so far; undefined before any was read. */
  const trashCount = computed(() => {
    const lists = Object.values(trash.value)
    return lists.length > 0 ? lists.reduce((sum, entries) => sum + entries.length, 0) : undefined
  })

  /** Numbers the reads of the roots, so one answering after leave() is dropped. */
  let rootsSeq = 0

  async function loadRoots(): Promise<void> {
    const mine = ++rootsSeq
    try {
      const loaded = await fetchRoots()
      if (mine !== rootsSeq) return
      roots.value = loaded
      rememberRoots(loaded.roots)
      rootsError.value = null
    } catch (err) {
      if (mine === rootsSeq) rootsError.value = err
    }
  }

  async function loadFavorites(): Promise<void> {
    try {
      favorites.value = await fetchFavorites()
    } catch {
      // Favourites are a convenience; browsing works without them.
    }
  }

  function isFavorite(root: string, path: string): boolean {
    return favorites.value.some((f) => f.root === root && f.path === path)
  }

  /** Adds or removes a favourite; errors are the caller's to show. */
  async function setFavorite(root: string, path: string, on: boolean): Promise<void> {
    if (on) await addFavorite(root, path)
    else await removeFavorite(root, path)
    await loadFavorites()
  }

  let listController: AbortController | null = null
  /** Numbers each root's trash reads, so an older answer arriving late is dropped. */
  const trashSeq: Record<string, number> = {}

  async function loadList(): Promise<void> {
    const target = where.value
    if (!target) return
    listController?.abort()
    const request = new AbortController()
    listController = request
    listLoading.value = true
    try {
      const result = await listDir(target.root, target.path, hidden.value, request.signal)
      if (listController !== request) return
      list.value = result
      listError.value = null
    } catch (err) {
      if (isAbortError(err) || listController !== request) return
      list.value = null
      listError.value = err
    } finally {
      if (listController === request) {
        listController = null
        listLoading.value = false
      }
    }
  }

  /** Shows a folder; the list of the previous one goes at once. */
  function show(root: string, path: string): void {
    if (where.value?.root === root && where.value.path === path) return
    where.value = { root, path }
    list.value = null
    listError.value = null
    void loadList()
  }

  /**
   * The page is left: forget the roots, the folder, the favourites and the
   * trash, so coming back reads them afresh (also for another account
   * signing in on this tab).
   */
  function leave(): void {
    rootsSeq++
    roots.value = null
    rootsError.value = null
    listController?.abort()
    listController = null
    where.value = null
    list.value = null
    listError.value = null
    listLoading.value = false
    favorites.value = []
    for (const root of Object.keys(trashSeq)) trashSeq[root] = (trashSeq[root] ?? 0) + 1
    trash.value = {}
    trashErrors.value = {}
    trashLoading.value = {}
  }
  onAccountReset(leave)

  watch(hidden, (value) => {
    try {
      window.localStorage.setItem(HIDDEN_KEY, value ? '1' : '0')
    } catch {
      // Not remembered; the switch still works for this visit.
    }
    void loadList()
  })

  async function loadTrash(root: string): Promise<void> {
    const mine = (trashSeq[root] = (trashSeq[root] ?? 0) + 1)
    trashLoading.value = { ...trashLoading.value, [root]: true }
    try {
      const entries = await fetchTrash(root)
      if (trashSeq[root] !== mine) return
      trash.value = { ...trash.value, [root]: entries }
      const { [root]: _gone, ...errors } = trashErrors.value
      trashErrors.value = errors
    } catch (err) {
      if (trashSeq[root] === mine) trashErrors.value = { ...trashErrors.value, [root]: err }
    } finally {
      if (trashSeq[root] === mine) trashLoading.value = { ...trashLoading.value, [root]: false }
    }
  }

  /** Reads the trash of every available root (for the tab's count). */
  function loadAllTrash(): void {
    for (const root of available.value) void loadTrash(root.id)
  }

  /** Entries changed: read the folder and the trash lists again. */
  function changed(): void {
    void loadList()
    for (const root of Object.keys(trash.value)) void loadTrash(root)
  }

  /** Starts a file job and hands it to the job dock; the folder and trash reload when it ends. */
  function runJob(
    start: () => Promise<Job>,
    options: { rethrow?: boolean; onFinished?: (job: Job) => void } = {},
  ): Promise<Job | null> {
    return launchJob(start, {
      rethrow: options.rethrow,
      onFinished: (job) => {
        changed()
        options.onFinished?.(job)
      },
    })
  }

  return {
    roots,
    rootsError,
    favorites,
    where,
    list,
    listError,
    listLoading,
    hidden,
    trash,
    trashErrors,
    trashLoading,
    settings,
    available,
    trashCount,
    loadRoots,
    loadFavorites,
    isFavorite,
    setFavorite,
    loadList,
    show,
    leave,
    loadTrash,
    loadAllTrash,
    changed,
    runJob,
  }
})
