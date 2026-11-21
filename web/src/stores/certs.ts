// SPDX-License-Identifier: AGPL-3.0-only
//
// The certificates page's data: certificates and DNS accounts, shared by the
// tabs, the drawer and the sheets, and reloaded by the job dock.
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { fetchCerts, type CertList } from '@/api/certs'
import { fetchDnsAccounts, fetchDnsProviders, type DnsAccount, type DnsProvider } from '@/api/dns'

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

  async function load(): Promise<void> {
    clearTimeout(timer)
    loading.value = true
    try {
      list.value = await fetchCerts()
      loadError.value = null
    } catch (err) {
      loadError.value = err
    } finally {
      loading.value = false
    }
    if (watchers > 0 && list.value?.certs.some((cert) => cert.state === 'issuing')) {
      timer = setTimeout(() => void load(), ISSUING_REFRESH_MS)
    }
  }

  async function loadAccounts(): Promise<void> {
    accountsLoading.value = true
    try {
      const [list, provs] = await Promise.all([
        fetchDnsAccounts(),
        providers.value.length > 0 ? Promise.resolve(providers.value) : fetchDnsProviders(),
      ])
      accounts.value = list
      providers.value = provs
      accountsError.value = null
    } catch (err) {
      accountsError.value = err
    } finally {
      accountsLoading.value = false
    }
  }

  function watch(): void {
    watchers++
  }

  function unwatch(): void {
    watchers = Math.max(0, watchers - 1)
    if (watchers === 0) clearTimeout(timer)
  }

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
