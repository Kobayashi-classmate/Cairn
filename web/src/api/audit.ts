// SPDX-License-Identifier: AGPL-3.0-only
import { api, type ErrorParams } from './client'

export interface AuditEntry {
  id: number
  /** Unix seconds. */
  ts: number
  userId: number
  username: string
  /** Action code, for example "users.create". */
  action: string
  params: ErrorParams
  /** "ok", or the error code the action failed with. */
  result: string
  remote: string
  /** Where the action came from, for example "web" or "cli". */
  source: string
}

export interface AuditPage {
  /** Newest first. */
  entries: AuditEntry[]
  /** Pass as `before` to load the next (older) page; null on the last page. */
  nextBefore: number | null
}

export interface AuditQuery {
  limit?: number
  before?: number
  user?: string
  /** Action code prefix, for example "users.". */
  action?: string
}

export function fetchAudit(query: AuditQuery, signal?: AbortSignal): Promise<AuditPage> {
  const params = new URLSearchParams()
  if (query.limit !== undefined) params.set('limit', String(query.limit))
  if (query.before !== undefined) params.set('before', String(query.before))
  if (query.user) params.set('user', query.user)
  if (query.action) params.set('action', query.action)
  const search = params.toString()
  return api.get<AuditPage>(`/audit${search ? `?${search}` : ''}`, { signal })
}
