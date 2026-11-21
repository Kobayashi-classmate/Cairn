// SPDX-License-Identifier: AGPL-3.0-only
//
// The overview summary (any signed-in user): services of installed software,
// the rows of the security card and the notices at the top of the page. Each
// section only holds what the caller may read.
import { api } from './client'

export type ServiceState = 'running' | 'stopped' | 'failed' | 'unknown'

export interface OverviewService {
  /** Catalog id ("nginx", "php"). */
  software: string
  line: string
  /** systemd unit. */
  unit: string
  /** Installed package version, as the package manager reports it. */
  version?: string
  state: ServiceState
}

export interface OverviewCheck {
  /** Names the finding ("firewall.on"); the UI words it from `params`. */
  code: string
  status: 'ok' | 'warn' | 'bad'
  params?: Record<string, unknown>
  /** Menu route where the finding is dealt with. */
  route?: string
}

export interface OverviewNotice extends OverviewCheck {
  status: 'warn' | 'bad'
}

export interface Overview {
  services: OverviewService[]
  checks: OverviewCheck[]
  notices: OverviewNotice[]
  /** Modules that did not answer in time. */
  unavailable: string[]
}

export async function fetchOverview(signal?: AbortSignal): Promise<Overview> {
  const data = await api.get<Partial<Overview>>('/overview', { signal })
  return {
    services: data.services ?? [],
    checks: data.checks ?? [],
    notices: data.notices ?? [],
    unavailable: data.unavailable ?? [],
  }
}
