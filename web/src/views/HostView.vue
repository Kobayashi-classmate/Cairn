<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { fetchHost, rebootHost, type HostView } from '@/api/host'
import ErrorState from '@/components/common/ErrorState.vue'
import Notice from '@/components/common/Notice.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import type { PageTab } from '@/components/common/types'
import { Skeleton } from '@/components/ui/skeleton'
import { confirmAction } from '@/composables/useConfirmDialog'
import { usePageTab } from '@/composables/usePageTab'
import { notifySuccess } from '@/lib/notify'
import { useSessionStore } from '@/stores/session'
import GeneralPanel from './host/GeneralPanel.vue'
import SourcesPanel from './host/SourcesPanel.vue'
import SwapPanel from './host/SwapPanel.vue'
import UpdatesPanel from './host/UpdatesPanel.vue'

// The host ("list" skeleton with tabs, ?tab=): host name, time and restart;
// swap; system updates; the distribution's package sources. Long changes
// run as jobs in the job dock.
type Tab = 'general' | 'swap' | 'updates' | 'sources'
const TABS: readonly Tab[] = ['general', 'swap', 'updates', 'sources']

const REBOOT_POLL_MS = 3000
/** After this long the panel counts as back even if it never seemed to go away. */
const ASSUME_BACK_S = 120

const { t } = useI18n()
const session = useSessionStore()
const { tab, tabLocation } = usePageTab(TABS, 'general')

const view = ref<HostView | null>(null)
const loadError = ref<unknown>(null)
const rebooting = ref(false)

const canManage = computed(() => session.can('host.manage'))

let loadSeq = 0
async function load() {
  const mine = ++loadSeq
  try {
    const next = await fetchHost()
    if (mine !== loadSeq) return
    view.value = next
    loadError.value = null
  } catch (err) {
    if (mine === loadSeq) loadError.value = err
  }
}

onMounted(load)

/** Comparing the mirrors takes some seconds: only once the tab was opened, then kept. */
const sourcesUsed = ref(tab.value === 'sources')
watch(tab, (value) => {
  if (value === 'sources') sourcesUsed.value = true
})

const updateCount = computed(() => view.value?.updates.packages.length)
const securityCount = computed(() => view.value?.updates.packages.filter((pkg) => pkg.security).length ?? 0)

const tabs = computed<PageTab<Tab>[]>(() => [
  { value: 'general', label: t('host.tabs.general') },
  { value: 'swap', label: t('host.tabs.swap') },
  { value: 'updates', label: t('host.tabs.updates'), count: updateCount.value || undefined },
  { value: 'sources', label: t('host.tabs.sources') },
])

const status = computed(() => {
  const current = view.value
  if (!current) return ''
  const parts = [current.hostname, current.timezone]
  const count = current.updates.packages.length
  if (count > 0) {
    parts.push(
      securityCount.value > 0
        ? t('host.status.updatesSecurity', { count, security: securityCount.value }, count)
        : t('host.status.updates', { count }, count),
    )
  } else if (current.updates.checkedAt > 0) {
    parts.push(t('host.status.upToDate'))
  }
  return parts.join(' · ')
})

// Restarting: the panel goes away and comes back.
let pollTimer: ReturnType<typeof setTimeout> | undefined

function waitForReturn(at: number) {
  let sawDown = false
  const poll = async () => {
    try {
      await fetchHost()
      if (sawDown || Date.now() / 1000 > at + ASSUME_BACK_S) {
        rebooting.value = false
        notifySuccess(t('host.reboot.back'))
        void load()
        return
      }
    } catch {
      sawDown = true
    }
    pollTimer = setTimeout(() => void poll(), REBOOT_POLL_MS)
  }
  pollTimer = setTimeout(() => void poll(), REBOOT_POLL_MS * 2)
}

function reboot() {
  const current = view.value
  if (!current || rebooting.value) return
  void confirmAction({
    title: t('host.reboot.title', { host: current.hostname }),
    description: t('host.reboot.text'),
    confirmLabel: t('host.reboot.submit'),
    destructive: true,
    run: async () => {
      // Step-up protected: the client asks for the password and retries.
      const { at } = await rebootHost()
      rebooting.value = true
      waitForReturn(at)
    },
  })
}

onBeforeUnmount(() => clearTimeout(pollTimer))
</script>

<template>
  <div>
    <PageHeader
      :title="t('host.title')"
      :status="status"
      :status-loading="!view && !loadError"
      :tabs="tabs"
      :tab="tab"
      :tab-location="tabLocation"
    />

    <div class="flex flex-col gap-4">
      <ErrorState v-if="loadError && !view" :error="loadError" class="rounded-lg border bg-card" @retry="load" />

      <template v-else-if="view">
        <Notice v-if="loadError" tone="destructive">
          {{ t('common.state.refreshFailed') }}
          <template #action>
            <button type="button" class="underline underline-offset-2 hover:no-underline" @click="load">{{ t('common.action.retry') }}</button>
          </template>
        </Notice>
        <Notice v-if="rebooting" tone="info">{{ t('host.reboot.waiting') }}</Notice>
        <Notice v-else-if="view.rebootRequired" tone="warning">
          {{ t('host.reboot.required') }}
          <template v-if="canManage" #action>
            <button type="button" class="underline underline-offset-2 hover:no-underline" @click="reboot">{{ t('host.reboot.submit') }}</button>
          </template>
        </Notice>

        <GeneralPanel v-if="tab === 'general'" :view="view" :can-manage="canManage" :rebooting="rebooting" @changed="load" @reboot="reboot" />
        <SwapPanel v-else-if="tab === 'swap'" :view="view" :can-manage="canManage" @changed="load" />
        <UpdatesPanel v-else-if="tab === 'updates'" :view="view" :can-manage="canManage" @changed="load" />
      </template>

      <div v-else-if="tab !== 'sources'" class="grid gap-4 lg:grid-cols-2" aria-hidden="true">
        <Skeleton class="h-44 w-full rounded-lg" />
        <Skeleton class="h-44 w-full rounded-lg" />
      </div>

      <SourcesPanel v-if="sourcesUsed" v-show="tab === 'sources'" :can-manage="canManage" />
    </div>
  </div>
</template>
