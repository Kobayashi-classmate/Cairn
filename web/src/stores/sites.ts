// SPDX-License-Identifier: AGPL-3.0-only
//
// The sites page's data: the site list (with what the system reports about
// nginx, PHP and systemd), the built-in rewrite rules and the four-layer
// proxies. Shared by the list, the settings drawer and the sheets, and
// reloaded by the job dock when a site job ends.
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { fetchRewrites, fetchSite, fetchSites, type RewriteRule, type SiteList, type SiteView } from '@/api/sites'
import { fetchStreams, type StreamView } from '@/api/streams'
import { onAccountReset } from './accountReset'

/** How often lists refresh while a change is being applied. */
const APPLYING_REFRESH_MS = 3000

export const useSitesStore = defineStore('sites', () => {
  const list = ref<SiteList | null>(null)
  const loadError = ref<unknown>(null)
  const loading = ref(false)
  const rewrites = ref<RewriteRule[]>([])

  const streams = ref<StreamView[] | null>(null)
  const streamsError = ref<unknown>(null)
  const streamsLoading = ref(false)

  /** Pages showing the lists; refreshing while applying stops when none is. */
  let watchers = 0
  let siteTimer: ReturnType<typeof setTimeout> | undefined
  let streamTimer: ReturnType<typeof setTimeout> | undefined

  const sites = computed<SiteView[]>(() => list.value?.sites ?? [])

  /** Number the loads, so an answer arriving after a newer load or a reset is dropped. */
  let seq = 0
  let streamSeq = 0

  async function load(): Promise<void> {
    clearTimeout(siteTimer)
    const mine = ++seq
    loading.value = true
    try {
      const [sitesList, rules] = await Promise.all([
        fetchSites(),
        rewrites.value.length > 0 ? Promise.resolve(rewrites.value) : fetchRewrites().catch(() => []),
      ])
      if (mine !== seq) return
      list.value = sitesList
      rewrites.value = rules
      loadError.value = null
    } catch (err) {
      if (mine === seq) loadError.value = err
    } finally {
      if (mine === seq) loading.value = false
    }
    if (mine !== seq) return
    if (watchers > 0 && list.value?.sites.some((site) => site.state === 'applying')) {
      siteTimer = setTimeout(() => void load(), APPLYING_REFRESH_MS)
    }
  }

  async function loadStreams(): Promise<void> {
    clearTimeout(streamTimer)
    const mine = ++streamSeq
    streamsLoading.value = true
    try {
      const loaded = await fetchStreams()
      if (mine !== streamSeq) return
      streams.value = loaded
      streamsError.value = null
    } catch (err) {
      if (mine === streamSeq) streamsError.value = err
    } finally {
      if (mine === streamSeq) streamsLoading.value = false
    }
    if (mine !== streamSeq) return
    if (watchers > 0 && streams.value?.some((stream) => stream.state === 'applying')) {
      streamTimer = setTimeout(() => void loadStreams(), APPLYING_REFRESH_MS)
    }
  }

  /** One site as listed, or from the server when the list does not have it (yet). */
  async function site(code: string): Promise<SiteView> {
    const listed = list.value?.sites.find((s) => s.code === code)
    if (listed) return listed
    return fetchSite(code)
  }

  /** A page starts showing the lists. */
  function watch(): void {
    watchers++
  }

  /** A page stops showing the lists. */
  function unwatch(): void {
    watchers = Math.max(0, watchers - 1)
    if (watchers === 0) {
      clearTimeout(siteTimer)
      clearTimeout(streamTimer)
    }
  }

  /** Forgets everything (sign-out): the next account loads its own. */
  onAccountReset(() => {
    seq++
    streamSeq++
    clearTimeout(siteTimer)
    clearTimeout(streamTimer)
    list.value = null
    loadError.value = null
    loading.value = false
    streams.value = null
    streamsError.value = null
    streamsLoading.value = false
  })

  return {
    list,
    sites,
    loadError,
    loading,
    rewrites,
    streams,
    streamsError,
    streamsLoading,
    load,
    loadStreams,
    site,
    watch,
    unwatch,
  }
})
