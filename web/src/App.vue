<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, defineAsyncComponent, watchEffect } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterView, useRoute } from 'vue-router'
import { CircleAlert } from '@lucide/vue'
import CairnLogo from '@/components/common/CairnLogo.vue'
import { Button } from '@/components/ui/button'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'
import { errorMessage } from '@/i18n/errors'
import { useJobDockStore } from '@/stores/jobDock'
import { useSessionStore } from '@/stores/session'
import { useThemeStore } from '@/stores/theme'

// Only signed-in users can be asked to re-authenticate; keep it out of the entry chunk.
const StepUpDialog = defineAsyncComponent(() => import('@/components/StepUpDialog.vue'))

const { t } = useI18n()
const route = useRoute()
const session = useSessionStore()
const theme = useThemeStore()

// Toasts sit above the job dock (both live in the bottom right corner).
const dock = useJobDockStore()
const toastOffset = computed(() => (dock.height > 0 && !dock.aside ? { bottom: dock.height + 28, right: 16 } : undefined))
const toastMobileOffset = computed(() => (dock.height > 0 ? { bottom: dock.height + (dock.aside ? 96 : 28) } : undefined))

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
  <TooltipProvider :delay-duration="300">
    <main v-if="session.bootError" class="flex min-h-dvh items-center justify-center p-4">
      <div role="alert" class="flex max-w-md flex-col items-center gap-3 text-center">
        <CircleAlert class="size-8 text-destructive" aria-hidden="true" />
        <h1 class="m-0 text-title">{{ t('common.boot.failed') }}</h1>
        <p class="m-0 text-muted-foreground">{{ errorMessage(session.bootError) }}</p>
        <Button class="mt-2" @click="reload">{{ t('common.action.retry') }}</Button>
      </div>
    </main>
    <div
      v-else-if="!session.initialized"
      class="flex min-h-dvh items-center justify-center"
      role="status"
    >
      <CairnLogo class="size-9 animate-pulse" />
      <span class="sr-only">{{ t('common.boot.loading') }}</span>
    </div>
    <RouterView v-else />
    <StepUpDialog v-if="session.isAuthenticated" />
    <Toaster
      position="bottom-right"
      :theme="theme.resolved"
      :duration="3000"
      :offset="toastOffset"
      :mobile-offset="toastMobileOffset"
    />
  </TooltipProvider>
</template>
