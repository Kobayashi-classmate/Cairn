// SPDX-License-Identifier: AGPL-3.0-only
//
// Firewall (`firewall.read` to view, `firewall.manage` to change; every
// change is step-up protected). Each change comes back with a pending id and
// is rolled back unless confirmed over a new connection within a minute
// (see useFirewallConfirm), so a change that locks the panel out undoes itself.
import { api } from './client'
import type { Job } from './jobs'

export type FirewallBackend = 'nftables' | 'ufw' | 'firewalld' | ''
export type RuleAction = 'allow' | 'deny'
export type RuleProtocol = 'tcp' | 'udp' | 'both'

export const RULE_PROTOCOLS: readonly RuleProtocol[] = ['tcp', 'udp', 'both']

export interface FirewallRule {
  id: string
  action: RuleAction
  protocol: RuleProtocol
  /** Ports and ranges ("80", "8000-8100"). */
  ports: string[]
  /** Addresses or CIDR ranges; empty means anywhere. */
  sources: string[]
  note: string
  /**
   * "" for the user's own rules; "ssh" and "panel" are always allowed;
   * "stream:<code>" and "db:<engine>" were opened by another feature.
   */
  owner: string
}

export type RuleRequest = Omit<FirewallRule, 'id' | 'owner'>

export interface FirewallAddress {
  id: string
  /** An address or a CIDR range. */
  address: string
  /** allow: trusted on every port; deny: blocked on every port. */
  action: RuleAction
  note: string
}

export type AddressRequest = Omit<FirewallAddress, 'id'>

/** A change waiting to be confirmed. */
export interface FirewallPending {
  id: string
  /** Unix seconds; the change is rolled back after this. */
  expires: number
  kind: string
}

export interface FirewallRollback {
  id: string
  at: number
  kind: string
}

export interface FirewallView {
  /** The backend in use, or the one enabling would use ("": none possible). */
  backend: FirewallBackend
  /** The backend's tool is installed (enabling installs it otherwise). */
  installed: boolean
  enabled: boolean
  /** The rules are really in effect. */
  loaded: boolean
  /** Two firewalls are active at once ("ufw_and_firewalld", …); changes are refused. */
  conflict: string
  /** Without systemd the firewall cannot be managed. */
  systemd: boolean
  blockPing: boolean
  sshPorts: number[]
  panelPort: number
  rules: FirewallRule[]
  addresses: FirewallAddress[]
  /** Rules not made by the panel, as the backend lists them. */
  external: string[]
  pending: FirewallPending | null
  lastRollback: FirewallRollback | null
  /** The address the panel sees for this browser. */
  clientIp: string
}

/** What a change answers. */
export interface ChangeResult {
  pending: FirewallPending
}

/** Most ports and sources per rule, longest note. */
export const PORTS_MAX = 15
export const SOURCES_MAX = 20
export const NOTE_MAX = 64

function jobOf(request: Promise<{ job: Job }>): Promise<Job> {
  return request.then((body) => body.job)
}

export async function fetchFirewall(signal?: AbortSignal): Promise<FirewallView> {
  const view = await api.get<FirewallView>('/firewall', { signal })
  // The server leaves "sources" out of rules that apply to every address.
  for (const rule of view.rules ?? []) rule.sources ??= []
  return view
}

export function addRule(body: RuleRequest): Promise<ChangeResult & { rule: FirewallRule }> {
  return api.post('/firewall/rules', body)
}

/** For rules opened by another feature only the sources change. */
export function updateRule(id: string, body: RuleRequest): Promise<ChangeResult> {
  return api.put(`/firewall/rules/${encodeURIComponent(id)}`, body)
}

export function deleteRule(id: string): Promise<ChangeResult> {
  return api.delete(`/firewall/rules/${encodeURIComponent(id)}`)
}

export function addAddress(body: AddressRequest): Promise<ChangeResult & { address: FirewallAddress }> {
  return api.post('/firewall/addresses', body)
}

export function deleteAddress(id: string): Promise<ChangeResult> {
  return api.delete(`/firewall/addresses/${encodeURIComponent(id)}`)
}

export function setPingBlocked(block: boolean): Promise<ChangeResult> {
  return api.put('/firewall/ping', { block })
}

/** Installs the backend if needed; the job's result names the change to confirm. */
export function enableFirewall(): Promise<Job> {
  return jobOf(api.post('/firewall/enable'))
}

export function disableFirewall(): Promise<Job> {
  return jobOf(api.post('/firewall/disable'))
}

/** Confirms a change; must go over a new connection (the server closed the old one). */
export function confirmChange(id: string): Promise<void> {
  return api.post<void>('/firewall/confirm', { id })
}

/** A port ("22") or a range ("8000-8100") the backend accepts. */
export function isPortSpec(value: string): boolean {
  const match = /^(\d{1,5})(?:-(\d{1,5}))?$/.exec(value.trim())
  if (!match) return false
  const low = Number(match[1])
  const high = match[2] === undefined ? low : Number(match[2])
  return low >= 1 && high <= 65535 && low <= high && (match[2] === undefined || low < high)
}
