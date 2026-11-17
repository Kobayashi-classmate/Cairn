<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// System management: host name and time, swap, package updates and the
// distribution's package sources.
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NH2, NSpin, NTabPane, NTabs } from 'naive-ui'
import { fetchHost, type HostView } from '@/api/host'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'
import GeneralPanel from './host/GeneralPanel.vue'
import SourcesPanel from './host/SourcesPanel.vue'
import SwapPanel from './host/SwapPanel.vue'
import UpdatesPanel from './host/UpdatesPanel.vue'

const TABS = ['general', 'swap', 'updates', 'sources'] as const

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()

const view = ref<HostView | null>(null)
const loadError = ref<unknown>(null)
const tab = ref<string>(TABS.find((name) => name === route.query.tab) ?? 'general')
watch(tab, (value) => void router.replace({ query: value === 'general' ? {} : { tab: value } }))

const canManage = computed(() => session.can('host.manage'))

async function load() {
  try {
    view.value = await fetchHost()
    loadError.value = null
  } catch (err) {
    loadError.value = err
  }
}

onMounted(load)
</script>

<template>
  <div class="host">
    <NH2 class="host__title">{{ t('host.title') }}</NH2>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NSpin v-if="!view && !loadError" class="wait" />
    <template v-else-if="view">
      <NAlert v-if="view.rebootRequired && tab !== 'general'" type="warning" class="block">{{ t('host.reboot.required') }}</NAlert>
      <NTabs v-model:value="tab" type="line">
        <NTabPane name="general" :tab="t('host.tabs.general')">
          <GeneralPanel :view="view" :can-manage="canManage" @changed="load" />
        </NTabPane>
        <NTabPane name="swap" :tab="t('host.tabs.swap')">
          <SwapPanel :view="view" :can-manage="canManage" @changed="load" />
        </NTabPane>
        <NTabPane name="updates" :tab="t('host.tabs.updates')">
          <UpdatesPanel :view="view" :can-manage="canManage" @changed="load" @open-sources="tab = 'sources'" />
        </NTabPane>
        <!-- Comparing the mirrors takes some seconds: only when the tab is opened, then kept. -->
        <NTabPane name="sources" :tab="t('host.tabs.sources')" display-directive="show:lazy">
          <SourcesPanel :can-manage="canManage" />
        </NTabPane>
      </NTabs>
    </template>
  </div>
</template>

<style scoped>
.host {
  max-inline-size: 1300px;
}

.host__title {
  margin: 0 0 12px;
}

.wait {
  display: flex;
  justify-content: center;
  padding-block: 64px;
}

.block {
  margin-block-end: 12px;
}
</style>
