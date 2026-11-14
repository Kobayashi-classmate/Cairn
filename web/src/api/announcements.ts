// SPDX-License-Identifier: AGPL-3.0-only
//
// Official messages published by the Cairn project. The panel downloads and
// verifies them; they are informational and never trigger any action.
import { api } from './client'
import type { Level } from './notify'

export type AnnouncementCategory = 'release' | 'security' | 'maintenance' | 'general'

/** Text per locale, for example {"en": "...", "zh-CN": "..."}. */
export type LocalizedText = Record<string, string>

export interface Announcement {
  id: string
  level: Level
  category: AnnouncementCategory
  /** Unix seconds. */
  publishedAt: number
  /** Unix seconds; 0 when the message does not expire. */
  expiresAt: number
  title: LocalizedText
  /** Limited Markdown, see src/markdown/parse.ts. */
  body: LocalizedText
  /** https:// URL with more information, or "". */
  link: string
  read: boolean
}

export interface AnnouncementList {
  /** Newest first. */
  announcements: Announcement[]
  unread: number
}

export function fetchAnnouncements(): Promise<AnnouncementList> {
  return api.get<AnnouncementList>('/announcements')
}

export function markAnnouncementRead(id: string): Promise<void> {
  return api.post<void>(`/announcements/${encodeURIComponent(id)}/read`)
}

export function markAllAnnouncementsRead(): Promise<void> {
  return api.post<void>('/announcements/read-all')
}
