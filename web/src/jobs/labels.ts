// SPDX-License-Identifier: AGPL-3.0-only
import type { JobState } from '@/api/jobs'
import { i18n } from '@/i18n'

const FINAL_STATES: ReadonlySet<string> = new Set(['succeeded', 'failed', 'canceled', 'interrupted'])

/** True once a job can no longer change. */
export function isFinalState(state: string): boolean {
  return FINAL_STATES.has(state)
}

/** Translated job type; a kind this build does not know is shown as is. */
export function jobKindLabel(kind: string): string {
  const key = `jobs.kind.${kind}`
  return i18n.global.te(key) ? i18n.global.t(key) : kind
}

export function jobStateLabel(state: string): string {
  const key = `jobs.state.${state}`
  return i18n.global.te(key) ? i18n.global.t(key) : state
}

export function jobStateTagType(state: JobState | string): 'default' | 'info' | 'success' | 'error' | 'warning' {
  switch (state) {
    case 'running':
      return 'info'
    case 'succeeded':
      return 'success'
    case 'failed':
      return 'error'
    case 'interrupted':
      return 'warning'
    default:
      return 'default'
  }
}

/** Who started a job: the user, or "System" for jobs the panel starts itself. */
export function jobActorLabel(username: string): string {
  return username || i18n.global.t('audit.noUser')
}

/** Translated origin of a request (web, api, cli, system); unknown values as is. */
export function sourceLabel(source: string): string {
  const key = `audit.source.${source}`
  return i18n.global.te(key) ? i18n.global.t(key) : source
}
