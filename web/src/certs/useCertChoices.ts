// SPDX-License-Identifier: AGPL-3.0-only
import { onMounted, ref } from 'vue'
import { fetchCerts, type CertView } from '@/api/certs'
import { useNavStore } from '@/stores/nav'
import { useSessionStore } from '@/stores/session'

/** Whether certificates can be listed: the module is on and the user may read them. */
export function certsAvailable(): boolean {
  const session = useSessionStore()
  return session.can('certs.read') && useNavStore().entries.some((entry) => entry.route === 'certs')
}

/**
 * The certificates a site or the panel can serve: valid and not expired.
 * Null while unknown or when certificates are not available to the user.
 */
export function useCertChoices() {
  const certs = ref<CertView[] | null>(null)

  async function load() {
    if (!certsAvailable()) return
    try {
      const now = Date.now() / 1000
      certs.value = (await fetchCerts()).certs.filter(
        (cert) => cert.state === 'valid' && (!cert.notAfter || cert.notAfter > now),
      )
    } catch {
      // Without the list the HTTPS choice is simply not offered.
      certs.value = null
    }
  }

  onMounted(load)
  return { certs, reload: load }
}
