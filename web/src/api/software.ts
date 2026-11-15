// SPDX-License-Identifier: AGPL-3.0-only
//
// Software catalog as offered on this system (`software.read`) and package
// index refresh (`software.manage`).
import { api } from './client'
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

/** Starts installing a line (`software.manage`); follow the returned job. */
export async function installSoftware(id: string, line: string): Promise<Job> {
  return (await api.post<{ job: Job }>(`/software/${encodeURIComponent(id)}/install`, { line })).job
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
