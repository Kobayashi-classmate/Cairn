// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './client'
import { deviceKey, setDeviceKey, setSessionKey } from './credentials'
import type { Permission, Role } from '@/auth/permissions'

/** Public panel metadata, available without a session. */
export interface Meta {
  version: string
  setupRequired: boolean
  defaultLocale: string
  locales: string[]
}

/** Colour scheme of the UI: fixed light or dark, or following the operating system. */
export type ThemeMode = 'light' | 'dark' | 'system'

export const THEME_MODES: readonly ThemeMode[] = ['light', 'dark', 'system']

export function isThemeMode(value: unknown): value is ThemeMode {
  return typeof value === 'string' && (THEME_MODES as readonly string[]).includes(value)
}

export interface User {
  id: number
  username: string
  locale: string
  /** Missing from servers older than the theme preference. */
  theme?: ThemeMode
  role: Role
  totpEnabled: boolean
}

/** The signed-in user and what they may do. */
export interface SessionInfo {
  user: User
  permissions: Permission[]
  /** Unix seconds until which step-up is not needed; 0 when not elevated. Only sent by /auth/me. */
  elevatedUntil?: number
}

/**
 * The answer to a completed sign-in. Only it carries the session key (and,
 * on a browser the account did not use before, the device key): they are
 * put away at once and never reach the rest of the app.
 */
interface SignedIn extends SessionInfo {
  sessionKey: string
  deviceKey?: string
}

/** First sign-in step for an account with two-factor authentication. */
export interface MfaChallenge {
  mfaRequired: true
  mfaToken: string
}

export type LoginResponse = SessionInfo | MfaChallenge

export function isMfaChallenge(response: LoginResponse): response is MfaChallenge {
  return 'mfaRequired' in response && response.mfaRequired === true
}

/** Keeps the keys of a sign-in in this page's storage and returns the rest. */
function keepKeys(answer: SignedIn): SessionInfo {
  const { sessionKey, deviceKey: device, ...info } = answer
  setSessionKey(sessionKey)
  if (device) setDeviceKey(device)
  return info
}

/**
 * The device key, sent with a sign-in: with the device cookie it shows the
 * account signed in from this browser before, which keeps failed attempts
 * made elsewhere with the same username from locking it out here.
 */
function deviceHeaders(): Record<string, string> {
  const key = deviceKey()
  return key ? { 'X-Cairn-Device': key } : {}
}

export interface LoginRequest {
  username: string
  password: string
}

/** Second sign-in step: exactly one of `code` and `recoveryCode`. */
export type LoginMfaRequest =
  | { mfaToken: string; code: string }
  | { mfaToken: string; recoveryCode: string }

export interface SetupRequest {
  token: string
  username: string
  password: string
  locale: string
}

export interface ElevateRequest {
  password: string
  code?: string
}

export function fetchMeta(): Promise<Meta> {
  return api.get<Meta>('/meta')
}

/** Returns the current session; rejects with 401 when signed out. */
export function fetchSession(): Promise<SessionInfo> {
  return api.get<SessionInfo>('/auth/me', { skipAuthRedirect: true })
}

export async function login(body: LoginRequest): Promise<LoginResponse> {
  // 401 here means wrong credentials, not an expired session.
  const answer = await api.post<SignedIn | MfaChallenge>('/auth/login', body, {
    skipAuthRedirect: true,
    headers: deviceHeaders(),
  })
  return isMfaChallenge(answer) ? answer : keepKeys(answer)
}

export async function loginMfa(body: LoginMfaRequest): Promise<SessionInfo> {
  return keepKeys(
    await api.post<SignedIn>('/auth/login/mfa', body, { skipAuthRedirect: true, headers: deviceHeaders() }),
  )
}

export function logout(): Promise<void> {
  return api.post<void>('/auth/logout', undefined, { skipAuthRedirect: true })
}

/** Confirms the user's identity again; sensitive actions are allowed until `elevatedUntil`. */
export function elevate(body: ElevateRequest): Promise<{ elevatedUntil: number }> {
  return api.post<{ elevatedUntil: number }>('/auth/elevate', body, { skipStepUp: true })
}

/** Creates the first administrator; the server signs the new user in. */
export async function completeSetup(body: SetupRequest): Promise<SessionInfo> {
  return keepKeys(await api.post<SignedIn>('/setup', body, { skipAuthRedirect: true }))
}

/**
 * Returns an API address (path and query, starting with the API base) with
 * a one-time ticket added, for a request the browser makes by itself and
 * that therefore cannot carry the session key: a WebSocket, a download
 * link, an image. The ticket is for that address only, works once and
 * expires within seconds: ask for it right before use, never keep it.
 */
export async function withTicket(path: string, signal?: AbortSignal): Promise<string> {
  const { ticket } = await api.post<{ ticket: string }>('/auth/tickets', { path }, { signal })
  return `${path}${path.includes('?') ? '&' : '?'}ticket=${encodeURIComponent(ticket)}`
}

export function updateMyLocale(locale: string): Promise<void> {
  return api.put<void>('/me/locale', { locale })
}

export function updateMyTheme(theme: ThemeMode): Promise<void> {
  return api.put<void>('/me/theme', { theme })
}
