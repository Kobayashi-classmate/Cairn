// SPDX-License-Identifier: AGPL-3.0-only
import type { CronTask, RunResult } from '@/api/cron'
import { i18n } from '@/i18n'
import { errorText } from '@/i18n/errors'
import type { Tone } from '@/lib/tone'

/** Colour of a run's result. */
export function resultTone(result: RunResult | string | undefined): Tone {
  switch (result) {
    case 'success':
      return 'success'
    case 'failed':
      return 'destructive'
    case 'timeout':
      return 'warning'
    case 'running':
      return 'progress'
    default:
      return 'neutral'
  }
}

export function resultText(result: RunResult | string): string {
  const { t, te } = i18n.global
  return te(`cron.result.${result}`) ? t(`cron.result.${result}`) : result
}

/** A task's own state: being applied, failed to apply, off or on. */
export function taskState(task: CronTask): { tone: Tone; text: string } {
  const { t } = i18n.global
  if (task.state === 'applying') return { tone: 'progress', text: t('cron.state.applying') }
  if (task.state === 'failed') return { tone: 'destructive', text: t('cron.state.failed') }
  if (!task.enabled) return { tone: 'neutral', text: t('cron.state.disabled') }
  return { tone: 'success', text: t('cron.state.active') }
}

/** Why applying the task failed. */
export function taskFailure(task: CronTask): string {
  return task.errorCode ? (errorText(task.errorCode) ?? task.errorCode) : ''
}
