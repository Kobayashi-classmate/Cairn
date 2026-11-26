<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, type RouteLocationRaw } from 'vue-router'
import PageHeader from '@/components/common/PageHeader.vue'
import type { PageTab } from '@/components/common/types'
import { TAB_QUERY } from '@/composables/usePageTab'
import { useSessionStore } from '@/stores/session'
import SshLogins from './ssh/SshLogins.vue'
import JournalTab from './logs/JournalTab.vue'
import SiteLogsTab from './logs/SiteLogsTab.vue'

// Logs ("list" skeleton): the system journal, the sites' access and error
// logs and the SSH sign-ins, a tab each, shown with the permission to read
// them. Each tab keeps its filters in the address (`?unit=`, `?site=&kind=`),
// so other pages link straight to them. Without `logs.system` the journal
// covers the panel's, the software's, app sites' and tasks' units only, and
// the tab says so.
type Tab = 'system' | 'sites' | 'ssh'

const { t } = useI18n()
const route = useRoute()
const session = useSessionStore()

const allowed = computed<Tab[]>(() => {
  const list: Tab[] = []
  if (session.can('logs.read')) list.push('system')
  if (session.can('sites.read')) list.push('sites')
  if (session.can('ssh.read')) list.push('ssh')
  return list
})

const tab = computed<Tab | null>(() => {
  const value = route.query[TAB_QUERY]
  if (typeof value === 'string' && (allowed.value as string[]).includes(value)) return value as Tab
  return allowed.value[0] ?? null
})

/** A tab's link: the page with only that tab, its filters left behind. */
function tabLocation(value: Tab): RouteLocationRaw {
  return { query: value === allowed.value[0] ? {} : { [TAB_QUERY]: value } }
}

/** The journal tab's texts: the whole journal, or the services' part of it. */
function textKey(value: Tab): string {
  return value === 'system' && !session.can('logs.system') ? 'services' : value
}

const tabs = computed<PageTab<Tab>[]>(() => allowed.value.map((value) => ({ value, label: t(`logs.tabs.${textKey(value)}`) })))

const tabStatus = ref('')
watch(tab, () => (tabStatus.value = ''))

const status = computed(() => tabStatus.value || (tab.value ? t(`logs.intro.${textKey(tab.value)}`) : ''))
</script>

<template>
  <div>
    <PageHeader :title="t('logs.title')" :status="status" :tabs="tabs" :tab="tab ?? undefined" :tab-location="tabLocation" />

    <JournalTab v-if="tab === 'system'" @status="(text: string) => (tabStatus = text)" />
    <SiteLogsTab v-else-if="tab === 'sites'" @status="(text: string) => (tabStatus = text)" />
    <SshLogins v-else-if="tab === 'ssh'" />
  </div>
</template>
