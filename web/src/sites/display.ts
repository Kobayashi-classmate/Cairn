// SPDX-License-Identifier: AGPL-3.0-only
//
// How a site reads in lists, cards and the settings drawer: its type in
// words, the line under its domain, and the HTTPS and backup columns with
// their tones (interface spec, section 3.3).
import type { SiteBackupSummary, SiteCertSummary, SiteView } from '@/api/sites'
import { i18n } from '@/i18n'
import type { Tone } from '@/lib/tone'
import { siteDisplayState, type SiteDisplayState } from './labels'

/** Product names of app runtimes (not translated). */
const RUNTIME_NAMES: Record<string, string> = {
  nodejs: 'Node.js',
  python: 'Python',
  go: 'Go',
  java: 'Java',
  dotnet: '.NET',
}

export function runtimeName(runtime: string): string {
  return RUNTIME_NAMES[runtime] ?? runtime
}

/** "Node.js 22", or null for an app using system tools only. */
export function appRuntimeLabel(site: Pick<SiteView, 'app'>): string | null {
  const app = site.app
  if (!app?.runtime) return null
  return [runtimeName(app.runtime), app.line].filter(Boolean).join(' ')
}

/** The type column: "PHP 8.4", "Node.js 22 app", "Static", "Reverse proxy". */
export function siteTypeText(site: SiteView): string {
  const { t } = i18n.global
  switch (site.type) {
    case 'php':
      return site.php ? `PHP ${site.php}` : t('sites.type.php.name')
    case 'app': {
      const runtime = appRuntimeLabel(site)
      return runtime ? t('sites.list.appWith', { runtime }) : t('sites.list.app')
    }
    case 'static':
      return t('sites.list.static')
    case 'proxy':
      return t('sites.list.proxy')
    default:
      return site.type
  }
}

/** The line under the domain: other domains, the app's port, the upstream or the directory. */
export function siteDetail(site: SiteView): string {
  const { t } = i18n.global
  if (site.type === 'app' && site.app) {
    const runtime = appRuntimeLabel(site)
    const port = t('sites.list.port', { port: site.app.port })
    return runtime ? `${runtime} · ${port}` : port
  }
  if (site.type === 'proxy' && site.proxy) return `→ ${site.proxy.url}`
  if (site.domains.length > 1) {
    const more = site.domains.length - 2
    return more > 0 ? `${site.domains[1]} ${t('sites.list.moreDomains', { count: more })}` : (site.domains[1] ?? '')
  }
  return site.dir
}

export const SITE_STATE_TONE: Record<SiteDisplayState, Tone> = {
  active: 'success',
  stopped: 'neutral',
  applying: 'progress',
  failed: 'destructive',
}

export function siteStateText(site: SiteView): string {
  return i18n.global.t(`sites.state.${siteDisplayState(site)}`)
}

export interface StatusText {
  tone: Tone
  text: string
}

/** Whole days until a Unix time; negative once it has passed. */
export function daysUntil(unixSeconds: number, nowSeconds: number): number {
  return Math.floor((unixSeconds - nowSeconds) / 86400)
}

/** Expiry of a certificate: success, warning from 14 days left, destructive once expired. */
export function expiryStatus(notAfter: number, nowSeconds: number): StatusText {
  const { t } = i18n.global
  if (notAfter <= nowSeconds) return { tone: 'destructive', text: t('certs.expiry.expired') }
  const days = daysUntil(notAfter, nowSeconds)
  return {
    tone: days <= 14 ? 'warning' : 'success',
    text: days < 1 ? t('certs.expiry.today') : t('certs.expiry.daysLeft', { count: days }, days),
  }
}

/** The certificate summary as one status. */
export function certSummaryStatus(cert: SiteCertSummary, nowSeconds: number): StatusText {
  const { t } = i18n.global
  if (cert.state === 'issuing') return { tone: 'progress', text: t('certs.state.issuing') }
  if (cert.state === 'failed') return { tone: 'destructive', text: t('certs.state.failed') }
  if (cert.notAfter) return expiryStatus(cert.notAfter, nowSeconds)
  return { tone: 'success', text: t('certs.state.valid') }
}

/** The HTTPS column of a site. */
export function httpsStatus(site: SiteView, nowSeconds: number): StatusText {
  const { t } = i18n.global
  if (!site.tls) return { tone: 'warning', text: t('sites.https.off') }
  if (!site.cert) return { tone: 'neutral', text: t('sites.https.on') }
  return certSummaryStatus(site.cert, nowSeconds)
}

/** The backup column of a site, without its time (see backupTime). */
export function backupStatus(
  backup: SiteBackupSummary | undefined,
  backupsKnown: boolean,
): (StatusText & { at?: number }) | null {
  const { t } = i18n.global
  if (!backupsKnown) return null
  if (!backup) return { tone: 'warning', text: t('sites.backup.none') }
  if (backup.lastFailedAt) return { tone: 'destructive', text: t('sites.backup.failed'), at: backup.lastFailedAt }
  if (!backup.lastOkAt) return { tone: 'warning', text: t('sites.backup.never') }
  return { tone: 'neutral', text: '', at: backup.lastOkAt }
}

/** The address a visitor would open; null for wildcards. */
export function siteUrl(site: Pick<SiteView, 'tls'>, domain: string | undefined): string | null {
  if (!domain || domain.startsWith('*.')) return null
  return `${site.tls ? 'https' : 'http'}://${domain}/`
}
