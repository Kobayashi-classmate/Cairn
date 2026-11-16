// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'
import type { Job } from '@/api/jobs'
import { useErrorToast } from './useErrorToast'

/**
 * Starts jobs from buttons and follows the current one inline (with
 * JobProgress). Errors are shown as toasts; a declined re-authentication
 * stays silent.
 */
export function useJobRun(onFinished: (job: Job) => void) {
  const showError = useErrorToast()
  const jobId = ref<string | null>(null)
  const busy = ref(false)

  async function run(start: () => Promise<Job>): Promise<void> {
    busy.value = true
    try {
      jobId.value = (await start()).id
    } catch (err) {
      showError(err)
    } finally {
      busy.value = false
    }
  }

  /** Follows a job started elsewhere (for example by a dialog). */
  function follow(job: Job) {
    jobId.value = job.id
  }

  function finished(job: Job) {
    onFinished(job)
  }

  function dismiss() {
    jobId.value = null
  }

  return { jobId, busy, run, follow, finished, dismiss }
}
