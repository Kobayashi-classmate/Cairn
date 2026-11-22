// SPDX-License-Identifier: AGPL-3.0-only
import type { Job, JobState } from '@/api/jobs'
import { i18n } from '@/i18n'
import { formatDuration } from '@/i18n/format'
import type { Tone } from '@/lib/tone'

/** Status colour of a job state (interface spec, section 3.3). */
export const JOB_TONE: Record<JobState, Tone> = {
  queued: 'neutral',
  running: 'progress',
  succeeded: 'success',
  failed: 'destructive',
  canceled: 'neutral',
  interrupted: 'warning',
}

export function jobTone(state: string): Tone {
  return JOB_TONE[state as JobState] ?? 'neutral'
}

/** How long a job ran (or runs so far); null before it started. */
export function jobDuration(job: Job, now: number): string | null {
  if (job.startedAt === 0) return null
  const end = job.finishedAt > 0 ? job.finishedAt : now
  return formatDuration(Math.max(0, end - job.startedAt))
}

/** Technical details of a failure (`reason`, program `output`), shown as they are. */
export function jobErrorDetails(job: Job): string[] {
  const params = job.error?.params ?? {}
  return ['reason', 'output', 'detail']
    .map((key) => params[key])
    .filter((value): value is string => typeof value === 'string' && value !== '')
}

/** Filters of the jobs list: one state, or "active" for queued and running. */
export const STATE_FILTERS = ['active', 'succeeded', 'failed', 'canceled', 'interrupted'] as const
export type StateFilter = (typeof STATE_FILTERS)[number]

export function stateFilterLabel(filter: StateFilter): string {
  const { t } = i18n.global
  return filter === 'active' ? t('jobs.filter.active') : t(`jobs.state.${filter}`)
}
