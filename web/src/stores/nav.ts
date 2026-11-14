// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { fetchNav, type NavEntry } from '@/api/nav'

/** Used when the menu cannot be loaded: settings is open to every user. */
const FALLBACK: NavEntry[] = [
  { key: 'settings', labelKey: 'common.nav.settings', icon: 'settings', route: 'settings', order: 0 },
]

/** The sider menu, loaded from the server after sign-in and after permission changes. */
export const useNavStore = defineStore('nav', () => {
  const entries = ref<NavEntry[]>([])

  /** Reloads the menu. On failure the current menu stays (or the fallback when empty). */
  async function load(): Promise<void> {
    try {
      entries.value = await fetchNav()
    } catch (err) {
      console.warn('cairn: failed to load the menu', err)
      if (entries.value.length === 0) entries.value = FALLBACK
    }
  }

  function clear(): void {
    entries.value = []
  }

  return { entries, load, clear }
})
