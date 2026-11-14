// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  fetchAnnouncements,
  markAllAnnouncementsRead,
  markAnnouncementRead,
  type Announcement,
} from '@/api/announcements'

/** Official messages for the top-bar bell, the banner and the messages page. */
export const useAnnouncementsStore = defineStore('announcements', () => {
  const items = ref<Announcement[]>([])
  const unread = ref(0)
  const loaded = ref(false)
  const error = ref<unknown>(null)
  /** Critical banners hidden in this tab; they return after a reload until read. */
  const dismissed = ref(new Set<string>())

  let inFlight: Promise<void> | null = null
  let lastFetch = 0

  /** Fetches the list; concurrent calls share one request. Errors are kept in `error`. */
  function refresh(): Promise<void> {
    inFlight ??= (async () => {
      try {
        const list = await fetchAnnouncements()
        items.value = list.announcements
        unread.value = list.unread
        error.value = null
        loaded.value = true
      } catch (err) {
        error.value = err
      } finally {
        lastFetch = Date.now()
        inFlight = null
      }
    })()
    return inFlight
  }

  /** Refreshes unless the list was fetched less than `maxAgeMs` ago. */
  function refreshIfStale(maxAgeMs: number): void {
    if (Date.now() - lastFetch >= maxAgeMs) void refresh()
  }

  async function markRead(id: string): Promise<void> {
    const item = items.value.find((a) => a.id === id)
    if (item?.read) return
    await markAnnouncementRead(id)
    if (item) item.read = true
    unread.value = Math.max(0, unread.value - 1)
  }

  async function markAllRead(): Promise<void> {
    await markAllAnnouncementsRead()
    for (const item of items.value) item.read = true
    unread.value = 0
  }

  function dismiss(id: string): void {
    dismissed.value.add(id)
  }

  /** Unread critical messages that have not been dismissed in this tab. */
  const banners = computed(() =>
    items.value.filter((a) => a.level === 'critical' && !a.read && !dismissed.value.has(a.id)),
  )

  /** Forgets everything, for example after signing out. */
  function reset(): void {
    items.value = []
    unread.value = 0
    loaded.value = false
    error.value = null
    dismissed.value.clear()
    lastFetch = 0
  }

  return {
    items,
    unread,
    loaded,
    error,
    banners,
    refresh,
    refreshIfStale,
    markRead,
    markAllRead,
    dismiss,
    reset,
  }
})
