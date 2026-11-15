// SPDX-License-Identifier: AGPL-3.0-only
import { onMounted, ref } from 'vue'
import { APP_RUNTIMES, type AppRuntime } from '@/api/sites'
import { fetchSoftware } from '@/api/software'
import { useNavStore } from '@/stores/nav'
import { useSessionStore } from '@/stores/session'

/** An installed runtime line an app can run on. */
export interface InstalledRuntime {
  software: AppRuntime
  line: string
  /** Product name, for example "Node.js". */
  name: string
}

/**
 * Installed runtime lines, from the software catalog. Empty when the user
 * cannot read the catalog or the software module is off; apps can then still
 * use system tools.
 */
export function useInstalledRuntimes() {
  const runtimes = ref<InstalledRuntime[]>([])
  const loaded = ref(false)

  async function load() {
    const session = useSessionStore()
    const nav = useNavStore()
    try {
      if (!session.can('software.read') || !nav.entries.some((entry) => entry.route === 'software')) return
      const catalog = await fetchSoftware()
      runtimes.value = catalog.software.flatMap((entry) =>
        (APP_RUNTIMES as readonly string[]).includes(entry.id)
          ? entry.offers
              .filter((offer) => offer.installed)
              .map((offer) => ({ software: entry.id as AppRuntime, line: offer.line, name: entry.name }))
          : [],
      )
    } catch {
      runtimes.value = []
    } finally {
      loaded.value = true
    }
  }

  onMounted(load)
  return { runtimes, loaded }
}
