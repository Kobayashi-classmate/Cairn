// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { isApiError, type ApiError } from '@/api/client'
import * as sessionApi from '@/api/session'
import type { LoginRequest, Meta, SetupRequest, User } from '@/api/session'
import {
  SOURCE_LOCALE,
  activateLocale,
  bundledLocales,
  currentLocale,
  loadLocaleMessages,
} from '@/i18n'
import { matchLocale } from '@/i18n/negotiate'
import { readStoredLocale, storeLocale } from '@/i18n/preference'

/** Panel metadata, the signed-in user and the UI language that follows from them. */
export const useSessionStore = defineStore('session', () => {
  const meta = ref<Meta | null>(null)
  const user = ref<User | null>(null)
  /** True once startup has finished, successfully or not. */
  const initialized = ref(false)
  /** Set when the panel could not be loaded at startup. */
  const bootError = ref<unknown>(null)
  /** Why the user was sent to the sign-in page, shown there once. */
  const notice = ref<ApiError | null>(null)

  const isAuthenticated = computed(() => user.value !== null)
  const setupRequired = computed(() => meta.value?.setupRequired === true)

  /** Locales both bundled in this build and enabled on the server. */
  const supportedLocales = computed<readonly string[]>(() => {
    const enabled = meta.value?.locales
    if (!enabled) return bundledLocales
    return bundledLocales.filter((locale) => locale === SOURCE_LOCALE || enabled.includes(locale))
  })

  function supported(locale: string | null | undefined): string | undefined {
    return locale ? matchLocale([locale], supportedLocales.value) : undefined
  }

  /**
   * Precedence: the user's own setting; when signed out (sign-in and setup
   * pages only), the choice remembered in this browser, then the browser's
   * languages; then the panel default; then English.
   */
  function preferredLocale(): string {
    const signedOut = user.value
      ? undefined
      : (supported(readStoredLocale()) ?? matchLocale(navigator.languages, supportedLocales.value))
    return (
      supported(user.value?.locale) ??
      signedOut ??
      supported(meta.value?.defaultLocale) ??
      SOURCE_LOCALE
    )
  }

  async function applyPreferredLocale(): Promise<void> {
    const locale = preferredLocale()
    try {
      await activateLocale(locale)
    } catch (err) {
      // The UI stays in its current language; a later switch can retry.
      console.warn(`cairn: failed to load locale "${locale}"`, err)
    }
  }

  let startup: Promise<void> | null = null

  /** Loads metadata and the current session once; later calls reuse the result. */
  function init(): Promise<void> {
    startup ??= (async () => {
      try {
        meta.value = await sessionApi.fetchMeta()
        if (!meta.value.setupRequired) {
          try {
            user.value = (await sessionApi.fetchCurrentUser()).user
          } catch (err) {
            if (!(isApiError(err) && err.status === 401)) throw err
          }
        }
      } catch (err) {
        bootError.value = err
      }
      await applyPreferredLocale()
      initialized.value = true
    })()
    return startup
  }

  async function signIn(body: LoginRequest): Promise<void> {
    user.value = (await sessionApi.login(body)).user
    notice.value = null
    await applyPreferredLocale()
  }

  async function completeSetup(body: SetupRequest): Promise<void> {
    const { user: created } = await sessionApi.completeSetup(body)
    if (meta.value) meta.value.setupRequired = false
    user.value = created
    await applyPreferredLocale()
  }

  /** Setup turned out to be done already (for example from another browser). */
  function setupAlreadyCompleted(reason: ApiError): void {
    if (meta.value) meta.value.setupRequired = false
    notice.value = reason
  }

  async function signOut(): Promise<void> {
    try {
      await sessionApi.logout()
    } catch (err) {
      // 401: the session had already ended, which is what we want.
      if (!(isApiError(err) && err.status === 401)) throw err
    }
    user.value = null
  }

  /**
   * The server no longer accepts the session. Returns false when the user was
   * already signed out, so concurrent 401 responses are handled only once.
   */
  function sessionLost(reason: ApiError): boolean {
    if (!user.value) return false
    user.value = null
    notice.value = reason
    return true
  }

  /** Returns the pending sign-in notice and clears it. */
  function takeNotice(): ApiError | null {
    const pending = notice.value
    notice.value = null
    return pending
  }

  /**
   * Switches the UI language immediately. Signed in, the choice is saved to
   * the user's account; signed out, it is remembered in this browser.
   */
  async function changeLocale(locale: string): Promise<void> {
    if (locale === currentLocale()) return
    // Download the messages first so a failure leaves everything unchanged.
    await loadLocaleMessages(locale)
    if (user.value) {
      await sessionApi.updateMyLocale(locale)
      user.value.locale = locale
    } else {
      storeLocale(locale)
    }
    await activateLocale(locale)
  }

  return {
    meta,
    user,
    initialized,
    bootError,
    notice,
    isAuthenticated,
    setupRequired,
    supportedLocales,
    init,
    signIn,
    completeSetup,
    setupAlreadyCompleted,
    signOut,
    sessionLost,
    takeNotice,
    changeLocale,
  }
})
