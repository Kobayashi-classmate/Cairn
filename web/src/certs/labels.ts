// SPDX-License-Identifier: AGPL-3.0-only
import type { CertView } from '@/api/certs'
import { i18n } from '@/i18n'

/** Known ACME CAs: display name key and the page with the subscriber agreement. */
const CA_AGREEMENTS: Record<string, string> = {
  letsencrypt: 'https://letsencrypt.org/repository/',
  'letsencrypt-staging': 'https://letsencrypt.org/docs/staging-environment/',
  zerossl: 'https://zerossl.com/terms/',
  google: 'https://pki.goog/repository/',
}

export function caLabel(name: string): string {
  const key = `certs.ca.${name}`
  return i18n.global.te(key) ? i18n.global.t(key) : name
}

/** Where the CA publishes the agreement users accept by issuing; none for a custom CA. */
export function caAgreementUrl(name: string): string | null {
  return CA_AGREEMENTS[name] ?? null
}

export type CertDisplayState = 'issuing' | 'valid' | 'failed' | 'expired' | 'renewalFailed'

/** What to show for a certificate: expiry and failed renewals override "valid". */
export function certDisplayState(cert: CertView, nowSeconds: number): CertDisplayState {
  if (cert.state === 'issuing') return 'issuing'
  if (cert.state === 'failed') return 'failed'
  if (cert.notAfter && cert.notAfter <= nowSeconds) return 'expired'
  if (cert.errorCode) return 'renewalFailed'
  return 'valid'
}

export function certStateTagType(state: CertDisplayState): 'success' | 'info' | 'error' | 'warning' {
  if (state === 'valid') return 'success'
  if (state === 'issuing') return 'info'
  if (state === 'renewalFailed') return 'warning'
  return 'error'
}

/** Days until expiry (negative when expired), or null when unknown. */
export function daysLeft(cert: CertView, nowSeconds: number): number | null {
  return cert.notAfter ? Math.floor((cert.notAfter - nowSeconds) / 86400) : null
}

/** Colour for an expiry date: red under a week, orange under 30 days. */
export function expiryType(days: number | null): 'error' | 'warning' | 'default' {
  if (days === null) return 'default'
  if (days < 7) return 'error'
  if (days < 30) return 'warning'
  return 'default'
}

/** Whether a certificate name covers a host; a wildcard covers exactly one label. */
export function nameCovers(pattern: string, host: string): boolean {
  if (pattern === host) return true
  if (!pattern.startsWith('*.') || host.startsWith('*.')) return false
  const dot = host.indexOf('.')
  return dot > 0 && host.slice(dot + 1) === pattern.slice(2)
}

/** Whether the certificate's names cover every one of the hosts. */
export function certCovers(cert: Pick<CertView, 'domains'>, hosts: readonly string[]): boolean {
  return hosts.every((host) => cert.domains.some((pattern) => nameCovers(pattern, host)))
}
