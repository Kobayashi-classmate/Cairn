<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import PageHeader from '@/components/common/PageHeader.vue'
import { cn } from '@/lib/utils'
import { useSessionStore } from '@/stores/session'
import { SETTINGS_SECTIONS, type SettingsGroup, type SettingsSection } from './settings/sections'

// The panel's own settings ("settings" skeleton, interface spec section 6):
// categories on the left, one child route each (/settings/<section>), and
// the chosen category's cards on the right. On phones the categories are a
// scrolling row above the content.
const { t } = useI18n()
const route = useRoute()
const session = useSessionStore()
const nav = ref<HTMLElement | null>(null)

const sections = computed(() => SETTINGS_SECTIONS.filter((s) => !s.permission || session.can(s.permission)))

const groups = computed(() => {
  const order: SettingsGroup[] = ['personal', 'panel']
  return order
    .map((group) => ({ group, items: sections.value.filter((s) => s.group === group) }))
    .filter((entry) => entry.items.length > 0)
})

const current = computed<SettingsSection | undefined>(() => sections.value.find((s) => s.name === route.name))

// On phones the row scrolls: keep the current category in view.
watch(
  () => route.name,
  () => {
    void nextTick(() => {
      nav.value?.querySelector('[aria-current="page"]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
    })
  },
  { immediate: true },
)

const itemClass =
  'inline-flex h-[34px] shrink-0 items-center gap-2 rounded-md px-3 whitespace-nowrap no-underline outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 max-md:h-11 md:w-full [&_svg]:size-4 [&_svg]:shrink-0'
</script>

<template>
  <div>
    <PageHeader :title="t('settings.title')" />

    <div class="flex flex-col gap-5 md:flex-row md:items-start md:gap-6 lg:gap-8">
      <nav
        ref="nav"
        :aria-label="t('settings.navLabel')"
        class="-mx-4 flex shrink-0 gap-0.5 overflow-x-auto border-b px-4 pb-2 [scrollbar-width:thin] md:sticky md:top-6 md:mx-0 md:w-48 md:flex-col md:gap-4 md:overflow-visible md:border-b-0 md:px-0 md:pb-0 lg:top-8 lg:w-[220px]"
      >
        <div v-for="entry in groups" :key="entry.group" class="flex shrink-0 gap-0.5 md:flex-col">
          <h2 class="m-0 hidden px-3 pb-1 text-caption text-muted-foreground md:block">
            {{ t(`settings.group.${entry.group}`) }}
          </h2>
          <RouterLink
            v-for="item in entry.items"
            :key="item.name"
            v-slot="{ href, navigate, isExactActive }"
            :to="{ name: item.name }"
            custom
          >
            <a
              :href="href"
              :aria-current="isExactActive ? 'page' : undefined"
              :class="
                cn(
                  itemClass,
                  isExactActive
                    ? 'bg-accent font-semibold text-foreground'
                    : 'font-medium text-muted-foreground hover:bg-accent/60 hover:text-foreground',
                )
              "
              @click="navigate"
            >
              <component :is="item.icon" aria-hidden="true" />
              {{ t(`settings.nav.${item.key}`) }}
            </a>
          </RouterLink>
        </div>
      </nav>

      <div class="flex min-w-0 flex-1 flex-col gap-5">
        <h2 v-if="current" class="m-0 text-section">{{ t(`settings.nav.${current.key}`) }}</h2>
        <RouterView />
      </div>
    </div>
  </div>
</template>
