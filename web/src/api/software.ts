// SPDX-License-Identifier: AGPL-3.0-only
//
// Software catalog as offered on this system (`software.read`) and package
// index refresh (`software.manage`).
import { api, type ErrorParams, type RequestOptions } from './client'
import type { Job } from './jobs'

export type InstallMethod = 'system' | 'vendor' | 'standalone'

/** One version line of a software product on this system. */
export interface Offer {
  /** Version line, for example "8.4". */
  line: string
  /** How it would be installed; absent or "" when it is not available here. */
  method?: InstallMethod | ''
  /** Repository id for method "vendor". */
  repo?: string
  provider?: string
  packages?: string[]
  /** Module stream on systems that use them. */
  stream?: string
  /** Exactly one available line per product is recommended. */
  recommended: boolean
  lts: boolean
  /** End of upstream support, YYYY-MM-DD. */
  eol?: string
  /** Upstream support has ended. */
  endOfLife: boolean
  /** Why the line is not available: "end_of_life" or "not_built". */
  reason?: string
  /** systemd units started after installation. */
  services?: string[]
  /** Whether the line is installed (always false when the status is unknown). */
  installed: boolean
  /** Exact installed versions of the line's packages. */
  installedPackages?: InstalledPackage[]
  /**
   * Configuration files of the line that can be edited (catalog data,
   * present whether or not the line is installed).
   */
  configs?: ConfigFile[]
  /** The line has PHP extension management (PHP lines only). */
  extensions?: boolean
  /**
   * The line's systemd services with their state; only for installed lines,
   * when the status is available and the system runs systemd.
   */
  units?: ServiceUnit[]
}

/** What happens after a configuration file is saved. */
export type ConfigApply = 'reload' | 'restart' | 'none'

/** An editable configuration file of a line. */
export interface ConfigFile {
  /** "nginx.conf", "php.ini", "50-server.cnf", ... */
  id: string
  /** Absolute path. */
  path: string
  apply: ConfigApply
  /** The unit reloaded or restarted after saving. */
  service?: string
  /** A syntax check runs before the change is applied. */
  check: boolean
}

export type UnitState = 'running' | 'stopped' | 'failed' | 'unknown'

/** A systemd service of an installed line. */
export interface ServiceUnit {
  name: string
  state: UnitState
  canReload: boolean
}

export interface InstalledPackage {
  name: string
  version: string
}

export interface SoftwareEntry {
  id: string
  /** Product name; not translated. */
  name: string
  category: string
  homepage: string
  offers: Offer[]
}

export interface Repository {
  id: string
  name: string
  homepage: string
  /** The software publisher's own repository. */
  official: boolean
}

export interface SoftwareCatalog {
  /** Platform profile, for example "debian-13"; "" when unsupported. */
  release: string
  arch: string
  supported: boolean
  /** False when the panel could not find out what is installed; nothing is then marked installed. */
  statusAvailable: boolean
  software: SoftwareEntry[]
  repos: Repository[]
}

export function fetchSoftware(): Promise<SoftwareCatalog> {
  return api.get<SoftwareCatalog>('/software')
}

/** Starts refreshing the system package indexes; follow the returned job. */
export async function refreshPackageIndexes(): Promise<Job> {
  return (await api.post<{ job: Job }>('/software/refresh')).job
}

/**
 * Display names of the providers of prebuilt (method "standalone") builds.
 * Product names are not translated; an unknown provider is shown as sent.
 */
const PROVIDER_NAMES: Record<string, string> = {
  nodejs: 'nodejs.org',
  'python-build-standalone': 'python-build-standalone (Astral)',
  go: 'go.dev',
  temurin: 'Eclipse Temurin (Adoptium)',
  dotnet: 'Microsoft .NET',
}

export function providerName(provider: string): string {
  return PROVIDER_NAMES[provider] ?? provider
}

/**
 * Starts installing a line (`software.manage`); follow the returned job.
 * `source` is a candidate id of the line's runtime download slot (see
 * probeSources); leave it out for other lines or to use the recommended one.
 */
export async function installSoftware(id: string, line: string, source?: string): Promise<Job> {
  const body = source ? { line, source } : { line }
  return (await api.post<{ job: Job }>(`/software/${encodeURIComponent(id)}/install`, body)).job
}

/**
 * Starts uninstalling a line (`software.manage`, step-up). Packages are
 * removed, not purged: configuration and data are kept.
 */
export async function removeSoftware(id: string, line: string): Promise<Job> {
  return (await api.post<{ job: Job }>(`/software/${encodeURIComponent(id)}/remove`, { line })).job
}

export type ServiceAction = 'start' | 'stop' | 'restart' | 'reload'

/** Starts, stops, restarts or reloads every service of a line (`software.manage`); follow the job. */
export async function controlSoftwareService(id: string, line: string, action: ServiceAction): Promise<Job> {
  return (await api.post<{ job: Job }>(`/software/${encodeURIComponent(id)}/service`, { line, action })).job
}

/** A PHP extension of a line. */
export interface PhpExtension {
  id: string
  /** Product name; not translated. */
  name: string
  /** Modules it provides, as `php -m` prints them (lower case). */
  modules: string[]
  /** It can be installed on this system. */
  available: boolean
  /** Its package is installed. */
  installed: boolean
  /** All its modules are loaded. */
  loaded: boolean
  /** Loaded without a package of its own: cannot be installed or removed. */
  builtin: boolean
}

export interface ExtensionsResponse {
  extensions: PhpExtension[]
  /** Every module the line's PHP loads, lower case and sorted. */
  modules: string[]
}

function linePath(id: string, line: string): string {
  return `/software/${encodeURIComponent(id)}/lines/${encodeURIComponent(line)}`
}

export function fetchExtensions(id: string, line: string, signal?: AbortSignal): Promise<ExtensionsResponse> {
  return api.get<ExtensionsResponse>(`${linePath(id, line)}/extensions`, { signal })
}

/** Installs and removes extensions in one job, then reloads the line's PHP-FPM. */
export async function changeExtensions(id: string, line: string, change: { install?: string[]; remove?: string[] }): Promise<Job> {
  return (await api.post<{ job: Job }>(`${linePath(id, line)}/extensions`, change)).job
}

/** One configuration file as read from the server. */
export interface ConfigView {
  id: string
  path: string
  /** UTF-8 text, at most 1 MiB. */
  content: string
  /** Sent back as `base` when saving. */
  sha256: string
  size: number
  /** Unix seconds. */
  modTime: number
  /** False when the server does not allow editing (`root_access = "never"`). */
  editable: boolean
  /** Earlier versions kept when saving, newest first. */
  versions: ConfigVersion[]
}

export interface ConfigVersion {
  id: string
  savedAt: number
  size: number
}

export interface ConfigVersionContent extends ConfigVersion {
  content: string
}

function configPath(id: string, line: string, config: string): string {
  return `${linePath(id, line)}/configs/${encodeURIComponent(config)}`
}

/** Step-up protected. */
export function fetchConfig(id: string, line: string, config: string, signal?: AbortSignal): Promise<ConfigView> {
  return api.get<ConfigView>(configPath(id, line, config), { signal })
}

/** Step-up protected. */
export function fetchConfigVersion(id: string, line: string, config: string, version: string): Promise<ConfigVersionContent> {
  return api.get<ConfigVersionContent>(`${configPath(id, line, config)}/versions/${encodeURIComponent(version)}`)
}

/**
 * Saves a configuration file (step-up protected); follow the job, which
 * checks the file and reloads or restarts its service, putting the previous
 * file back when either fails. `base` is the sha256 of the file as read.
 */
export async function saveConfig(id: string, line: string, config: string, content: string, base: string): Promise<Job> {
  return (await api.put<{ job: Job }>(configPath(id, line, config), { content, base })).job
}

/** Lines the panel can install and remove: packages and prebuilt runtimes. */
export function isManageable(offer: Offer): boolean {
  return offer.method === 'system' || offer.method === 'vendor' || offer.method === 'standalone'
}

/** Prebuilt runtimes are unpacked here, one directory per exact version. */
const RUNTIME_ROOT = '/opt/cairn/runtimes'

/**
 * Paths of an installed prebuilt runtime: `line` always points to the
 * installed build of the line (projects use it), `build` is the exact
 * version's directory. Null for anything that is not an installed runtime.
 */
export function runtimePaths(id: string, offer: Offer): { line: string; build: string } | null {
  const version = offer.installedPackages?.[0]?.version
  if (offer.method !== 'standalone' || !offer.installed || !version) return null
  return { line: `${RUNTIME_ROOT}/${id}/${offer.line}`, build: `${RUNTIME_ROOT}/${id}/${version}` }
}

/** True when the line can be installed on this system. */
export function isAvailable(offer: Offer): boolean {
  return !!offer.method
}

/** "recommended": the fastest source serving exactly the official file; "official": upstream addresses only. */
export type DownloadPolicy = 'recommended' | 'official'
export const DOWNLOAD_POLICIES: readonly DownloadPolicy[] = ['recommended', 'official']

export type CandidateKind = 'official' | 'official_mirror' | 'supplementary' | 'other'

export type CandidateStatus =
  | 'in_sync'
  | 'newer'
  | 'unverified'
  | 'behind'
  | 'mismatch'
  | 'not_found'
  | 'unreachable'
  | 'skipped'

/** One place a slot's download or index can come from. */
export interface SourceCandidate {
  /** Pass as `source` when installing (runtime slots only). */
  id: string
  kind: CandidateKind
  name: string
  /** The file (downloads) or the repository base address. */
  url: string
  /** The system currently uses this source. */
  inUse: boolean
  status: CandidateStatus
  /** Serves exactly the official file or index. */
  eligible: boolean
  /** Unix seconds the index was generated. */
  indexTime?: number
  /** How much older the index is than the official one. */
  lagSeconds?: number
  /** Time to the first response. */
  latencyMs?: number
  /** Measured transfer rate in bytes per second; 0 or absent when not measured. */
  speed?: number
  /** Full size of the download from this candidate in bytes (runtime slots). */
  size?: number
  recommended: boolean
  /** Technical description when the candidate did not answer. */
  error?: string
}

export type SlotWarning = 'in_use_behind' | 'in_use_unusable' | 'official_unreachable' | 'no_eligible'

/** A download (runtime) or package index (repository, system) the probed lines need. */
export interface SourceSlot {
  /** "runtime:<software>:<line>", "repo:<repo>" or "system:<role>". */
  id: string
  kind: 'runtime' | 'repo' | 'system'
  /** Catalog ids of the probed entries that use this slot. */
  software: string[]
  repo?: string
  /** System slots: "main" or "security". */
  role?: 'main' | 'security'
  /** Runtime slots: the exact version and file that would be downloaded. */
  version?: string
  file?: string
  /** Runtime slots: size of the download in bytes (absent or 0 when unknown). */
  size?: number
  /** Repository slots: the compared suites. */
  suites?: string[]
  /** Only runtime downloads can be pointed at another source. */
  selectable: boolean
  /** Recommended (runtime) or in-use (repository) candidate id. */
  selected?: string
  warning?: SlotWarning
  /** Set when the slot could not be checked at all. */
  error?: { code: string; params?: ErrorParams }
  /** The official candidate comes first. */
  candidates: SourceCandidate[]
}

/** What the package manager would download for the probed package lines. */
export interface PackageEstimate {
  /** False when the package manager could not tell (bytes and packages are then 0). */
  known: boolean
  bytes: number
  packages: number
  /** Lines from repositories that are not added yet are left out. */
  partial?: boolean
}

/** What installing one probed line will do, step by step. */
export interface LinePlan {
  software: string
  line: string
  method: InstallMethod
  /** Vendor lines: catalog repository id. */
  repo?: string
  /** Vendor lines: the repository is already added (otherwise it is added first and its key checked). */
  repoReady: boolean
  /** Module stream enabled first, for example "php:8.3". */
  stream?: string
  /** Packages with the exact version that will be installed; "" when not known yet. */
  packages: InstalledPackage[]
  /** Standalone runtimes: system libraries installed first. */
  libraries?: string[]
  /** Services enabled and started afterwards. */
  services?: string[]
}

export interface ProbeResult {
  policy: DownloadPolicy
  /** Probe time in milliseconds. */
  took: number
  /** Slots are shared between the probed lines. */
  slots: SourceSlot[]
  /** Absent when none of the lines uses the package manager. */
  packages?: PackageEstimate
  /** One plan per probed item. */
  plan?: LinePlan[]
}

/** Slot id of a line's runtime download. */
export function runtimeSlotId(software: string, line: string): string {
  return `runtime:${software}:${line}`
}

export interface ProbeItem {
  software: string
  line: string
}

/** Most lines one probe can check. */
export const MAX_PROBE_ITEMS = 16

/**
 * Checks where the lines would be downloaded from (`software.manage`). This
 * takes a few seconds, up to about 45.
 */
export function probeSources(items: ProbeItem[], options?: RequestOptions): Promise<ProbeResult> {
  return api.post<ProbeResult>('/software/probe', { items }, options)
}

export interface SoftwareSettings {
  downloadPolicy: DownloadPolicy
}

export function fetchSoftwareSettings(): Promise<SoftwareSettings> {
  return api.get<SoftwareSettings>('/software/settings')
}

export function saveSoftwareSettings(settings: SoftwareSettings): Promise<SoftwareSettings> {
  return api.put<SoftwareSettings>('/software/settings', settings)
}

/** One item of a suite, resolved for this machine. */
export interface SuiteItem {
  /** The first of `choices` offered here, with its recommended line. */
  software?: string
  line?: string
  /** Catalog ids in order of preference. */
  choices: string[]
  /** None of the choices is offered on this system. */
  missing?: boolean
}

/** A preselection of the first-login wizard; names come from i18n by id. */
export interface Suite {
  id: string
  items: SuiteItem[]
}

export async function fetchSuites(): Promise<Suite[]> {
  return (await api.get<{ suites: Suite[] }>('/software/suites')).suites
}

export interface InstallSetItem {
  software: string
  line: string
  /** Candidate id of the line's runtime download slot; runtime lines only. */
  source?: string
}

/**
 * Installs several lines as one job (`software.install_set`): package lines
 * in one transaction, runtimes downloading meanwhile. 1 to 16 items, each
 * software and line at most once.
 */
export async function installSoftwareSet(items: InstallSetItem[]): Promise<Job> {
  return (await api.post<{ job: Job }>('/software/install', { items })).job
}
