// SPDX-License-Identifier: AGPL-3.0-only

/**
 * Language chosen on the sign-in or setup page, remembered in this browser.
 * Storage may be unavailable (private browsing, blocked site data), so every
 * access is allowed to fail silently: the choice then lasts for this visit only.
 */
const STORAGE_KEY = 'cairn.locale'

export function readStoredLocale(): string | null {
  try {
    return window.localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

export function storeLocale(locale: string): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, locale)
  } catch {
    // See the note above.
  }
}
