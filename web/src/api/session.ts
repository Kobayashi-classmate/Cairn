// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './client'

/** Public panel metadata, available without a session. */
export interface Meta {
  version: string
  setupRequired: boolean
  defaultLocale: string
  locales: string[]
}

export interface User {
  id: number
  username: string
  locale: string
}

export interface UserResponse {
  user: User
}

export interface LoginRequest {
  username: string
  password: string
}

export interface SetupRequest {
  token: string
  username: string
  password: string
  locale: string
}

export function fetchMeta(): Promise<Meta> {
  return api.get<Meta>('/meta')
}

/** Returns the current session's user; rejects with 401 when signed out. */
export function fetchCurrentUser(): Promise<UserResponse> {
  return api.get<UserResponse>('/auth/me', { skipAuthRedirect: true })
}

export function login(body: LoginRequest): Promise<UserResponse> {
  // 401 here means wrong credentials, not an expired session.
  return api.post<UserResponse>('/auth/login', body, { skipAuthRedirect: true })
}

export function logout(): Promise<void> {
  return api.post<void>('/auth/logout', undefined, { skipAuthRedirect: true })
}

/** Creates the first administrator; the server signs the new user in. */
export function completeSetup(body: SetupRequest): Promise<UserResponse> {
  return api.post<UserResponse>('/setup', body, { skipAuthRedirect: true })
}

export function updateMyLocale(locale: string): Promise<void> {
  return api.put<void>('/me/locale', { locale })
}
