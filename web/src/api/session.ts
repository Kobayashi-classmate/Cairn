// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './client'
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

/** First sign-in step for an account with two-factor authentication. */
export interface MfaChallenge {
  mfaRequired: true
  mfaToken: string
}

export type LoginResponse = SessionInfo | MfaChallenge

export function isMfaChallenge(response: LoginResponse): response is MfaChallenge {
  return 'mfaRequired' in response && response.mfaRequired === true
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

export function login(body: LoginRequest): Promise<LoginResponse> {
  // 401 here means wrong credentials, not an expired session.
  return api.post<LoginResponse>('/auth/login', body, { skipAuthRedirect: true })
}

export function loginMfa(body: LoginMfaRequest): Promise<SessionInfo> {
  return api.post<SessionInfo>('/auth/login/mfa', body, { skipAuthRedirect: true })
}

export function logout(): Promise<void> {
  return api.post<void>('/auth/logout', undefined, { skipAuthRedirect: true })
}

/** Confirms the user's identity again; sensitive actions are allowed until `elevatedUntil`. */
export function elevate(body: ElevateRequest): Promise<{ elevatedUntil: number }> {
  return api.post<{ elevatedUntil: number }>('/auth/elevate', body, { skipStepUp: true })
}

/** Creates the first administrator; the server signs the new user in. */
export function completeSetup(body: SetupRequest): Promise<SessionInfo> {
  return api.post<SessionInfo>('/setup', body, { skipAuthRedirect: true })
}

export function updateMyLocale(locale: string): Promise<void> {
  return api.put<void>('/me/locale', { locale })
}

export function updateMyTheme(theme: ThemeMode): Promise<void> {
  return api.put<void>('/me/theme', { theme })
}
