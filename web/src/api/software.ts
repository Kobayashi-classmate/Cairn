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

export interface ProbeResult {
  policy: DownloadPolicy
  /** Probe time in milliseconds. */
  took: number
  slots: SourceSlot[]
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
