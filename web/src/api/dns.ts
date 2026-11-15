// SPDX-License-Identifier: AGPL-3.0-only
//
// DNS provider accounts, used for DNS-01 validation (and later for DNS
// record management). Secrets are written but never returned.
import { api } from './client'

export interface ProviderField {
  /** snake_case key, translated as dns.field.<name>. */
  name: string
  secret?: boolean
  optional?: boolean
}

export interface DnsProvider {
  id: string
  /** Product name (not translated). */
  name: string
  homepage: string
  fields: ProviderField[]
}

export interface DnsAccount {
  id: string
  name: string
  provider: string
  /** Non-secret field values. */
  fields: Record<string, string>
  /** Secret fields that have a stored value. */
  secretSet: string[]
  /** Ids of the certificates that use this account. */
  usedBy: string[]
  createdAt: number
  updatedAt: number
}

export interface DnsAccountRequest {
  name: string
  provider: string
  /** On update, an empty secret field keeps the stored value. */
  fields: Record<string, string>
}

export async function fetchDnsProviders(): Promise<DnsProvider[]> {
  return (await api.get<{ providers: DnsProvider[] }>('/dns/providers')).providers
}

export async function fetchDnsAccounts(): Promise<DnsAccount[]> {
  return (await api.get<{ accounts: DnsAccount[] }>('/dns/accounts')).accounts
}

/** Step-up protected. */
export function createDnsAccount(body: DnsAccountRequest): Promise<unknown> {
  return api.post<unknown>('/dns/accounts', body)
}

/** Step-up protected. */
export function updateDnsAccount(id: string, body: DnsAccountRequest): Promise<unknown> {
  return api.put<unknown>(`/dns/accounts/${encodeURIComponent(id)}`, body)
}

/** Step-up protected; refused while certificates use the account. */
export async function deleteDnsAccount(id: string): Promise<void> {
  await api.delete<unknown>(`/dns/accounts/${encodeURIComponent(id)}`)
}

/** Lists the zone's records with the stored credentials; returns how many there are. */
export async function testDnsAccount(id: string, zone: string): Promise<number> {
  return (await api.post<{ records: number }>(`/dns/accounts/${encodeURIComponent(id)}/test`, { zone })).records
}
