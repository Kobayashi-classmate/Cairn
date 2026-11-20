<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import CairnLogo from '@/components/common/CairnLogo.vue'
import LocaleSelect from '@/components/common/LocaleSelect.vue'
import ThemeSwitcher from '@/components/common/ThemeSwitcher.vue'
import { useSessionStore } from '@/stores/session'

// Sign-in and first-setup pages: language and colour scheme at the top
// right (remembered in this browser only), one centred card, the version
// below it.
withDefaults(
  defineProps<{
    title: string
    subtitle?: string
    /** Hide the corner switcher when the page has its own language field. */
    localeSwitcher?: boolean
  }>(),
  { subtitle: undefined, localeSwitcher: true },
)

const { t } = useI18n()
const session = useSessionStore()

// The address this panel was opened at, so it is clear which server this is.
const address = window.location.hostname
const version = computed(() => session.meta?.version ?? '')
</script>

<template>
  <div class="flex min-h-dvh flex-col bg-background text-foreground">
    <header class="flex flex-wrap items-center justify-end gap-2.5 px-4 py-4 sm:px-6">
      <LocaleSelect v-if="localeSwitcher" size="sm" />
      <ThemeSwitcher icon-only />
    </header>

    <div class="flex flex-1 flex-col items-center justify-center gap-5 px-4 pt-6 pb-16">
      <main class="flex w-full max-w-[400px] flex-col gap-[22px] rounded-xl border border-border bg-card p-6 text-card-foreground sm:p-7">
        <div class="flex items-center gap-2.5">
          <CairnLogo class="size-8 rounded-[9px]" />
          <span class="flex min-w-0 flex-col">
            <span class="text-base leading-tight font-bold tracking-tight">{{ t('common.app.name') }}</span>
            <span v-if="address" class="truncate text-xs text-muted-foreground">{{ address }}</span>
          </span>
        </div>
        <div class="flex flex-col gap-1">
          <h1 class="m-0 text-title tracking-tight">{{ title }}</h1>
          <p v-if="subtitle" class="m-0 text-muted-foreground">{{ subtitle }}</p>
        </div>
        <slot />
      </main>
      <footer v-if="version" class="text-xs text-muted-foreground">
        {{ t('auth.footer', { version }) }}
      </footer>
    </div>
  </div>
</template>
