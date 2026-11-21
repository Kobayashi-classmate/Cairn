// SPDX-License-Identifier: AGPL-3.0-only
import type { Job } from '@/api/jobs'
import { notifyError } from '@/lib/notify'
import { useJobDockStore, type TrackOptions } from '@/stores/jobDock'

/**
 * Starts a job and hands it to the job dock. Returns the job, or null when
 * it could not be started; the reason is shown as a toast unless `rethrow`
 * asks for it (forms show errors next to their fields).
 */
export async function launchJob(
  start: () => Promise<Job>,
  options: TrackOptions & { rethrow?: boolean } = {},
): Promise<Job | null> {
  let job: Job
  try {
    job = await start()
  } catch (err) {
    if (options.rethrow) throw err
    notifyError(err)
    return null
  }
  useJobDockStore().track(job, { onFinished: options.onFinished })
  return job
}
