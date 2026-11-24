// SPDX-License-Identifier: AGPL-3.0-only
//
// Websites on the panel's own host name. Cookies belong to a host name, not
// to a port: a browser signed in to https://example.com:28443 sends the
// panel's cookies to the website at https://example.com as well, and that
// website can set cookies the panel then receives. The panel takes its
// cookies only together with a key the website never sees, and the web
// server keeps them from the site's code, but sharing a host name still
// lets the site sign the administrator out, and nothing protects a service
// that is not behind this server's nginx. The forms warn about it.
import type { CertView } from '@/api/certs'

/** Whether a (possibly wildcard) domain is or covers a host name. */
function covers(domain: string, host: string): boolean {
  if (domain === host) return true
  if (!domain.startsWith('*.') || host.startsWith('*.')) return false
  const dot = host.indexOf('.')
  return dot > 0 && host.slice(dot + 1) === domain.slice(2)
}

/** The names a certificate is for that are host names: a wildcard does not tell which host is used. */
export function certHosts(cert: Pick<CertView, 'domains'> | null | undefined): string[] {
  return (cert?.domains ?? []).map((name) => name.toLowerCase()).filter((name) => !name.startsWith('*.'))
}

/**
 * The host names the panel is opened with: the one of this page, and the
 * names of the certificate the panel serves (when the certificates are
 * known).
 */
export function panelHosts(certs: readonly CertView[] | null | undefined): string[] {
  const hosts = new Set<string>()
  const page = window.location.hostname.toLowerCase()
  if (page) hosts.add(page)
  for (const name of certHosts(certs?.find((cert) => cert.panel))) hosts.add(name)
  return [...hosts]
}

/** The domains (of a site) that are, or as wildcards cover, one of the host names. */
export function domainsOnHosts(domains: readonly string[], hosts: readonly string[]): string[] {
  return domains.filter((domain) => hosts.some((host) => covers(domain.toLowerCase(), host)))
}
