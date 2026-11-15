// SPDX-License-Identifier: AGPL-3.0-only
import type { RewriteRule, SiteView } from '@/api/sites'
import { i18n } from '@/i18n'

export type SiteDisplayState = 'active' | 'stopped' | 'applying' | 'failed'

/** What a site is doing, as shown in lists and headers. */
export function siteDisplayState(site: SiteView): SiteDisplayState {
  if (site.state === 'applying') return 'applying'
  if (site.state === 'failed') return 'failed'
  return site.enabled ? 'active' : 'stopped'
}

export function siteStateTagType(state: SiteDisplayState): 'success' | 'default' | 'info' | 'error' {
  return { active: 'success', stopped: 'default', applying: 'info', failed: 'error' }[state] as
    | 'success'
    | 'default'
    | 'info'
    | 'error'
}

export function siteTypeLabel(type: string): string {
  const key = `sites.type.${type}.name`
  return i18n.global.te(key) ? i18n.global.t(key) : type
}

/** Rewrite rules carry product names; generic ones (such as "spa") are translated. */
export function rewriteLabel(rule: Pick<RewriteRule, 'id' | 'name'>): string {
  const key = `sites.rewrite.${rule.id}`
  return i18n.global.te(key) ? i18n.global.t(key) : rule.name
}
