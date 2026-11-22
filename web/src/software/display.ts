// SPDX-License-Identifier: AGPL-3.0-only
//
// How software, its lines and their services are shown: page tabs, line
// states with their tones, sources and the drawer address of a line.
import type { Component } from 'vue'
import { Boxes, Code, Database, Globe, Wrench, Zap } from '@lucide/vue'
import {
  isAvailable,
  providerName,
  type Offer,
  type Repository,
  type SoftwareEntry,
  type UnitState,
} from '@/api/software'
import { i18n } from '@/i18n'
import { formatIsoDate } from '@/i18n/format'
import type { Tone } from '@/lib/tone'

export const SOFTWARE_TABS = ['all', 'installed', 'web', 'runtime', 'database'] as const
export type SoftwareTab = (typeof SOFTWARE_TABS)[number]

/** Whether an entry is listed under a page tab; tools only show under "All". */
export function inTab(entry: SoftwareEntry, tab: SoftwareTab): boolean {
  switch (tab) {
    case 'all':
      return true
    case 'installed':
      return entry.offers.some((offer) => offer.installed)
    case 'database':
      return entry.category === 'database' || entry.category === 'cache'
    default:
      return entry.category === tab
  }
}

const CATEGORY_ICON: Record<string, Component> = { web: Globe, runtime: Code, database: Database, cache: Zap, tool: Wrench }

export function categoryIcon(category: string): Component {
  return CATEGORY_ICON[category] ?? Boxes
}

export function categoryText(category: string): string {
  const { t, te } = i18n.global
  return te(`software.category.${category}`) ? t(`software.category.${category}`) : category
}

export const UNIT_TONE: Record<UnitState, Tone> = {
  running: 'success',
  stopped: 'neutral',
  failed: 'destructive',
  unknown: 'neutral',
}

export function unitStateText(state: UnitState): string {
  return i18n.global.t(`software.unitState.${state}`)
}

/** An installed line as a whole: its services' states, or just installed when it has none. */
export type LineState = 'running' | 'partial' | 'stopped' | 'failed' | 'installed'

export const LINE_STATE_TONE: Record<LineState, Tone> = {
  running: 'success',
  partial: 'warning',
  stopped: 'neutral',
  failed: 'destructive',
  installed: 'success',
}

export function lineState(offer: Offer): LineState {
  const units = offer.units ?? []
  if (units.length === 0 || units.every((unit) => unit.state === 'unknown')) return 'installed'
  if (units.some((unit) => unit.state === 'failed')) return 'failed'
  if (units.every((unit) => unit.state === 'running')) return 'running'
  if (units.some((unit) => unit.state === 'running')) return 'partial'
  return 'stopped'
}

export function lineStateText(state: LineState): string {
  return i18n.global.t(`software.lineState.${state}`)
}

export function installedOffers(entry: SoftwareEntry): Offer[] {
  return entry.offers.filter((offer) => offer.installed)
}

/** Lines that can be installed here and are not installed yet. */
export function installableOffers(entry: SoftwareEntry): Offer[] {
  return entry.offers.filter((offer) => isAvailable(offer) && !offer.installed)
}

/** The exact installed version of a line's first package ("8.4.27-1~deb13u1"). */
export function installedVersion(offer: Offer): string {
  return offer.installedPackages?.[0]?.version ?? ''
}

/** How a line is (or would be) installed, in full: "Additional repository: nginx.org". */
export function methodText(offer: Offer, repos: ReadonlyMap<string, Repository>): string {
  const { t } = i18n.global
  switch (offer.method) {
    case 'system':
      return t('software.method.system')
    case 'vendor':
      return t('software.method.vendor', { repo: repos.get(offer.repo ?? '')?.name ?? offer.repo ?? '' })
    case 'standalone':
      // Not "official": a provider is not necessarily the upstream project.
      return offer.provider
        ? t('software.method.standaloneFrom', { provider: providerName(offer.provider) })
        : t('software.method.standalone')
    default:
      return ''
  }
}

/** Where a line comes from, in a few words for the table: "System", "nginx.org", "nodejs.org". */
export function sourceText(offer: Offer, repos: ReadonlyMap<string, Repository>): string {
  const { t } = i18n.global
  switch (offer.method) {
    case 'system':
      return t('software.source.system')
    case 'vendor':
      return repos.get(offer.repo ?? '')?.name ?? offer.repo ?? ''
    case 'standalone':
      return offer.provider ? providerName(offer.provider) : t('software.source.standalone')
    default:
      return ''
  }
}

/** Why a line cannot be installed here. */
export function reasonText(offer: Offer): string {
  const { t, te } = i18n.global
  return offer.reason && te(`software.reason.${offer.reason}`) ? t(`software.reason.${offer.reason}`) : t('software.reason.unknown')
}

/** Facts about a line for choosing it: "Recommended · LTS · Supported until 2028-12-31". */
export function lineFacts(offer: Offer): string {
  const { t } = i18n.global
  const parts: string[] = []
  if (offer.installed) parts.push(t('software.installed'))
  if (!isAvailable(offer)) {
    parts.push(reasonText(offer))
    return parts.join(' · ')
  }
  if (offer.recommended) parts.push(t('software.recommended'))
  if (offer.lts) parts.push(t('software.lts'))
  if (offer.eol) {
    parts.push(
      offer.endOfLife
        ? t('software.eol.ended', { date: formatIsoDate(offer.eol) })
        : t('software.eol.until', { date: formatIsoDate(offer.eol) }),
    )
  }
  return parts.join(' · ')
}

/** The drawer address of a line: "php@8.4". */
export function lineKey(software: string, line: string): string {
  return `${software}@${line}`
}

/** Splits a drawer address; line is "" when the address names the software only. */
export function parseLineKey(value: string): { software: string; line: string } {
  const at = value.lastIndexOf('@')
  return at < 0 ? { software: value, line: '' } : { software: value.slice(0, at), line: value.slice(at + 1) }
}
