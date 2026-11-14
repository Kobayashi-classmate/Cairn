<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterView, useRoute, useRouter } from 'vue-router'
import { NH2, NTab, NTabs } from 'naive-ui'
import type { Permission } from '@/auth/permissions'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()

interface SettingsTab {
  name: string
  labelKey: string
  permission?: Permission
}

// Each tab is a child route; the route also checks the permission.
const allTabs: SettingsTab[] = [
  { name: 'settings-account', labelKey: 'settings.tabs.account' },
  { name: 'settings-security', labelKey: 'settings.tabs.security' },
  { name: 'settings-sessions', labelKey: 'settings.tabs.sessions' },
  { name: 'settings-tokens', labelKey: 'settings.tabs.tokens' },
  { name: 'settings-panel', labelKey: 'settings.tabs.panel', permission: 'settings.read' },
  { name: 'settings-updates', labelKey: 'settings.tabs.updates', permission: 'settings.read' },
  {
    name: 'settings-notifications',
    labelKey: 'settings.tabs.notifications',
    permission: 'notify.manage',
  },
  { name: 'settings-api', labelKey: 'settings.tabs.api' },
]

const tabs = computed(() => allTabs.filter((tab) => !tab.permission || session.can(tab.permission)))
const active = computed(() => String(route.name ?? ''))

function open(name: string | number) {
  void router.push({ name: String(name) })
}
</script>

<template>
  <div class="settings">
    <NH2 class="settings__title">{{ t('settings.title') }}</NH2>
    <NTabs :value="active" type="line" @update:value="open">
      <NTab v-for="tab in tabs" :key="tab.name" :name="tab.name">{{ t(tab.labelKey) }}</NTab>
    </NTabs>
    <div class="settings__body">
      <RouterView />
    </div>
  </div>
</template>

<style scoped>
.settings {
  max-inline-size: 1100px;
}

.settings__title {
  margin-block: 0 8px;
}

.settings__body {
  display: flex;
  flex-direction: column;
  gap: 16px;
  margin-block-start: 16px;
}
</style>
