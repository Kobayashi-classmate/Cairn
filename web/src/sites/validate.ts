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

/** A code a site (or a layer-4 proxy) can have. */
export function isSiteCode(code: string): boolean {
  return SITE_CODE.test(code) && !code.includes('--')
}

/**
 * Whether a new site may take a code. A site's users are cw-<code> and
 * cw-<code>-run, so a new code ending in "-run" is refused: site "x-run"
 * would get the user of site "x"'s runner. (Sites that have such a code
 * already keep it.)
 */
export function isNewSiteCodeEnding(code: string): boolean {
  return !code.endsWith('-run')
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

/**
 * The code the server derives from the first domain ("www.example.com"
 * gives "example-com"). An ending "-run" becomes "-rn" ("site.run" gives
 * "site-rn"), since no code may end in "-run".
 */
export function deriveCode(domain: string): string {
  const d = domain.toLowerCase().trim().replace(/^\*\./, '').replace(/^www\./, '')
  let code = d.replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  if (code === '' || code[0]! < 'a' || code[0]! > 'z') code = `site-${code}`
  if (code.length > 20) code = code.slice(0, 20)
  code = code.replace(/-+$/, '')
  return code.endsWith('-run') ? `${code.slice(0, -'-run'.length)}-rn` : code
}

export const APP_PORT_MIN = 20000
export const APP_PORT_MAX = 29999
export const MAX_ENV = 64
export const MAX_UPSTREAMS = 16

const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/
const HEALTH_PATH = /^\/[A-Za-z0-9._~/%?&=+-]{0,255}$/
// Control characters (newlines included) are not allowed in commands and values.
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f]/

export function isEnvName(name: string): boolean {
  return ENV_NAME.test(name)
}

export function isEnvValue(value: string): boolean {
  return value.length <= 4096 && !CONTROL.test(value)
}

/** One shell line without control characters. */
export function isAppCommand(command: string): boolean {
  return command.trim() !== '' && command.length <= 4096 && !CONTROL.test(command)
}

export function isHealthPath(path: string): boolean {
  return HEALTH_PATH.test(path)
}

/** A port a four-layer proxy may listen on. */
export function isStreamPort(port: number): boolean {
  return (
    Number.isInteger(port) &&
    port >= 1 &&
    port <= 65535 &&
    port !== 80 &&
    port !== 443 &&
    (port < APP_PORT_MIN || port > APP_PORT_MAX)
  )
}

/** The host of a host:port or [v6]:port upstream (without the brackets). */
function upstreamHost(value: string): string {
  const v6 = /^\[([^\]]*)\]:\d+$/.exec(value)
  if (v6) return v6[1] ?? ''
  const index = value.lastIndexOf(':')
  return index < 0 ? value : value.slice(0, index)
}

/** An IPv6 address as the server writes it (lower case, zeros compressed), or null. */
function canonicalIPv6(host: string): string | null {
  try {
    return new URL(`http://[${host}]`).hostname.slice(1, -1)
  } catch {
    return null
  }
}

/**
 * Whether an upstream names its host by IP address, written the usual way
 * (the server compares with its own form, so an address it writes
 * differently is refused there). Without the firewall permission, a stream
 * may only forward to such addresses: a name could point to this server.
 */
export function isAddressUpstream(value: string): boolean {
  const host = upstreamHost(value)
  return IPV4.test(host) || (host.includes(':') && canonicalIPv6(host) === host)
}

/** Whether an upstream is this server itself (loopback, 0.0.0.0/8 or the unspecified address). */
export function isLocalUpstream(value: string): boolean {
  const host = upstreamHost(value).toLowerCase()
  if (host === 'localhost' || host.endsWith('.localhost')) return true
  if (IPV4.test(host)) return host.startsWith('127.') || host.startsWith('0.')
  const v6 = canonicalIPv6(host)
  return v6 === '::1' || v6 === '::'
}

/** host:port, with [v6]:port allowed. */
export function isHostPort(value: string): boolean {
  const v6 = /^\[([0-9a-fA-F:.]+)\]:(\d{1,5})$/.exec(value)
  const plain = /^([A-Za-z0-9.-]+):(\d{1,5})$/.exec(value)
  const match = v6 ?? plain
  if (!match) return false
  const port = Number(match[2])
  if (port < 1 || port > 65535) return false
  const host = match[1] ?? ''
  return v6 !== null || host === 'localhost' || isSiteHost(host.toLowerCase())
}
