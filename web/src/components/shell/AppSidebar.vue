<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, type RouteLocationRaw } from 'vue-router'
import { LogOut, Search } from '@lucide/vue'
import CairnLogo from '@/components/common/CairnLogo.vue'
import ThemeSwitcher from '@/components/common/ThemeSwitcher.vue'
import { Button } from '@/components/ui/button'
import { Kbd } from '@/components/ui/kbd'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useSignOut } from '@/composables/useSignOut'
import { sidebarSections, type NavItem } from '@/layouts/navigation'
import { cn } from '@/lib/utils'
import ElevationIndicator from './ElevationIndicator.vue'
import ServerCard from './ServerCard.vue'
import UserMenu from './UserMenu.vue'

// Sidebar contents (interface spec, section 6): logo, server card, search,
// the grouped menu, then the theme switch and the account row. "rail" is the
// 64px icon-only form used on tablets; labels move into tooltips.
const props = defineProps<{
  items: readonly NavItem[]
  home: RouteLocationRaw
  rail?: boolean
  /** Shortcut hint shown on the search button, e.g. "Ctrl K". */
  shortcut: string
}>()

const emit = defineEmits<{ search: [] }>()

const { t, te } = useI18n()
const route = useRoute()
const { signingOut, signOut } = useSignOut()

const sections = computed(() => sidebarSections(props.items))
const hasMessages = computed(() => props.items.some((item) => item.key === 'messages'))

function label(item: NavItem): string {
  // A label key this build lacks shows the entry's key rather than the raw i18n key.
  return te(item.labelKey) ? t(item.labelKey) : item.key
}

function isActive(item: NavItem): boolean {
  return route.meta.navKey === item.key
}

const itemClass =
  'flex h-[34px] items-center gap-2.5 rounded-[7px] px-2.5 font-medium max-md:h-11 text-sidebar-foreground outline-none transition-colors hover:bg-sidebar-accent/70 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 [&_svg]:size-4 [&_svg]:shrink-0'
const activeClass = 'bg-sidebar-accent font-semibold text-sidebar-accent-foreground shadow-raise hover:bg-sidebar-accent hover:text-sidebar-accent-foreground'
</script>

<template>
  <div :class="cn('flex h-full flex-col bg-sidebar text-sidebar-foreground', props.rail ? 'items-center px-2 py-3' : 'px-3 py-4')">
    <!-- Logo -->
    <!-- custom: the logo is not marked as the current page; the menu entry is. -->
    <RouterLink v-slot="{ href, navigate }" :to="home" custom>
      <a
        :href="href"
        :aria-label="props.rail ? t('common.app.name') : undefined"
        :class="cn('flex items-center gap-2.5 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring/50', props.rail ? 'p-1.5' : 'px-2 pt-1 pb-3')"
        @click="navigate"
      >
        <CairnLogo />
        <span v-if="!props.rail" class="text-base font-bold tracking-tight text-foreground">{{ t('common.app.name') }}</span>
      </a>
    </RouterLink>

    <!-- Server and search -->
    <div v-if="!props.rail" class="flex flex-col gap-2 pb-2">
      <ServerCard />
      <button
        type="button"
        class="flex h-[34px] items-center gap-2 rounded-md border border-sidebar-border px-2.5 text-detail text-sidebar-muted max-md:h-11 outline-none transition-colors hover:bg-sidebar-accent/70 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
        aria-keyshortcuts="Control+K Meta+K"
        @click="emit('search')"
      >
        <Search class="size-[15px] shrink-0" aria-hidden="true" />
        <span class="flex-1 truncate text-start">{{ t('shell.search') }}</span>
        <Kbd aria-hidden="true">{{ shortcut }}</Kbd>
      </button>
    </div>
    <Tooltip v-else>
      <TooltipTrigger as-child>
        <Button
          variant="ghost"
          size="icon-lg"
          class="my-2 text-sidebar-muted hover:bg-sidebar-accent"
          :aria-label="t('shell.search')"
          aria-keyshortcuts="Control+K Meta+K"
          @click="emit('search')"
        >
          <Search aria-hidden="true" />
        </Button>
      </TooltipTrigger>
      <TooltipContent side="right" class="flex items-center gap-2">
        {{ t('shell.search') }}
        <Kbd>{{ shortcut }}</Kbd>
      </TooltipContent>
    </Tooltip>

    <!-- Menu -->
    <nav
      :aria-label="t('shell.mainNav')"
      :class="cn('-mx-1 flex min-h-0 flex-1 flex-col overflow-y-auto px-1 pb-2', props.rail && 'w-full items-center')"
    >
      <div
        v-for="(section, index) in sections"
        :key="section.group || 'top'"
        :class="cn('flex w-full flex-col gap-0.5', index > 0 && (props.rail ? 'mt-2 border-t border-sidebar-border pt-2' : 'pt-3'))"
      >
        <h2
          v-if="section.group && !props.rail"
          :id="`nav-group-${section.group}`"
          class="m-0 px-2.5 py-1 text-caption text-sidebar-muted"
        >
          {{ t(`shell.group.${section.group}`) }}
        </h2>
        <ul
          class="m-0 flex list-none flex-col gap-0.5 p-0"
          :aria-labelledby="section.group && !props.rail ? `nav-group-${section.group}` : undefined"
          :aria-label="section.group && props.rail ? t(`shell.group.${section.group}`) : undefined"
        >
          <li v-for="item in section.items" :key="item.key" :class="props.rail ? 'flex justify-center' : undefined">
            <Tooltip v-if="props.rail">
              <TooltipTrigger as-child>
                <RouterLink
                  :to="item.to"
                  :aria-label="label(item)"
                  :aria-current="isActive(item) ? 'page' : undefined"
                  :class="cn(itemClass, 'size-10 justify-center px-0', isActive(item) && activeClass)"
                >
                  <component :is="item.icon" :stroke-width="1.8" aria-hidden="true" />
                </RouterLink>
              </TooltipTrigger>
              <TooltipContent side="right">{{ label(item) }}</TooltipContent>
            </Tooltip>
            <RouterLink
              v-else
              :to="item.to"
              :aria-current="isActive(item) ? 'page' : undefined"
              :class="cn(itemClass, isActive(item) && activeClass)"
            >
              <component :is="item.icon" :stroke-width="1.8" aria-hidden="true" />
              <span class="truncate">{{ label(item) }}</span>
            </RouterLink>
          </li>
        </ul>
      </div>
    </nav>

    <!-- Theme and account -->
    <div
      :class="
        cn(
          'flex shrink-0 flex-col gap-3 border-t border-sidebar-border pt-3',
          props.rail ? 'w-full items-center' : 'px-1',
        )
      "
    >
      <ThemeSwitcher
        :icon-only="props.rail"
        :vertical="props.rail"
        class="bg-sidebar-border dark:bg-muted"
      />
      <ElevationIndicator v-if="props.rail" compact />
      <div :class="cn('flex items-center gap-1', props.rail && 'justify-center')">
        <UserMenu :compact="props.rail" :has-messages="hasMessages" />
        <template v-if="!props.rail">
          <ElevationIndicator compact />
          <Tooltip>
            <TooltipTrigger as-child>
              <Button
                variant="ghost"
                size="icon"
                class="shrink-0 text-sidebar-muted hover:bg-sidebar-accent max-md:size-11"
                :aria-label="t('common.action.signOut')"
                :disabled="signingOut"
                @click="signOut"
              >
                <LogOut :stroke-width="1.8" aria-hidden="true" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>{{ t('common.action.signOut') }}</TooltipContent>
          </Tooltip>
        </template>
      </div>
    </div>
  </div>
</template>
