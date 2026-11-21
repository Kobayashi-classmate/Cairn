// SPDX-License-Identifier: AGPL-3.0-only
//
// The job dock (interface spec, section 7): long jobs started from a page
// get a card in the bottom right corner that follows them, also after
// leaving the page. Succeeded cards leave 5 seconds after the job ends;
// failed ones stay until dismissed. The ids of the cards are kept for this
// browser tab, so a reload keeps following them.
import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { Job } from '@/api/jobs'
import { i18n } from '@/i18n'
import { isFinalState, jobKindLabel, jobStateLabel } from '@/jobs/labels'

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
      entries.value = [{ id: job.id, job, done: false }, ...entries.value].slice(0, MAX_ENTRIES)
    }
    collapsed.value = false
    persist()
  }

  /** The card's latest view of its job. */
  function update(id: string, job: Job) {
    const entry = entries.value.find((e) => e.id === id)
    if (!entry || entry.done) return
    entry.job = job
    if (!isFinalState(job.state)) return
    entry.done = true
    const callbacks = listeners.get(id) ?? []
    listeners.delete(id)
    for (const callback of callbacks) {
      try {
        callback(job)
      } catch (err) {
        console.warn('cairn: job callback failed', err)
      }
    }
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
    dismiss(id)
  }

  function dismiss(id: string) {
    clearTimeout(timers.get(id))
    timers.delete(id)
    listeners.delete(id)
    entries.value = entries.value.filter((entry) => entry.id !== id)
    persist()
  }

  function clear() {
    for (const timer of timers.values()) clearTimeout(timer)
    timers.clear()
    listeners.clear()
    entries.value = []
    announcement.value = ''
    persist()
  }

  return { entries, collapsed, announcement, height, aside, track, update, lost, dismiss, clear }
})
