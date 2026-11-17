// SPDX-License-Identifier: AGPL-3.0-only
//
// System management (`host.read` to view, `host.manage` to change): host
// name, time zone and time sync, swap, reboot, package updates and the
// distribution's package sources. Some changes are step-up protected and
// some run as jobs.
import { api } from './client'
import type { Job } from './jobs'
import type { SourceSlot } from './software'

export interface SwapDevice {
  path: string
  kind: 'file' | 'partition'
  size: number
  used: number
  /** The panel's own swap file. */
  managed: boolean
}

export interface HostSwap {
  total: number
  used: number
  swappiness: number
  devices: SwapDevice[]
  supported: boolean
  /** Why a swap file cannot be made here. */
  reason: '' | 'btrfs_old' | 'container'
}

export interface HostUpdate {
  name: string
  from: string
  to: string
  security: boolean
}

export interface HostView {
  hostname: string
  timezone: string
  ntp: { available: boolean; enabled: boolean; synchronized: boolean }
  swap: HostSwap
  kernel: { running: string; latest: string }
  rebootRequired: boolean
  autoUpdates: { available: boolean; enabled: boolean; tool: string }
  updates: {
    /** Unix seconds of the last check; 0 when never checked. */
    checkedAt: number
    packages: HostUpdate[]
    sources: { role: 'main' | 'security'; warning: '' | 'in_use_behind' | 'in_use_unusable' }[]
  }
}

export interface SourceBackup {
  id: string
  at: number
  role: 'main' | 'security'
  /** Host the sources pointed at before, and after. */
  from: string
  to: string
  files: string[]
}

/** Swap file sizes the server accepts (MiB); 0 removes the panel's swap file. */
export const SWAP_MIN_MB = 128
export const SWAP_MAX_MB = 65536

function jobOf(request: Promise<{ job: Job }>): Promise<Job> {
  return request.then((body) => body.job)
}

export function fetchHost(signal?: AbortSignal): Promise<HostView> {
  return api.get<HostView>('/host', { signal })
}

/** Step-up protected. */
export function setHostname(hostname: string): Promise<void> {
  return api.put<void>('/host/hostname', { hostname })
}

export async function fetchTimezones(signal?: AbortSignal): Promise<string[]> {
  return (await api.get<{ zones: string[] }>('/host/timezones', { signal })).zones
}

export function setTimezone(timezone: string): Promise<void> {
  return api.put<void>('/host/timezone', { timezone })
}

export function setNtp(enabled: boolean): Promise<void> {
  return api.put<void>('/host/ntp', { enabled })
}

/** Step-up protected; 0 removes the panel's swap file. */
export function setSwap(sizeMb: number): Promise<Job> {
  return jobOf(api.put('/host/swap', { sizeMb }))
}

export function setSwappiness(value: number): Promise<void> {
  return api.put<void>('/host/swappiness', { value })
}

/** Step-up protected; answers with when the reboot starts. */
export function rebootHost(): Promise<{ at: number }> {
  return api.post<{ at: number }>('/host/reboot')
}

export function checkUpdates(): Promise<Job> {
  return jobOf(api.post('/host/updates/check'))
}

/** Step-up protected. */
export function installUpdates(securityOnly: boolean): Promise<Job> {
  return jobOf(api.post('/host/updates/install', { securityOnly }))
}

export function setAutoUpdates(enabled: boolean): Promise<Job> {
  return jobOf(api.put('/host/updates/auto', { enabled }))
}

/** Probes the system sources (can take some 15 seconds). */
export async function fetchSystemSources(signal?: AbortSignal): Promise<SourceSlot[]> {
  return (await api.get<{ slots: SourceSlot[] }>('/host/sources', { signal })).slots
}

/** Step-up protected. */
export function switchSystemSource(role: string, candidate: string): Promise<Job> {
  return jobOf(api.post('/host/sources/switch', { role, candidate }))
}

export async function fetchSourceBackups(signal?: AbortSignal): Promise<SourceBackup[]> {
  return (await api.get<{ backups: SourceBackup[] }>('/host/sources/backups', { signal })).backups
}

/** Step-up protected. */
export function restoreSources(id: string): Promise<Job> {
  return jobOf(api.post('/host/sources/restore', { id }))
}
