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
