// SPDX-License-Identifier: AGPL-3.0-only
//
// Web terminal (`terminal.use`; `terminal.admin` for root terminals and the
// root switch). A session is a login shell on the server; the browser joins
// it over a WebSocket and can come back to it after a reload.
import { API_BASE, api } from './client'
import type { AccessSettings } from './files'

export type TermUserKind = 'site' | 'user' | 'root'

export interface TermUser {
  name: string
  kind: TermUserKind
  home: string
  /** The site this user owns (site owners only). */
  site?: string
}

export interface TermUsers {
  users: TermUser[]
  /** Root terminals are switched on. */
  rootTerminal: boolean
  /** The server's configuration forbids root terminals; the switch stays off. */
  locked?: boolean
}

export interface TermSession {
  id: string
  user: string
  shell: string
  home: string
  createdAt: number
  /** Someone is attached right now. */
  attached: boolean
}

/** State of the browser's connection to a session. */
export type TermConnection = 'connecting' | 'open' | 'reconnecting' | 'ended'

/** Most terminals one user may have open. */
export const TERMINALS_MAX = 8

export function fetchTermUsers(signal?: AbortSignal): Promise<TermUsers> {
  return api.get<TermUsers>('/terminal/users', { signal })
}

/** Administrators only; step-up protected. */
export function setRootTerminal(rootTerminal: boolean): Promise<AccessSettings> {
  return api.put<AccessSettings>('/terminal/settings', { rootTerminal })
}

export async function fetchTermSessions(signal?: AbortSignal): Promise<TermSession[]> {
  return (await api.get<{ sessions: TermSession[] }>('/terminal/sessions', { signal })).sessions
}

/** Step-up protected. */
export function openTermSession(user: string, cols: number, rows: number): Promise<TermSession> {
  return api.post<TermSession>('/terminal/sessions', { user, cols, rows })
}

export function closeTermSession(id: string): Promise<void> {
  return api.delete<void>(`/terminal/sessions/${encodeURIComponent(id)}`)
}

/** WebSocket address of a session (same origin; the session cookie signs in). */
export function termSocketUrl(id: string): string {
  const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws'
  return `${scheme}://${window.location.host}${API_BASE}/terminal/sessions/${encodeURIComponent(id)}/ws`
}
