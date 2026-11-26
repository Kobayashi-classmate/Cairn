// SPDX-License-Identifier: AGPL-3.0-only
//
// Online update of the Cairn package itself (no other system software).
// Viewing needs `settings.read`; checking and installing need `settings.manage`.
import { api, type RequestOptions } from './client'
import type { LocalizedText } from './announcements'

/** Why updates cannot be offered (when `status` is "unavailable"). */
export type UpdateUnavailableReason =
  | 'dev_build'
  | 'cloud_disabled'
  | 'no_trusted_key'
  | 'fetch_failed'
  | 'verification_failed'

export interface ReleaseInfo {
  version: string
  /** Unix seconds. */
  publishedAt: number
  /** The release fixes security issues. */
  security: boolean
  /** Limited Markdown per locale. */
  notes: LocalizedText
}

/**
 * `interrupted`: the run ended without finishing (the server restarted, or its
 * unit was stopped); the package manager may need a look before a retry.
 */
export type UpdateJobState = 'idle' | 'running' | 'succeeded' | 'failed' | 'interrupted'

export interface UpdateJob {
  state: UpdateJobState
  /** Version being (or last) installed. */
  version: string
  /** Unix seconds. */
  startedAt: number
  /** Unix seconds. */
  finishedAt: number
  /** Technical outcome of a failed or interrupted run, in English. */
  error: string
  /** Output of the update run, shown when it fails. */
  log: string
}

export interface UpdateStatus {
  current: string
  channel: string
  status: 'up_to_date' | 'available' | 'unavailable'
  reason: string
  latest: ReleaseInfo | null
  /** Unix seconds of the last check. */
  checkedAt: number
  job: UpdateJob
}

export function fetchUpdateStatus(options?: RequestOptions): Promise<UpdateStatus> {
  return api.get<UpdateStatus>('/updates', options)
}

/** Asks the release server again and returns the fresh status. */
export function checkForUpdates(): Promise<UpdateStatus> {
  return api.post<UpdateStatus>('/updates/check')
}

/**
 * Starts installing a version (step-up). The panel restarts while the job
 * runs, so callers must expect failed requests until it is back.
 */
export async function applyUpdate(version: string): Promise<UpdateJob> {
  return (await api.post<{ job: UpdateJob }>('/updates/apply', { version })).job
}
