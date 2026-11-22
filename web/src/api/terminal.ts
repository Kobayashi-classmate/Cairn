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

/** A saved command of the signed-in account. */
export interface TermSnippet {
  id: number
  name: string
  /** Lines separated by "\n". */
  command: string
  /** Unix seconds. */
  updatedAt: number
}

export interface TermSnippetRequest {
  name: string
  command: string
}

/** Most saved commands one account keeps. */
export const SNIPPETS_MAX = 100
/** Longest name (characters, after trimming). */
export const SNIPPET_NAME_MAX = 64
/** Longest command (bytes of UTF-8). */
export const SNIPPET_COMMAND_MAX = 8192

/** The account's saved commands, sorted by name. */
export async function fetchSnippets(signal?: AbortSignal): Promise<TermSnippet[]> {
  return (await api.get<{ snippets: TermSnippet[] }>('/terminal/snippets', { signal })).snippets
}

export function createSnippet(body: TermSnippetRequest): Promise<TermSnippet> {
  return api.post<TermSnippet>('/terminal/snippets', body)
}

export function updateSnippet(id: number, body: TermSnippetRequest): Promise<TermSnippet> {
  return api.put<TermSnippet>(`/terminal/snippets/${id}`, body)
}

export function deleteSnippet(id: number): Promise<void> {
  return api.delete<void>(`/terminal/snippets/${id}`)
}
