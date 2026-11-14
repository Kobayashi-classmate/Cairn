// SPDX-License-Identifier: AGPL-3.0-only
//
// Panel modules: listing needs `settings.read`, switching needs
// `settings.manage` and is step-up protected.
import { api } from './client'

export interface ModuleState {
  id: string
  /** Core modules cannot be turned off. */
  optional: boolean
  enabled: boolean
}

export async function fetchModules(): Promise<ModuleState[]> {
  return (await api.get<{ modules: ModuleState[] }>('/modules')).modules
}

export async function setModuleEnabled(id: string, enabled: boolean): Promise<ModuleState> {
  return (await api.put<{ module: ModuleState }>(`/modules/${encodeURIComponent(id)}`, { enabled })).module
}
