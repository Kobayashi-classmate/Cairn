// SPDX-License-Identifier: AGPL-3.0-only
//
// Firewall (`firewall.read` to view, `firewall.manage` to change; every
// change is step-up protected): port rules, trusted and blocked addresses,
// port forwards and ping. While the firewall is on, each change comes back
// with a pending id and is rolled back unless confirmed over a new
// connection within a minute (see useFirewallConfirm), so a change that
// locks the panel out undoes itself.
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

/**
 * Connections arriving on `port` go on to `toPort` of this host (no
 * `toAddress`) or of another IPv4 host. Forwards to another host need the
 * kernel to forward IPv4, which the server switches on while one exists, and
 * are masqueraded so that replies come back through this server.
 */
export interface FirewallForward {
  id: string
  protocol: RuleProtocol
  port: number
  /** An IPv4 address of another host; absent for this host. */
  toAddress?: string
  toPort: number
  note?: string
}

export type ForwardRequest = Omit<FirewallForward, 'id'>

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
  forwards: FirewallForward[]
  /** The kernel forwards IPv4 packets now (forwards to other hosts need it). */
  ipForward: boolean
  /** Rules not made by the panel, as the backend lists them. */
  external: string[]
  pending: FirewallPending | null
  lastRollback: FirewallRollback | null
  /** The address the panel sees for this browser. */
  clientIp: string
}

/** What a change answers; no pending change while the firewall is off. */
export interface ChangeResult {
  pending: FirewallPending | null
}

/** Most ports and sources per rule, longest note, most forwards. */
export const PORTS_MAX = 15
export const SOURCES_MAX = 20
export const NOTE_MAX = 64
export const FORWARDS_MAX = 100
/** Seconds a change waits for its confirmation. */
export const CONFIRM_SECONDS = 60

function jobOf(request: Promise<{ job: Job }>): Promise<Job> {
  return request.then((body) => body.job)
}

export async function fetchFirewall(signal?: AbortSignal): Promise<FirewallView> {
  const view = await api.get<FirewallView>('/firewall', { signal })
  // The server leaves "sources" out of rules that apply to every address.
  for (const rule of view.rules ?? []) rule.sources ??= []
  view.forwards ??= []
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

export function addForward(body: ForwardRequest): Promise<ChangeResult & { forward: FirewallForward }> {
  return api.post('/firewall/forwards', body)
}

export function updateForward(id: string, body: ForwardRequest): Promise<ChangeResult & { forward: FirewallForward }> {
  return api.put(`/firewall/forwards/${encodeURIComponent(id)}`, body)
}

export function deleteForward(id: string): Promise<ChangeResult> {
  return api.delete(`/firewall/forwards/${encodeURIComponent(id)}`)
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

/** Undoes a pending change now instead of waiting for its rollback. */
export function rollbackChange(id: string): Promise<void> {
  return api.post<void>('/firewall/rollback', { id })
}

/** A port ("22") or a range ("8000-8100") the backend accepts. */
export function isPortSpec(value: string): boolean {
  const match = /^(\d{1,5})(?:-(\d{1,5}))?$/.exec(value.trim())
  if (!match) return false
  const low = Number(match[1])
  const high = match[2] === undefined ? low : Number(match[2])
  return low >= 1 && high <= 65535 && low <= high && (match[2] === undefined || low < high)
}

/** A port forwards may use: 1–65535. */
export function isPort(value: number): boolean {
  return Number.isInteger(value) && value >= 1 && value <= 65535
}

/**
 * An IPv4 address a forward may lead to: another host, so not loopback,
 * unspecified, multicast or the broadcast address (as the server checks).
 */
export function isForwardTarget(value: string): boolean {
  const match = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(value)
  if (!match) return false
  const parts = match.slice(1).map(Number)
  if (parts.some((part, i) => part > 255 || (match[i + 1]!.length > 1 && match[i + 1]!.startsWith('0')))) return false
  const [a, b, c, d] = parts as [number, number, number, number]
  if (a === 127 || (a === 0 && b === 0 && c === 0 && d === 0)) return false
  if (a >= 224 && a <= 239) return false
  return !(a === 255 && b === 255 && c === 255 && d === 255)
}
