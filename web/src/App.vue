<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, defineAsyncComponent, watchEffect } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterView, useRoute } from 'vue-router'
import {
  NButton,
  NConfigProvider,
  NDialogProvider,
  NGlobalStyle,
  NMessageProvider,
  NResult,
  NSpin,
  darkTheme,
  useOsTheme,
} from 'naive-ui'
import { errorMessage } from '@/i18n/errors'
import { naiveLocaleFor } from '@/i18n/naive'
import { useSessionStore } from '@/stores/session'

// Only signed-in users can be asked to re-authenticate; keep it out of the entry chunk.
const StepUpDialog = defineAsyncComponent(() => import('@/components/StepUpDialog.vue'))

const { t, locale } = useI18n()
const route = useRoute()
const session = useSessionStore()

// Follow the operating system's light/dark preference.
const osTheme = useOsTheme()
const theme = computed(() => (osTheme.value === 'dark' ? darkTheme : null))
const naive = computed(() => naiveLocaleFor(locale.value))

watchEffect(() => {
  const app = t('common.app.name')
  const key = route.meta.titleKey
  document.title = key ? t('common.app.pageTitle', { page: t(key), app }) : app
})

function reload() {
  window.location.reload()
}
</script>

<template>
  <NConfigProvider :theme="theme" :locale="naive.locale" :date-locale="naive.dateLocale">
    <NGlobalStyle />
    <NMessageProvider>
      <NDialogProvider>
        <div v-if="session.bootError" class="app-state">
          <NResult
            status="error"
            :title="t('common.boot.failed')"
            :description="errorMessage(session.bootError)"
          >
            <template #footer>
              <NButton @click="reload">{{ t('common.action.retry') }}</NButton>
            </template>
          </NResult>
        </div>
        <div v-else-if="!session.initialized" class="app-state">
          <NSpin />
        </div>
        <RouterView v-else />
        <StepUpDialog v-if="session.isAuthenticated" />
      </NDialogProvider>
    </NMessageProvider>
  </NConfigProvider>
</template>

<style scoped>
.app-state {
  display: flex;
  align-items: center;
  justify-content: center;
  min-block-size: 100vh;
  min-block-size: 100dvh;
  padding: 16px;
}
</style>
