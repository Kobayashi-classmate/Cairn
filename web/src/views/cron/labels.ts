// SPDX-License-Identifier: AGPL-3.0-only
import type { RunResult } from '@/api/cron'

/** Tag colour of a run's result. */
export function resultType(result: RunResult | string | undefined): 'default' | 'info' | 'success' | 'error' | 'warning' {
  switch (result) {
    case 'success':
      return 'success'
    case 'failed':
      return 'error'
    case 'timeout':
      return 'warning'
    case 'running':
      return 'info'
    default:
      return 'default'
  }
}
