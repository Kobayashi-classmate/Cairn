// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './client'

export interface HostInfo {
  hostname: string
  os: string
  kernel: string
  arch: string
  cpuModel: string
  cpuCores: number
  uptimeSeconds: number
}

export interface DiskUsage {
  mount: string
  fstype: string
  /** Bytes. */
  total: number
  /** Bytes. */
  used: number
}

export interface CurrentMetrics {
  /** Unix time in seconds. */
  ts: number
  cpuPercent: number
  load1: number
  load5: number
  load15: number
  /** Memory and swap values are in bytes. */
  memTotal: number
  memUsed: number
  memAvailable: number
  swapTotal: number
  swapUsed: number
  /** Bytes per second. */
  netRxBps: number
  netTxBps: number
  disks: DiskUsage[]
}

export interface HistoryPoint {
  /** Unix time in seconds. */
  ts: number
  cpuPercent: number
  memUsedPercent: number
  netRxBps: number
  netTxBps: number
}

export interface SystemOverview {
  host: HostInfo
  current: CurrentMetrics
  /** Up to the last hour, one point every 5 seconds, oldest first. */
  history: HistoryPoint[]
}

export function fetchSystemOverview(signal?: AbortSignal): Promise<SystemOverview> {
  return api.get<SystemOverview>('/system/overview', { signal })
}
