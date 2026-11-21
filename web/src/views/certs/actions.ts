// SPDX-License-Identifier: AGPL-3.0-only
//
// Certificate actions shared by the list's "more" menu and the drawer. Every
// change of a certificate is a job that the job dock follows; the list
// reloads when it starts and when it ends.
import { deleteCert, renewCert, setPanelCert, type CertView } from '@/api/certs'
import type { Job } from '@/api/jobs'
import type { CertDisplayState } from '@/certs/labels'
import { confirmAction } from '@/composables/useConfirmDialog'
import { i18n } from '@/i18n'
import { launchJob } from '@/jobs/dock'
import type { Tone } from '@/lib/tone'
import { useCertsStore } from '@/stores/certs'

export const CERT_STATE_TONE: Record<CertDisplayState, Tone> = {
  valid: 'success',
  issuing: 'progress',
  failed: 'destructive',
  expired: 'destructive',
  renewalFailed: 'warning',
}

function reload() {
  void useCertsStore().load()
}

export async function runCertJob(start: () => Promise<Job>, options: { rethrow?: boolean } = {}): Promise<Job | null> {
  const job = await launchJob(start, { onFinished: reload, rethrow: options.rethrow })
  if (job) reload()
  return job
}

export function renew(cert: CertView): Promise<Job | null> {
  return runCertJob(() => renewCert(cert.id))
}

/** Valid and not expired: the panel can serve it. */
export function canServePanel(cert: CertView, nowSeconds: number): boolean {
  return cert.state === 'valid' && (!cert.notAfter || cert.notAfter > nowSeconds)
}

/** Serves the certificate for the panel (or, with null, the self-signed one again). Step-up protected. */
export function askPanelCert(cert: CertView | null, current?: CertView): Promise<boolean> {
  const { t } = i18n.global
  const domain = cert?.domains.find((d) => !d.startsWith('*.')) ?? cert?.domains[0] ?? ''
  return confirmAction({
    title: cert ? t('certs.panelUse.title', { name: cert.name }) : t('certs.panelUse.selfSignedTitle'),
    description: cert
      ? `${t('certs.panelUse.text')} ${domain ? t('certs.panel.openBy', { domain }) : ''}`
      : t('certs.panelUse.selfSignedText', { name: current?.name ?? '' }),
    confirmLabel: cert ? t('certs.panel.apply') : t('certs.panelUse.selfSignedConfirm'),
    run: async () => {
      await runCertJob(() => setPanelCert(cert?.id ?? ''), { rethrow: true })
    },
  })
}

/** Asks before deleting; refused by the server while sites or the panel use it. Step-up protected. */
export function askDeleteCert(cert: CertView): Promise<boolean> {
  const { t } = i18n.global
  return confirmAction({
    title: t('certs.delete.title', { name: cert.name }),
    description: t('certs.delete.consequence', { domains: cert.domains.join(', ') }),
    confirmLabel: t('certs.delete.confirm'),
    destructive: true,
    run: async () => {
      await runCertJob(() => deleteCert(cert.id), { rethrow: true })
    },
  })
}
