// SPDX-License-Identifier: AGPL-3.0-only
//
// Web sites (`sites.read` to view, `sites.manage` to change). Every change
// runs as a job in which the helper renders the configuration, lets nginx
// and php-fpm check it and reloads them, restoring the old files on failure.
import { api } from './client'
import type { Job } from './jobs'

export type SiteType = 'static' | 'php' | 'proxy'
export const SITE_TYPES: readonly SiteType[] = ['static', 'php', 'proxy']

export type SiteState = 'applying' | 'active' | 'failed'

export interface ProxySpec {
  /** Upstream, http(s)://host[:port][/path]. */
  url: string
  /** Pass connection upgrades through. */
  webSocket?: boolean
  /** Send the upstream's own host name instead of the requested one. */
  upstreamHost?: boolean
}

/** A site as the API takes it. `code` only on create; `type` cannot change. */
export interface SiteRequest {
  code?: string
  type: SiteType
  domains: string[]
  /** Web root relative to the site's app directory ("" for the directory itself). */
  root?: string
  index?: string[]
  /** Id of a built-in rewrite rule. */
  rewrite?: string
  /** PHP line of a PHP site. */
  php?: string
  /** Directories (relative) the runtime user may write. */
  writable?: string[]
  proxy?: ProxySpec
}

export interface SiteView extends Required<Pick<SiteRequest, 'type' | 'domains'>> {
  code: string
  root?: string
  index?: string[]
  rewrite?: string
  php?: string
  writable?: string[]
  proxy?: ProxySpec
  /** False keeps the site configured but answering 503. */
  enabled: boolean
  state: SiteState
  /** Why the last change failed. */
  errorCode?: string
  /** Id of the last job that changed the site. */
  lastJob?: string
  /** Unix seconds. */
  createdAt: number
  updatedAt: number
  /** /srv/www/<code> */
  dir: string
  /** cw-<code>: owns and deploys the files. */
  owner: string
  /** cw-<code>-run: runs PHP; may write only the writable directories. */
  runner: string
  /** What the system runs differs from this spec (a change failed). */
  drift: boolean
}

export interface SiteList {
  supported: boolean
  /** Why sites are not supported, for example "selinux_pending". */
  reason?: string
  /** False when the helper could not be asked about nginx and PHP. */
  statusAvailable: boolean
  nginx: { installed: boolean; running: boolean }
  /** Installed PHP lines that have PHP-FPM. */
  php: string[]
  sites: SiteView[]
}

export interface RewriteRule {
  id: string
  /** Product name (not translated), except ids translated by the UI such as "spa". */
  name: string
  types: SiteType[]
}

export type SiteLogKind = 'access' | 'error'

export interface SiteLogChunk {
  data: string
  /** Where the next read continues. */
  offset: number
  /** Current file size; smaller than the requested offset after rotation. */
  size: number
}

export function fetchSites(): Promise<SiteList> {
  return api.get<SiteList>('/sites')
}

export async function fetchRewrites(): Promise<RewriteRule[]> {
  return (await api.get<{ rules: RewriteRule[] }>('/sites/rewrites')).rules
}

export function fetchSite(code: string): Promise<SiteView> {
  return api.get<SiteView>(`/sites/${encodeURIComponent(code)}`)
}

export async function createSite(body: SiteRequest): Promise<Job> {
  return (await api.post<{ job: Job }>('/sites', body)).job
}

export async function updateSite(code: string, body: SiteRequest): Promise<Job> {
  return (await api.put<{ job: Job }>(`/sites/${encodeURIComponent(code)}`, body)).job
}

export async function startSite(code: string): Promise<Job> {
  return (await api.post<{ job: Job }>(`/sites/${encodeURIComponent(code)}/start`)).job
}

/** The site keeps its configuration but answers 503. */
export async function stopSite(code: string): Promise<Job> {
  return (await api.post<{ job: Job }>(`/sites/${encodeURIComponent(code)}/stop`)).job
}

/** Step-up protected. Without `deleteFiles` the directory and users are kept. */
export async function deleteSite(code: string, deleteFiles: boolean): Promise<Job> {
  const query = `deleteFiles=${deleteFiles ? 'true' : 'false'}`
  return (await api.delete<{ job: Job }>(`/sites/${encodeURIComponent(code)}?${query}`)).job
}

/** Reads a site log from `offset`; -1 reads the last part. */
export function fetchSiteLog(
  code: string,
  kind: SiteLogKind,
  offset: number,
  signal?: AbortSignal,
): Promise<SiteLogChunk> {
  return api.get<SiteLogChunk>(`/sites/${encodeURIComponent(code)}/log?kind=${kind}&offset=${offset}`, {
    signal,
  })
}

/** The request that re-applies a site exactly as it is specified. */
export function requestOf(site: SiteView): SiteRequest {
  return {
    type: site.type,
    domains: site.domains,
    root: site.root,
    index: site.index,
    rewrite: site.rewrite,
    php: site.php,
    writable: site.writable,
    proxy: site.proxy,
  }
}
