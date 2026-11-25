// SPDX-License-Identifier: AGPL-3.0-only
//
// Panel-wide settings: `settings.read` to view, `settings.manage` to change.
import { useSessionStore } from '@/stores/session'
import { api } from './client'

export interface PanelSettings {
  defaultLocale: string
  /** Whether sensitive actions ask for the password again; missing from older servers, which always ask. */
  stepUpRequired?: boolean
}

export async function fetchSettings(): Promise<PanelSettings> {
  const settings = await api.get<PanelSettings>('/settings')
  if (typeof settings.stepUpRequired === 'boolean') useSessionStore().setStepUpRequired(settings.stepUpRequired)
  return settings
}

export function updateDefaultLocale(locale: string): Promise<void> {
  return api.put<void>('/settings/default-locale', { locale })
}

/**
 * Turns re-authentication for sensitive actions on or off for the whole
 * panel (`settings.manage`; turning it off is itself a sensitive action).
 * API tokens stay refused on those actions either way. The session store
 * follows the answer, so whoever calls this needs nothing more.
 */
export async function setStepUp(enabled: boolean): Promise<{ enabled: boolean }> {
  const answer = await api.put<{ enabled: boolean }>('/settings/step-up', { enabled })
  useSessionStore().setStepUpRequired(answer.enabled)
  return answer
}

export type CloudChannel = 'stable' | 'beta'
export const CLOUD_CHANNELS: readonly CloudChannel[] = ['stable', 'beta']

/** Where the panel downloads signed public data (messages, release information). */
export interface CloudSettings {
  enabled: boolean
  /** https:// address of the official service or a self-hosted mirror. */
  baseUrl: string
  channel: CloudChannel
}

export function fetchCloudSettings(): Promise<CloudSettings> {
  return api.get<CloudSettings>('/settings/cloud')
}

/** Saves the cloud settings (`settings.manage`, step-up). */
export function saveCloudSettings(settings: CloudSettings): Promise<void> {
  return api.put<void>('/settings/cloud', settings)
}
