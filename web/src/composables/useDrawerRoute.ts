// SPDX-License-Identifier: AGPL-3.0-only
import { computed } from 'vue'
import { useRoute, useRouter, type LocationQuery } from 'vue-router'
import { OPEN_QUERY, SECTION_QUERY, withoutDrawer } from './usePageTab'

/** The page address before the last drawer was opened from it, to return to on close. */
let openedFrom: { path: string; query: LocationQuery } | null = null

/**
 * A settings drawer whose state lives in the address (interface spec,
 * section 6): `?open=<kind>:<id>&section=<sub-item>`. Opening pushes a history
 * entry, so Back closes the drawer; switching sub-items replaces it, so Back
 * does not walk through them. Links, bookmarks and reloads open the same
 * drawer on the same sub-item.
 */
export function useDrawerRoute(kind: string) {
  const route = useRoute()
  const router = useRouter()
  const prefix = `${kind}:`

  /** Id of the object whose drawer is open, or null. */
  const id = computed<string | null>(() => {
    const value = route.query[OPEN_QUERY]
    return typeof value === 'string' && value.startsWith(prefix) && value.length > prefix.length
      ? value.slice(prefix.length)
      : null
  })

  /** The sub-item asked for; "" lets the drawer pick its first one. */
  const section = computed(() => {
    const value = route.query[SECTION_QUERY]
    return id.value !== null && typeof value === 'string' ? value : ''
  })

  /**
   * Opens the drawer of an object, on a sub-item or the first one. `extra`
   * adds parameters that belong to the drawer (see withoutDrawer).
   */
  function open(target: string, sub?: string, extra: Record<string, string> = {}) {
    const base = withoutDrawer(route.query)
    if (id.value === null) openedFrom = { path: route.path, query: base }
    // The page's own tab (?tab=) stays as it is under the drawer.
    const query: LocationQuery = { ...base, ...extra, [OPEN_QUERY]: `${prefix}${target}` }
    if (sub) query[SECTION_QUERY] = sub
    void (id.value === null ? router.push({ query }) : router.replace({ query }))
  }

  /** Shows another sub-item of the open drawer. */
  function setSection(sub: string) {
    if (id.value === null || sub === section.value) return
    void router.replace({ query: { ...route.query, [SECTION_QUERY]: sub } })
  }

  /** Closes the drawer: back to the page it was opened from, or the page without it. */
  function close() {
    if (id.value === null) return
    const back = (window.history.state as { back?: unknown } | null)?.back
    const from = openedFrom
    openedFrom = null
    if (from && from.path === route.path && typeof back === 'string' && router.resolve(back).path === from.path) {
      router.back()
      return
    }
    void router.replace({ query: from && from.path === route.path ? from.query : withoutDrawer(route.query) })
  }

  return { id, section, open, setSection, close }
}
