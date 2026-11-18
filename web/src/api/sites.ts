// SPDX-License-Identifier: AGPL-3.0-only
//
// Web sites (`sites.read` to view, `sites.manage` to change). Every change
// runs as a job in which the helper renders the configuration, lets nginx
// and php-fpm check it and reloads them, restoring the old files on failure.
import { api } from './client'
import type { Job } from './jobs'

export type SiteType = 'static' | 'php' | 'proxy' | 'app'
export const SITE_TYPES: readonly SiteType[] = ['static', 'php', 'proxy', 'app']

/** Runtimes an app can run on (prebuilt or system packages of these catalog entries). */
export const APP_RUNTIMES = ['nodejs', 'python', 'go', 'java', 'dotnet'] as const
export type AppRuntime = (typeof APP_RUNTIMES)[number]

export interface EnvVar {
  name: string
  value: string
}

/**
 * A long-running application behind nginx, run by systemd as the site's
 * runtime user. The command must listen on 127.0.0.1:$PORT.
 */
export interface AppSpec {
  /** Installed runtime put first on PATH; empty for system tools only. */
  runtime?: AppRuntime
  line?: string
  /** One shell line, run by /bin/sh. */
  command: string
  /** Relative to the site directory. */
  workDir?: string
  /** 0 on create: the panel picks a free port in 20000-29999; on update: keep the current one. */
  port: number
  env?: EnvVar[]
  /** 0 or absent: no limit; at least 16. */
  memoryMb?: number
  /** Percent of one CPU (200 = two); 0 or absent: no limit; at least 5. */
  cpuPercent?: number
  /** Checked every 30 s; three failures restart the app. */
  healthPath?: string
  webSocket?: boolean
}

export type SiteState = 'applying' | 'active' | 'failed'

/** HTTPS of a site: a stored certificate covering every domain. */
export interface SiteTls {
  /** Certificate id. */
  cert: string
  /** Send HTTP visitors to HTTPS. */
  redirect?: boolean
  /** Tell browsers to use HTTPS only (they remember it). */
  hsts?: boolean
}

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
  tls?: SiteTls
  app?: AppSpec
}

export interface SiteView extends Required<Pick<SiteRequest, 'type' | 'domains'>> {
  code: string
  root?: string
  index?: string[]
  rewrite?: string
  php?: string
  writable?: string[]
  proxy?: ProxySpec
  tls?: SiteTls
  app?: AppSpec
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
  /** Why sites are not supported, for example "platform". */
  reason?: string
  /** False when the helper could not be asked about nginx and PHP. */
  statusAvailable: boolean
  nginx: { installed: boolean; running: boolean }
  /** Installed PHP lines that have PHP-FPM. */
  php: string[]
  /** Apps run as systemd services; without systemd they are not available. */
  systemd: boolean
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
    tls: site.tls,
    app: site.app,
  }
}

/** The systemd service of an app site. */
export interface AppStatus {
  code: string
  /** systemd ActiveState: active, inactive, failed, activating, deactivating, ... */
  activeState: string
  subState: string
  mainPid?: number
  restarts: number
  memoryBytes?: number
  cpuNanos?: number
  /** Unix seconds the service entered its state. */
  since?: number
  /** How the last run ended: success, exit-code, signal, oom-kill, ... */
  result?: string
  /** Last health check, when the site has a health path. */
  health?: 'ok' | 'failing'
  failures?: number
}

export interface AppLogChunk {
  lines: string[]
  /** Pass back to continue after these lines. */
  cursor?: string
}

export function fetchAppStatus(code: string, signal?: AbortSignal): Promise<AppStatus> {
  return api.get<AppStatus>(`/sites/${encodeURIComponent(code)}/app`, { signal })
}

/** Without a cursor: the last 300 journal lines. */
export function fetchAppLog(code: string, cursor?: string, signal?: AbortSignal): Promise<AppLogChunk> {
  const query = cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''
  return api.get<AppLogChunk>(`/sites/${encodeURIComponent(code)}/app/log${query}`, { signal })
}

export async function restartSite(code: string): Promise<Job> {
  return (await api.post<{ job: Job }>(`/sites/${encodeURIComponent(code)}/restart`)).job
}
