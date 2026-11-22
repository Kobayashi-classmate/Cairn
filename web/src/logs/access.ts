// SPDX-License-Identifier: AGPL-3.0-only
//
// Site access logs: nginx writes one JSON object per request. Lines that
// are not such an object are shown as they are.
import type { Tone } from '@/lib/tone'

export interface AccessEntry {
  raw: string
  time?: string
  remote?: string
  host?: string
  method?: string
  uri?: string
  status?: number
  bytes?: number
  /** Seconds. */
  duration?: number
}

export function parseAccess(raw: string): AccessEntry {
  try {
    const value: unknown = JSON.parse(raw)
    if (typeof value !== 'object' || value === null) return { raw }
    const v = value as Record<string, unknown>
    const text = (key: string) => (typeof v[key] === 'string' ? (v[key] as string) : undefined)
    const number = (key: string) => {
      if (v[key] === undefined || v[key] === null || v[key] === '') return undefined
      const n = typeof v[key] === 'number' ? (v[key] as number) : Number(v[key])
      return Number.isFinite(n) ? n : undefined
    }
    return {
      raw,
      time: text('time'),
      remote: text('remote'),
      host: text('host'),
      method: text('method'),
      uri: text('uri'),
      status: number('status'),
      bytes: number('bytes'),
      duration: number('duration'),
    }
  } catch {
    return { raw }
  }
}

/** Whether a line was understood as a request. */
export function isRequest(entry: AccessEntry): boolean {
  return entry.status !== undefined || entry.uri !== undefined
}

/** 2xx fine, 3xx quiet, 4xx a warning, 5xx bad (interface spec, section 9 "LogView"). */
export function statusTone(status: number | undefined): Tone {
  if (status === undefined) return 'neutral'
  if (status >= 500) return 'destructive'
  if (status >= 400) return 'warning'
  if (status >= 300) return 'neutral'
  return 'success'
}
