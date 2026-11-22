// SPDX-License-Identifier: AGPL-3.0-only
//
// The certificates page's data: certificates and DNS accounts, shared by the
// tabs, the drawer and the sheets, and reloaded by the job dock.
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { fetchCerts, type CertList } from '@/api/certs'
import { fetchDnsAccounts, fetchDnsProviders, type DnsAccount, type DnsProvider } from '@/api/dns'
import { onAccountReset } from './accountReset'

/** How often the list refreshes while a certificate is being issued. */
const ISSUING_REFRESH_MS = 3000

export const useCertsStore = defineStore('certs', () => {
  const list = ref<CertList | null>(null)
  const loadError = ref<unknown>(null)
  const loading = ref(false)

  const accounts = ref<DnsAccount[] | null>(null)
  const providers = ref<DnsProvider[]>([])
  const accountsError = ref<unknown>(null)
  const accountsLoading = ref(false)

  let watchers = 0
  let timer: ReturnType<typeof setTimeout> | undefined

  /** Numbers the loads, so an answer arriving after a newer load or a reset is dropped. */
  let seq = 0

  async function load(): Promise<void> {
    clearTimeout(timer)
    const mine = ++seq
    loading.value = true
    try {
      const loaded = await fetchCerts()
      if (mine !== seq) return
      list.value = loaded
      loadError.value = null
    } catch (err) {
      if (mine === seq) loadError.value = err
    } finally {
      if (mine === seq) loading.value = false
    }
    if (mine !== seq) return
    if (watchers > 0 && list.value?.certs.some((cert) => cert.state === 'issuing')) {
      timer = setTimeout(() => void load(), ISSUING_REFRESH_MS)
    }
  }

  let accountsSeq = 0

  async function loadAccounts(): Promise<void> {
    const mine = ++accountsSeq
    accountsLoading.value = true
    try {
      const [list, provs] = await Promise.all([
        fetchDnsAccounts(),
        providers.value.length > 0 ? Promise.resolve(providers.value) : fetchDnsProviders(),
      ])
      if (mine !== accountsSeq) return
      accounts.value = list
      providers.value = provs
      accountsError.value = null
    } catch (err) {
      if (mine === accountsSeq) accountsError.value = err
    } finally {
      if (mine === accountsSeq) accountsLoading.value = false
    }
  }

  function watch(): void {
    watchers++
  }

  function unwatch(): void {
    watchers = Math.max(0, watchers - 1)
    if (watchers === 0) clearTimeout(timer)
  }

  /** Forgets everything (sign-out): the next account loads its own. */
  function reset(): void {
    seq++
    accountsSeq++
    clearTimeout(timer)
    list.value = null
    loadError.value = null
    loading.value = false
    accounts.value = null
    accountsError.value = null
    accountsLoading.value = false
  }
  onAccountReset(reset)

  return {
    list,
    loadError,
    loading,
    accounts,
    providers,
    accountsError,
    accountsLoading,
    load,
    loadAccounts,
    watch,
    unwatch,
  }
})
