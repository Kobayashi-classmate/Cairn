// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { isApiError, type ApiError } from '@/api/client'
import * as sessionApi from '@/api/session'
import { isMfaChallenge } from '@/api/session'
import type {
  ElevateRequest,
  LoginMfaRequest,
  LoginRequest,
  Meta,
  SessionInfo,
  SetupRequest,
  User,
} from '@/api/session'
import type { Permission } from '@/auth/permissions'
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
  const permissions = ref<Permission[]>([])
  /** Unix seconds until which sensitive actions need no re-authentication; 0 if not elevated. */
  const elevatedUntil = ref(0)
  /** True once startup has finished, successfully or not. */
  const initialized = ref(false)
  /** Set when the panel could not be loaded at startup. */
  const bootError = ref<unknown>(null)
  /** Why the user was sent to the sign-in page, shown there once. */
  const notice = ref<ApiError | null>(null)

  const isAuthenticated = computed(() => user.value !== null)

  function can(permission: Permission): boolean {
    return permissions.value.includes(permission)
  }

  function applySession(info: SessionInfo): void {
    user.value = info.user
    permissions.value = info.permissions ?? []
    elevatedUntil.value = info.elevatedUntil ?? 0
  }

  function clearSession(): void {
    user.value = null
    permissions.value = []
    elevatedUntil.value = 0
  }
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
            applySession(await sessionApi.fetchSession())
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

  async function finishSignIn(info: SessionInfo): Promise<void> {
    applySession(info)
    notice.value = null
    await applyPreferredLocale()
  }

  /**
   * First sign-in step. Returns the challenge token when the account uses
   * two-factor authentication and a second step is needed, otherwise null.
   */
  async function signIn(body: LoginRequest): Promise<string | null> {
    const response = await sessionApi.login(body)
    if (isMfaChallenge(response)) return response.mfaToken
    await finishSignIn(response)
    return null
  }

  /** Second sign-in step with an authenticator or recovery code. */
  async function signInMfa(body: LoginMfaRequest): Promise<void> {
    await finishSignIn(await sessionApi.loginMfa(body))
  }

  async function completeSetup(body: SetupRequest): Promise<void> {
    const info = await sessionApi.completeSetup(body)
    if (meta.value) meta.value.setupRequired = false
    applySession(info)
    await applyPreferredLocale()
  }

  /** Re-authenticates for sensitive actions. */
  async function elevate(body: ElevateRequest): Promise<void> {
    elevatedUntil.value = (await sessionApi.elevate(body)).elevatedUntil
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
    clearSession()
  }

  /** Keeps the profile in step after two-factor authentication is turned on or off. */
  function setTotpEnabled(enabled: boolean): void {
    if (user.value) user.value.totpEnabled = enabled
  }

  /** The current session was ended on purpose (for example from the sessions list). */
  function sessionEnded(): void {
    clearSession()
  }

  /**
   * The server no longer accepts the session. Returns false when the user was
   * already signed out, so concurrent 401 responses are handled only once.
   */
  function sessionLost(reason: ApiError): boolean {
    if (!user.value) return false
    clearSession()
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
    permissions,
    elevatedUntil,
    initialized,
    bootError,
    notice,
    isAuthenticated,
    setupRequired,
    supportedLocales,
    can,
    init,
    signIn,
    signInMfa,
    completeSetup,
    elevate,
    sessionEnded,
    setTotpEnabled,
    setupAlreadyCompleted,
    signOut,
    sessionLost,
    takeNotice,
    changeLocale,
  }
})
