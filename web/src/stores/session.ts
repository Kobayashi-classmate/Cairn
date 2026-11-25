// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { isApiError, type ApiError } from '@/api/client'
import { borrowSessionKey, clearSessionKey, sessionKey, shareSessionKey } from '@/api/credentials'
import * as sessionApi from '@/api/session'
import { isMfaChallenge, isThemeMode } from '@/api/session'
import type {
  ElevateRequest,
  LoginMfaRequest,
  LoginRequest,
  Meta,
  SessionInfo,
  SetupRequest,
  ThemeMode,
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
import { resetAccountData } from './accountReset'
import { useNavStore } from './nav'
import { useThemeStore } from './theme'

/** Panel metadata, the signed-in user and the UI language that follows from them. */
export const useSessionStore = defineStore('session', () => {
  const meta = ref<Meta | null>(null)
  const user = ref<User | null>(null)
  const permissions = ref<Permission[]>([])
  /** Unix seconds until which sensitive actions need no re-authentication; 0 if not elevated. */
  const elevatedUntil = ref(0)
  /**
   * Whether sensitive actions ask for the password again (the panel's
   * step-up switch). Assumed on until the server says otherwise.
   */
  const stepUpRequired = ref(true)
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
    stepUpRequired.value = info.stepUpRequired ?? true
    // The account's colour scheme wins over the one remembered in this browser.
    if (isThemeMode(info.user.theme)) useThemeStore().setMode(info.user.theme)
  }

  function clearSession(): void {
    // The key is of no use without its session, and must not stay behind.
    clearSessionKey()
    user.value = null
    permissions.value = []
    elevatedUntil.value = 0
    stepUpRequired.value = true
    useNavStore().clear()
    // The pages' data (lists, scripts, uploads) is this account's only.
    resetAccountData()
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

  /**
   * The session this tab can continue, or null when it has to sign in. The
   * cookie the browser sends is not enough: the tab needs the session key,
   * its own (kept across reloads) or, in a new tab, the one a tab already
   * open hands over. A key the server no longer takes is dropped.
   */
  async function restoreSession(): Promise<SessionInfo | null> {
    for (const borrow of [false, true]) {
      if (borrow) {
        clearSessionKey()
        if (!(await borrowSessionKey())) return null
      } else if (!sessionKey()) {
        continue
      }
      try {
        return await sessionApi.fetchSession()
      } catch (err) {
        if (!(isApiError(err) && err.status === 401)) throw err
      }
    }
    clearSessionKey()
    return null
  }

  let startup: Promise<void> | null = null

  /** Loads metadata and the current session once; later calls reuse the result. */
  function init(): Promise<void> {
    startup ??= (async () => {
      shareSessionKey()
      try {
        meta.value = await sessionApi.fetchMeta()
        if (!meta.value.setupRequired) {
          const info = await restoreSession()
          if (info) {
            applySession(info)
            try {
              await useNavStore().load()
            } catch (err) {
              if (!(isApiError(err) && err.status === 401)) throw err
            }
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
    // The menu decides where the user lands, so load it before navigating.
    await useNavStore().load()
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
    await useNavStore().load()
    await applyPreferredLocale()
  }

  /** Re-authenticates for sensitive actions. */
  async function elevate(body: ElevateRequest): Promise<void> {
    elevatedUntil.value = (await sessionApi.elevate(body)).elevatedUntil
  }

  /**
   * Keeps the step-up switch in step: after it was changed here, read with
   * the panel settings, or found on by a request the server refused.
   */
  function setStepUpRequired(required: boolean): void {
    stepUpRequired.value = required
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

  let themeChanges = 0

  /**
   * Switches between light, dark and following the system immediately.
   * Signed in, the choice is saved to the user's account (and undone if that
   * fails); it is always remembered in this browser as well, for the sign-in
   * page and the first paint.
   */
  async function changeTheme(mode: ThemeMode): Promise<void> {
    const theme = useThemeStore()
    const previous = theme.mode
    if (mode === previous) return
    theme.setMode(mode)
    if (!user.value) return
    const change = ++themeChanges
    try {
      await sessionApi.updateMyTheme(mode)
      if (user.value) user.value.theme = mode
    } catch (err) {
      // A newer choice has been made meanwhile; that one decides.
      if (change === themeChanges) theme.setMode(previous)
      throw err
    }
  }

  return {
    meta,
    user,
    permissions,
    elevatedUntil,
    stepUpRequired,
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
    setStepUpRequired,
    sessionEnded,
    setTotpEnabled,
    setupAlreadyCompleted,
    signOut,
    sessionLost,
    takeNotice,
    changeLocale,
    changeTheme,
  }
})
