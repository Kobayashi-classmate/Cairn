// SPDX-License-Identifier: AGPL-3.0-only
//
// Client-side mirrors of the server's site field rules, for faster feedback.
// The server checks everything again.

export const MAX_DOMAINS = 32
export const MAX_INDEX = 8
export const MAX_WRITABLE = 16

const SITE_CODE = /^[a-z][a-z0-9-]{0,18}[a-z0-9]$/
const HOST_LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/
const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/
const PATH_PART = /^[A-Za-z0-9._-]{1,128}$/
const FILE_NAME = /^[A-Za-z0-9._-]{1,64}$/
const PROXY_PATH = /^\/[A-Za-z0-9._~/%-]{0,255}$/

export function isSiteCode(code: string): boolean {
  return SITE_CODE.test(code) && !code.includes('--')
}

/**
 * Lower-cases a host name and converts an international name to punycode
 * ("bücher.example" becomes "xn--bcher-kva.example"). Input with a port,
 * path or other extras is returned unchanged so validation reports it.
 */
export function normalizeHost(input: string): string {
  const host = input.trim().toLowerCase()
  if (host === '' || /[:/?#@\s]/.test(host)) return host
  const wildcard = host.startsWith('*.')
  const name = wildcard ? host.slice(2) : host
  try {
    const ascii = new URL(`http://${name}`).hostname
    return wildcard ? `*.${ascii}` : ascii
  } catch {
    return host
  }
}

/** A host a site may serve: a name (optionally "*." wildcard) or an IPv4 address, no port. */
export function isSiteHost(host: string): boolean {
  if (host.length === 0 || host.length > 253) return false
  if (IPV4.test(host)) return true
  const name = host.startsWith('*.') ? host.slice(2) : host
  const labels = name.split('.')
  if (labels.length < 2 && !(name === 'localhost' && name === host)) return false
  if (!labels.every((label) => HOST_LABEL.test(label))) return false
  // The last label of a name is never all digits.
  return !/^\d+$/.test(labels[labels.length - 1] ?? '')
}

/** A path inside the site: relative, no "..", no leading or trailing "/" ("" is the app directory). */
export function isRelativePath(path: string): boolean {
  if (path === '') return true
  if (path.length > 512 || path.startsWith('/') || path.endsWith('/')) return false
  return path.split('/').every((part) => part !== '.' && part !== '..' && PATH_PART.test(part))
}

export function isIndexName(name: string): boolean {
  return FILE_NAME.test(name) && name !== '.' && name !== '..'
}

/** http(s)://host[:port][/path] with no credentials, query or fragment. */
export function isProxyUrl(value: string): boolean {
  let url: URL
  try {
    url = new URL(value)
  } catch {
    return false
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') return false
  if (url.username || url.password || url.search || url.hash || value.includes('?') || value.includes('#')) {
    return false
  }
  const host = url.hostname
  if (!IPV4.test(host) && host !== 'localhost' && !host.startsWith('[') && !isSiteHost(host)) return false
  return url.pathname === '' || PROXY_PATH.test(url.pathname)
}

/** The code the server derives from the first domain ("www.example.com" gives "example-com"). */
export function deriveCode(domain: string): string {
  const d = domain.toLowerCase().trim().replace(/^\*\./, '').replace(/^www\./, '')
  let code = d.replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  if (code === '' || code[0]! < 'a' || code[0]! > 'z') code = `site-${code}`
  if (code.length > 20) code = code.slice(0, 20)
  return code.replace(/-+$/, '')
}
