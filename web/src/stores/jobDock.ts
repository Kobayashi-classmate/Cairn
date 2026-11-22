// SPDX-License-Identifier: AGPL-3.0-only
//
// The job dock (interface spec, section 7): long jobs started from a page
// get a card in the bottom right corner that follows them, also after
// leaving the page. Succeeded cards leave 5 seconds after the job ends;
// failed ones stay until dismissed. The ids of the cards are kept for this
// browser tab, so a reload keeps following them.
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { isApiError } from '@/api/client'
import { fetchJob, type Job } from '@/api/jobs'
import { i18n } from '@/i18n'
import { isFinalState, jobKindLabel, jobStateLabel } from '@/jobs/labels'
import { onAccountReset } from './accountReset'

export interface DockEntry {
  id: string
  /** Latest known state; null until the first answer after a reload. */
  job: Job | null
  /** The job ended while followed here. */
  done: boolean
}

export interface TrackOptions {
  /** Called once when the job ends (any final state), for example to reload a list. */
  onFinished?: (job: Job) => void
}

/** Most cards kept; older ones are dropped. */
const MAX_ENTRIES = 8
/** How long a succeeded card stays. */
const SUCCESS_LINGER_MS = 5000
/** How often a hidden job that a page waits for is checked. */
const HIDDEN_POLL_MS = 3000
const STORAGE_KEY = 'cairn.jobDock'

function readStored(): string[] {
  try {
    const raw: unknown = JSON.parse(window.sessionStorage.getItem(STORAGE_KEY) ?? '[]')
    return Array.isArray(raw) ? raw.filter((id): id is string => typeof id === 'string').slice(0, MAX_ENTRIES) : []
  } catch {
    return []
  }
}

export const useJobDockStore = defineStore('jobDock', () => {
  const entries = ref<DockEntry[]>(readStored().map((id) => ({ id, job: null, done: false })))
  const collapsed = ref(false)
  /** Last state change, read out by screen readers. */
  const announcement = ref('')
  /** Height of the dock in pixels, so toasts can sit above it. */
  const height = ref(0)
  /** The dock moved to the bottom left while an overlay is open; toasts need no room then. */
  const aside = ref(false)

  const listeners = new Map<string, Array<(job: Job) => void>>()
  const timers = new Map<string, ReturnType<typeof setTimeout>>()

  function persist() {
    try {
      const ids = entries.value.filter((entry) => !(entry.done && entry.job?.state === 'succeeded')).map((e) => e.id)
      window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(ids))
    } catch {
      // Storage unavailable: the dock is not restored after a reload.
    }
  }

  /** Shows a card for a job that was just started and follows it. */
  function track(job: Job, options: TrackOptions = {}) {
    if (options.onFinished) listeners.set(job.id, [...(listeners.get(job.id) ?? []), options.onFinished])
    if (!entries.value.some((entry) => entry.id === job.id)) {
      const next = [{ id: job.id, job, done: false }, ...entries.value]
      while (next.length > MAX_ENTRIES) {
        // The oldest finished card makes room first; else the oldest running
        // one, whose job is then followed quietly (pages wait on its end).
        let index = next.length - 1
        while (index > 0 && !next[index]?.done) index--
        if (index === 0) index = next.length - 1
        const [gone] = next.splice(index, 1)
        if (gone) evict(gone)
      }
      entries.value = next
    }
    collapsed.value = false
    persist()
  }

  /** Hidden jobs still followed for their callbacks, by job id. */
  const hidden = new Map<string, ReturnType<typeof setTimeout>>()

  function notify(id: string, job: Job) {
    const callbacks = listeners.get(id) ?? []
    listeners.delete(id)
    for (const callback of callbacks) {
      try {
        callback(job)
      } catch (err) {
        console.warn('cairn: job callback failed', err)
      }
    }
  }

  /**
   * A card hidden while its job runs: pages still wait for the job to end
   * (to reload a list, to show an outcome), so it is checked quietly until then.
   */
  function followHidden(id: string) {
    hidden.set(
      id,
      setTimeout(async () => {
        try {
          const job = await fetchJob(id)
          if (!hidden.has(id)) return
          if (isFinalState(job.state)) {
            hidden.delete(id)
            notify(id, job)
            return
          }
        } catch (err) {
          if (isApiError(err) && (err.status === 404 || err.status === 403)) {
            hidden.delete(id)
            listeners.delete(id)
            return
          }
        }
        if (hidden.has(id)) followHidden(id)
      }, HIDDEN_POLL_MS),
    )
  }

  /** A card pushed out by newer ones: a running job's callbacks still fire. */
  function evict(entry: DockEntry) {
    clearTimeout(timers.get(entry.id))
    timers.delete(entry.id)
    if (!entry.done && (listeners.get(entry.id)?.length ?? 0) > 0) followHidden(entry.id)
    else listeners.delete(entry.id)
  }

  /** The card's latest view of its job. */
  function update(id: string, job: Job) {
    const entry = entries.value.find((e) => e.id === id)
    if (!entry || entry.done) return
    entry.job = job
    if (!isFinalState(job.state)) return
    entry.done = true
    notify(id, job)
    const { t } = i18n.global
    announcement.value = t('common.dock.announce', {
      job: jobKindLabel(job.kind, job.params),
      state: jobStateLabel(job.state),
    })
    if (job.state === 'succeeded') timers.set(id, setTimeout(() => dismiss(id), SUCCESS_LINGER_MS))
    persist()
  }

  /** The job could not be followed (gone, or not visible to this user): drop its card. */
  function lost(id: string) {
    drop(id)
  }

  function dismiss(id: string) {
    clearTimeout(timers.get(id))
    timers.delete(id)
    const entry = entries.value.find((e) => e.id === id)
    if (entry && !entry.done && (listeners.get(id)?.length ?? 0) > 0) followHidden(id)
    else listeners.delete(id)
    entries.value = entries.value.filter((e) => e.id !== id)
    persist()
  }

  /** Drops a card and everything waiting for its job. */
  function drop(id: string) {
    clearTimeout(hidden.get(id))
    hidden.delete(id)
    listeners.delete(id)
    dismiss(id)
  }

  function clear() {
    for (const timer of timers.values()) clearTimeout(timer)
    timers.clear()
    for (const timer of hidden.values()) clearTimeout(timer)
    hidden.clear()
    listeners.clear()
    entries.value = []
    announcement.value = ''
    persist()
  }

  onAccountReset(clear)

  return { entries, collapsed, announcement, height, aside, track, update, lost, dismiss, clear }
})
