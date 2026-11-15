// SPDX-License-Identifier: AGPL-3.0-only
import { fetchWizardState } from '@/api/wizard'
import { useNavStore } from '@/stores/nav'
import { useSessionStore } from '@/stores/session'

/**
 * Whether the first-login wizard should open now: for users who may change
 * settings while the software module is on (its menu entry is listed), as
 * long as nobody finished or skipped it.
 */
export async function wizardDue(): Promise<boolean> {
  const session = useSessionStore()
  const nav = useNavStore()
  if (!session.can('settings.manage') || !session.can('software.manage')) return false
  if (!nav.entries.some((entry) => entry.route === 'software')) return false
  try {
    return (await fetchWizardState()) === 'pending'
  } catch {
    // Better to skip the wizard than to block signing in.
    return false
  }
}
