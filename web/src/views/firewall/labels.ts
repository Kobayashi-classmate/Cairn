// SPDX-License-Identifier: AGPL-3.0-only
//
// How the firewall page names rules, forwards and changes.
import type { FirewallForward, FirewallRule, ForwardRequest, RuleProtocol, RuleRequest } from '@/api/firewall'
import { i18n } from '@/i18n'

export function protocolLabel(protocol: RuleProtocol): string {
  return i18n.global.t(`firewall.protocol.${protocol}`)
}

/** "Allow 80, 443 (TCP)". */
export function describeRule(rule: Pick<FirewallRule, 'action' | 'protocol' | 'ports'>): string {
  const { t } = i18n.global
  return t('firewall.rule.describe', {
    action: t(`firewall.action.${rule.action}`),
    ports: rule.ports.join(', '),
    protocol: protocolLabel(rule.protocol),
  })
}

/** The rule as a request, to put it back or change it. */
export function ruleRequest(rule: FirewallRule): RuleRequest {
  return { action: rule.action, protocol: rule.protocol, ports: [...rule.ports], sources: [...rule.sources], note: rule.note }
}

/** Where a forward leads: "this host, port 80" or "10.0.0.5:80". */
export function forwardTarget(forward: Pick<FirewallForward, 'toAddress' | 'toPort'>): string {
  const { t } = i18n.global
  return forward.toAddress ? `${forward.toAddress}:${forward.toPort}` : t('firewall.forward.thisHostPort', { port: forward.toPort })
}

/** "TCP 8090 to this host, port 80". */
export function describeForward(forward: Pick<FirewallForward, 'protocol' | 'port' | 'toAddress' | 'toPort'>): string {
  return i18n.global.t('firewall.forward.describe', {
    protocol: protocolLabel(forward.protocol),
    port: forward.port,
    target: forwardTarget(forward),
  })
}

/** The forward as a request, to put it back or change it. */
export function forwardRequest(forward: FirewallForward): ForwardRequest {
  const body: ForwardRequest = { protocol: forward.protocol, port: forward.port, toPort: forward.toPort }
  if (forward.toAddress) body.toAddress = forward.toAddress
  if (forward.note) body.note = forward.note
  return body
}

/** A pending change made elsewhere, by its kind ("rule.add", "ping", "enable"). */
export function pendingKindLabel(kind: string): string {
  const { t, te } = i18n.global
  const key = `firewall.pending.kind.${kind}`
  return te(key) ? t(key) : t('firewall.pending.kind.other')
}
