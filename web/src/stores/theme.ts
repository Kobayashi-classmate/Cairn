// SPDX-License-Identifier: AGPL-3.0-only
//
// Light/dark mode of the UI. The chosen mode ("light", "dark" or "system")
// resolves to light or dark; dark puts the `dark` class on <html>, which the
// design tokens in styles/main.css switch on. The choice is remembered in this
// browser under STORAGE_KEY so public/theme-boot.js can apply it before the
// first paint; signed in, the session store also saves it to the account,
// and the account's choice wins at sign-in.
import { defineStore } from 'pinia'
import { computed, ref, watchSyncEffect } from 'vue'
import { isThemeMode, type ThemeMode } from '@/api/session'

/** Must match public/theme-boot.js. */
export const THEME_STORAGE_KEY = 'cairn.theme'

const DARK_QUERY = '(prefers-color-scheme: dark)'

function readStoredMode(): ThemeMode | null {
  try {
    const value = window.localStorage.getItem(THEME_STORAGE_KEY)
    return isThemeMode(value) ? value : null
  } catch {
    // Storage unavailable (private browsing, blocked site data).
    return null
  }
}

function storeMode(mode: ThemeMode): void {
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, mode)
  } catch {
    // Storage unavailable: the choice lasts for this visit only.
  }
}

function darkQuery(): MediaQueryList | null {
  try {
    return window.matchMedia(DARK_QUERY)
  } catch {
    return null
  }
}

export const useThemeStore = defineStore('theme', () => {
  /** Pages before sign-in follow the system unless this browser chose otherwise. */
  const mode = ref<ThemeMode>(readStoredMode() ?? 'system')

  const query = darkQuery()
  const systemDark = ref(query?.matches ?? false)
  // Follow the operating system live while the mode is "system".
  query?.addEventListener('change', (event) => {
    systemDark.value = event.matches
  })

  // Another tab changed the mode.
  window.addEventListener('storage', (event) => {
    if (event.key === THEME_STORAGE_KEY && isThemeMode(event.newValue)) {
      mode.value = event.newValue
    }
  })

  const resolved = computed<'light' | 'dark'>(() => {
    if (mode.value === 'system') return systemDark.value ? 'dark' : 'light'
    return mode.value
  })
  const isDark = computed(() => resolved.value === 'dark')

  // Synchronous, so the class is in place before anything renders with the new mode
  // (the Naive UI overrides read the CSS variables it switches).
  watchSyncEffect(() => {
    const root = document.documentElement
    root.classList.toggle('dark', isDark.value)
    root.style.colorScheme = resolved.value
  })

  /** Applies a mode immediately and remembers it in this browser. */
  function setMode(next: ThemeMode): void {
    mode.value = next
    storeMode(next)
  }

  return { mode, resolved, isDark, setMode }
})
