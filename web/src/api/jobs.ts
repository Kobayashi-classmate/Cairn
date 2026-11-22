// SPDX-License-Identifier: AGPL-3.0-only
//
// Background jobs (task center). Every signed-in user may call these; the
// server only returns jobs the caller is allowed to see.
import { api, type ErrorParams } from './client'

export type JobState = 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled' | 'interrupted'

export interface Job {
  id: string
  /** Job type, for example "software.refresh"; translated as jobs.kind.<kind>. */
  kind: string
  params: Record<string, unknown>
  state: JobState
  /** 0..1, or null when the job cannot tell. */
  progress: number | null
  /** Why the job failed, as an error code with parameters. */
  error?: { code: string; params?: ErrorParams }
  /** Whether the job can be canceled right now. */
  cancelable: boolean
  username: string
  /** "web", "api" or "system". */
  source: string
  /** Unix seconds; 0 = not yet. */
  createdAt: number
  startedAt: number
  finishedAt: number
}

export interface JobPage {
  /** Newest first. */
  jobs: Job[]
  /** Opaque cursor: pass it back unchanged as `before` for the next page; null on the last page. */
  nextBefore: number | null
}

export interface JobLogChunk {
  job: Job
  /** Log text from the requested offset. Plain text, never HTML. */
  log: string
  /** Offset to continue from. */
  offset: number
  /** Current total length of the log. */
  logSize: number
}

export interface JobQuery {
  limit?: number
  before?: number
  /** A job state, or "active" (queued or running). */
  state?: string
  /** A job kind, or a prefix ending in "." ("software."). */
  kind?: string
}

export function fetchJobs(query: JobQuery, signal?: AbortSignal): Promise<JobPage> {
  const params = new URLSearchParams()
  if (query.limit !== undefined) params.set('limit', String(query.limit))
  if (query.before !== undefined) params.set('before', String(query.before))
  if (query.state) params.set('state', query.state)
  if (query.kind) params.set('kind', query.kind)
  const search = params.toString()
  return api.get<JobPage>(`/jobs${search ? `?${search}` : ''}`, { signal })
}

export async function fetchJob(id: string, signal?: AbortSignal): Promise<Job> {
  return (await api.get<{ job: Job }>(`/jobs/${encodeURIComponent(id)}`, { signal })).job
}

/**
 * Returns the job and its log from `offset`. With `waitSeconds` > 0 the server
 * holds the request (up to 25 s) until there is new output or a state change.
 */
export function followJobLog(
  id: string,
  offset: number,
  waitSeconds: number,
  signal?: AbortSignal,
): Promise<JobLogChunk> {
  const query = `offset=${offset}&wait=${waitSeconds}`
  return api.get<JobLogChunk>(`/jobs/${encodeURIComponent(id)}/log?${query}`, { signal })
}

export async function cancelJob(id: string): Promise<Job> {
  return (await api.post<{ job: Job }>(`/jobs/${encodeURIComponent(id)}/cancel`)).job
}
