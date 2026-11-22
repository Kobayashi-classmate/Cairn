// SPDX-License-Identifier: AGPL-3.0-only
//
// Site actions shared by the list, its "more" menu and the settings drawer.
// Every change runs as a job that the job dock follows; the list reloads
// when it starts and when it ends.
import { ApiError } from '@/api/client'
import type { Job } from '@/api/jobs'
import { deleteSite, restartSite, startSite, stopSite, updateSite, type SiteRequest, type SiteView } from '@/api/sites'
import { deleteStream, startStream, stopStream, type StreamView } from '@/api/streams'
import { confirmAction } from '@/composables/useConfirmDialog'
import { i18n } from '@/i18n'
import { launchJob } from '@/jobs/dock'
import { useSessionStore } from '@/stores/session'
import { useSitesStore } from '@/stores/sites'

function reloadSites() {
  void useSitesStore().load()
}

function reloadStreams() {
  void useSitesStore().loadStreams()
}

/** Starts a site job and reloads the list now and when it ends. */
export async function runSiteJob(start: () => Promise<Job>, options: { rethrow?: boolean } = {}): Promise<Job | null> {
  const job = await launchJob(start, { onFinished: reloadSites, rethrow: options.rethrow })
  if (job) reloadSites()
  return job
}

export function toggleSite(site: SiteView): Promise<Job | null> {
  return runSiteJob(() => (site.enabled ? stopSite(site.code) : startSite(site.code)))
}

export function restartApp(site: SiteView): Promise<Job | null> {
  return runSiteJob(() => restartSite(site.code))
}

/**
 * Saves the whole site; errors go back to the caller, which shows them by
 * the fields. Accounts that may not manage sites get an app's environment
 * without its values: their copy of a site is never sent back.
 */
export function saveSite(site: SiteView, request: SiteRequest): Promise<Job | null> {
  if (!useSessionStore().can('sites.manage')) return Promise.reject(new ApiError(403, 'auth.forbidden'))
  return runSiteJob(() => updateSite(site.code, request), { rethrow: true })
}

/**
 * Asks before deleting a site: the dialog names the consequence and wants
 * the primary domain typed. Resolves true once the delete job started.
 */
export function askDeleteSite(site: SiteView): Promise<boolean> {
  const { t } = i18n.global
  const domain = site.domains[0] ?? site.code
  return confirmAction({
    title: t('sites.delete.title', { site: domain }),
    description: (deleteFiles) =>
      `${t('sites.delete.text', { site: domain })} ${
        deleteFiles
          ? t('sites.delete.filesOn', { dir: site.dir, owner: site.owner, runner: site.runner })
          : t('sites.delete.filesOff', { dir: site.dir })
      }`,
    confirmLabel: t('sites.delete.confirm'),
    destructive: true,
    typeToConfirm: domain,
    option: { label: t('sites.delete.files') },
    run: async (deleteFiles) => {
      // Step-up protected: the client asks for the password and retries.
      await runSiteJob(() => deleteSite(site.code, deleteFiles), { rethrow: true })
    },
  })
}

export async function runStreamJob(start: () => Promise<Job>, options: { rethrow?: boolean } = {}): Promise<Job | null> {
  const job = await launchJob(start, { onFinished: reloadStreams, rethrow: options.rethrow })
  if (job) reloadStreams()
  return job
}

export function toggleStream(stream: StreamView): Promise<Job | null> {
  return runStreamJob(() => (stream.enabled ? stopStream(stream.code) : startStream(stream.code)))
}

export function askDeleteStream(stream: StreamView): Promise<boolean> {
  const { t } = i18n.global
  return confirmAction({
    title: t('streams.delete.title', { code: stream.code }),
    description: t('streams.delete.consequence', { protocol: stream.protocol.toUpperCase(), port: stream.port }),
    confirmLabel: t('streams.delete.confirm'),
    destructive: true,
    run: async () => {
      await runStreamJob(() => deleteStream(stream.code), { rethrow: true })
    },
  })
}
