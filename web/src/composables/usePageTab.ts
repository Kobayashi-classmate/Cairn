// SPDX-License-Identifier: AGPL-3.0-only
import { computed, toValue, type MaybeRefOrGetter } from 'vue'
import { useRoute, type LocationQuery, type RouteLocationRaw } from 'vue-router'

/** Query parameter of a page's underline tabs. */
export const TAB_QUERY = 'tab'
/** Query parameter of an open settings drawer ("site:<code>", "cert:<id>"). */
export const OPEN_QUERY = 'open'
/** Query parameter of the open drawer's sub-item. */
export const SECTION_QUERY = 'section'
/** Query parameter of the line an open editor shows (files opened from a search). */
export const LINE_QUERY = 'line'

/** The query without the parameters of a settings drawer. */
export function withoutDrawer(query: LocationQuery): LocationQuery {
  const rest = { ...query }
  delete rest[OPEN_QUERY]
  delete rest[SECTION_QUERY]
  delete rest[LINE_QUERY]
  return rest
}

/**
 * The selected underline tab of a page, kept in the address (`?tab=`) so it
 * survives a reload and the Back button returns to the previous tab. A
 * settings drawer opened on top keeps it (`?tab=php&open=site:x&section=https`).
 */
export function usePageTab<V extends string>(tabs: MaybeRefOrGetter<readonly V[]>, fallback: V) {
  const route = useRoute()

  const tab = computed<V>(() => {
    const value = route.query[TAB_QUERY]
    return typeof value === 'string' && (toValue(tabs) as readonly string[]).includes(value) ? (value as V) : fallback
  })

  /** Where a tab link leads: the same page with only that tab, no drawer. */
  function tabLocation(value: V): RouteLocationRaw {
    const query = withoutDrawer(route.query)
    if (value === fallback) delete query[TAB_QUERY]
    else query[TAB_QUERY] = value
    return { query }
  }

  return { tab, tabLocation }
}
