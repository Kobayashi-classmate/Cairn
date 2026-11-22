// SPDX-License-Identifier: AGPL-3.0-only
//
// Software actions shared by the table, the settings drawer and the
// databases page. Every change runs as a job that the job dock follows; the
// software is locked meanwhile and the catalog reloads when it ends.
import { ref } from 'vue'
import type { Job } from '@/api/jobs'
import {
  controlSoftwareService,
  refreshPackageIndexes,
  removeSoftware,
  type Offer,
  type ServiceAction,
  type SoftwareEntry,
} from '@/api/software'
import { confirmAction } from '@/composables/useConfirmDialog'
import { i18n } from '@/i18n'
import { launchJob } from '@/jobs/dock'
import { lineKey } from '@/software/display'
import { onAccountReset } from '@/stores/accountReset'
import { useSoftwareStore } from '@/stores/software'

/** What stopping a line's services takes offline, in one sentence. */
function stopConsequence(entry: SoftwareEntry, offer: Offer): string {
  const { t } = i18n.global
  if (entry.id === 'nginx') return t('software.service.stopText.nginx')
  if (entry.id === 'php') return t('software.service.stopText.php', { line: offer.line })
  if (entry.category === 'database' || entry.category === 'cache') {
    return t('software.service.stopText.database', { name: entry.name })
  }
  const units = (offer.units ?? []).map((unit) => unit.name).join(', ')
  return t('software.service.stopText.other', { units })
}

/** Starts, stops, restarts or reloads a line's services; stopping asks first. */
export async function controlLine(entry: SoftwareEntry, offer: Offer, action: ServiceAction): Promise<Job | null> {
  const store = useSoftwareStore()
  const key = lineKey(entry.id, offer.line)
  const start = () => controlSoftwareService(entry.id, offer.line, action)
  // A failed service shows its last journal lines in the drawer's service pane.
  const onFinished = (done: Job) => store.setServiceFailure(key, done.state === 'failed' && done.error ? done.error : null)
  if (action !== 'stop') return store.runJob(start, { onFinished })
  const { t } = i18n.global
  let job: Job | null = null
  await confirmAction({
    title: t('software.service.stopTitle', { name: entry.name, line: offer.line }),
    description: stopConsequence(entry, offer),
    confirmLabel: t('software.service.stop'),
    destructive: true,
    run: async () => {
      job = await store.runJob(start, { rethrow: true, onFinished })
    },
  })
  return job
}

/**
 * Asks before uninstalling a line (step-up protected): the dialog names the
 * consequence and wants the software's name typed. Resolves true once the
 * job started.
 */
export function askUninstall(entry: SoftwareEntry, offer: Offer): Promise<boolean> {
  const { t } = i18n.global
  const named = { name: entry.name, line: offer.line }
  const store = useSoftwareStore()
  return confirmAction({
    title: t('software.confirmRemove.title', named),
    // Runtimes are plain files; packages leave configuration and data behind.
    description: t(
      offer.method === 'standalone' ? 'software.confirmRemove.runtimeContent' : 'software.confirmRemove.content',
      named,
    ),
    confirmLabel: t('software.action.remove'),
    destructive: true,
    typeToConfirm: entry.id,
    run: async () => {
      // Step-up protected: the client asks for the password and retries.
      await store.runJob(() => removeSoftware(entry.id, offer.line), { rethrow: true })
    },
  })
}

/** Refreshes the package indexes; the catalog reloads when it ends. */
/** A refresh of the package indexes started from this tab runs: one at a time. */
export const refreshingIndexes = ref(false)
onAccountReset(() => {
  refreshingIndexes.value = false
})

export async function refreshIndexes(): Promise<Job | null> {
  if (refreshingIndexes.value) return null
  const store = useSoftwareStore()
  refreshingIndexes.value = true
  const job = await launchJob(refreshPackageIndexes, {
    onFinished: () => {
      refreshingIndexes.value = false
      void store.load()
    },
  })
  if (!job) refreshingIndexes.value = false
  return job
}
