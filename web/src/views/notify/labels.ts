// SPDX-License-Identifier: AGPL-3.0-only
//
// Severity of notifications and official messages, as pill colours and text.
import { i18n } from '@/i18n'
import type { Tone } from '@/lib/tone'

export function levelTone(level: string): Tone {
  if (level === 'critical') return 'destructive'
  if (level === 'warning') return 'warning'
  return 'progress'
}

export function levelText(level: string): string {
  const { t, te } = i18n.global
  const key = `notify.level.${level}`
  return te(key) ? t(key) : level
}

/** "security." → its translated category; unknown prefixes are shown as they are. */
export function categoryText(prefix: string): string {
  const { t, te } = i18n.global
  const key = `notify.category.${prefix.replace(/\.$/, '')}.label`
  return te(key) ? t(key) : prefix
}

export function eventText(event: string): string {
  const { t, te } = i18n.global
  const key = `notify.event.${event}`
  return te(key) ? t(key) : event
}
