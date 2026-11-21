// SPDX-License-Identifier: AGPL-3.0-only
import { computed, type ComputedRef } from 'vue'
import { useNavStore } from '@/stores/nav'

/**
 * Names of the routes in the user's menu: the modules that are on and that
 * the user may open. Links to other pages are left out.
 */
export function useNavRoutes(): ComputedRef<ReadonlySet<string>> {
  const nav = useNavStore()
  return computed(() => new Set(nav.entries.map((entry) => entry.route)))
}
