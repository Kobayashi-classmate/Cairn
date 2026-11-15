// SPDX-License-Identifier: AGPL-3.0-only
//
// First-login wizard state: reading it needs any signed-in user, changing it
// needs `settings.manage`.
import { api } from './client'

export type WizardState = 'pending' | 'done' | 'skipped'

export async function fetchWizardState(): Promise<WizardState> {
  return (await api.get<{ state: WizardState }>('/setup/wizard')).state
}

export async function saveWizardState(state: WizardState): Promise<void> {
  await api.put<unknown>('/setup/wizard', { state })
}
