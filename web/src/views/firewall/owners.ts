// SPDX-License-Identifier: AGPL-3.0-only
import { engineIdLabel } from '@/databases/labels'
import { i18n } from '@/i18n'

/** Who made a rule: "ssh" and "panel" are protected, "stream:<code>" and "db:<engine>" belong to features. */
export function ownerLabel(owner: string): string {
  const { t } = i18n.global
  if (owner === 'ssh') return t('firewall.owner.ssh')
  if (owner === 'panel') return t('firewall.owner.panel')
  if (owner.startsWith('stream:')) return t('firewall.owner.stream', { name: owner.slice('stream:'.length) })
  if (owner.startsWith('db:')) return t('firewall.owner.db', { engine: engineIdLabel(owner.slice('db:'.length)) })
  return owner
}

/** Always-allowed rules that keep the server reachable. */
export function isProtected(owner: string): boolean {
  return owner === 'ssh' || owner === 'panel'
}
