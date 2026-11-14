// SPDX-License-Identifier: AGPL-3.0-only
//
// Panel-wide settings: `settings.read` to view, `settings.manage` to change.
import { api } from './client'

export interface PanelSettings {
  defaultLocale: string
}

export function fetchSettings(): Promise<PanelSettings> {
  return api.get<PanelSettings>('/settings')
}

export function updateDefaultLocale(locale: string): Promise<void> {
  return api.put<void>('/settings/default-locale', { locale })
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
