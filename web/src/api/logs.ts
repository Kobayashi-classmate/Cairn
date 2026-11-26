// SPDX-License-Identifier: AGPL-3.0-only
//
// The system journal, read through journalctl on the server: the whole of
// it with `logs.system`, else (`logs.read`) the units `fetchUnits` lists,
// all of them when no unit is asked for.
import { api } from './client'

export interface JournalEntry {
  /** Microseconds since the epoch. */
  time: number
  /** 0 (emergency) to 7 (debug). */
  priority: number
  unit?: string
  identifier?: string
  pid?: number
  message: string
}

export interface Journal {
  entries: JournalEntry[]
  /** Continues after the last entry (pass it as `after`). */
  cursor?: string
}

export interface JournalQuery {
  unit?: string
  /** 1–7: this level and more severe; absent: all. */
  priority?: number
  /** journalctl forms: "today", "-1h", "2026-11-16 10:00". */
  since?: string
  until?: string
  grep?: string
  /** This boot only. */
  boot?: boolean
  limit?: number
  after?: string
}

/** Most entries one request returns. */
export const JOURNAL_MAX = 1000
/** Times journalctl accepts here. */
export const JOURNAL_TIME = /^[0-9A-Za-z :+-]{1,40}$/

export function fetchJournal(query: JournalQuery, signal?: AbortSignal): Promise<Journal> {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === '' || value === false) continue
    params.set(key, String(value))
  }
  return api.get<Journal>(`/logs/journal?${params.toString()}`, { signal })
}

export async function fetchUnits(signal?: AbortSignal): Promise<string[]> {
  return (await api.get<{ units: string[] }>('/logs/units', { signal })).units
}
