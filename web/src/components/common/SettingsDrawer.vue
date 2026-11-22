<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts" generic="V extends string">
import { computed, nextTick, ref, watch, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { X } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'
import ErrorState from './ErrorState.vue'
import StatusPill from './StatusPill.vue'
import { keepOpenOnCompanion, keepOpenOnEscape, trackOverlay } from './overlayGuard'
import type { DrawerItem, DrawerStatus } from './types'

// The wide drawer for an object's settings (interface spec, section 6):
// min(1040px, 100%) from the right. The header has an icon, the name, a
// status pill, one line of facts, the common actions and Close; on the left
// a vertical sub-menu, one function per entry, with destructive entries
// (Delete) apart at the bottom; on the right the current entry's blocks.
// On phones it fills the screen and the sub-menu becomes a scrolling row.
// The parent keeps the state in the address (useDrawerRoute).
const props = defineProps<{
  open: boolean
  title: string
  icon?: Component
  status?: DrawerStatus | null
  /** One line under the title ("PHP 8.4 · /srv/www/site"). */
  meta?: string
  items: readonly DrawerItem<V>[]
  /** Destructive entries; selecting one emits `danger` instead of showing a pane. */
  dangerItems?: readonly DrawerItem<string>[]
  /** The entry shown. */
  section: V
  loading?: boolean
  /** Why the object could not be loaded; replaces the content. */
  error?: unknown
}>()

const emit = defineEmits<{
  close: []
  'update:section': [value: V]
  danger: [value: string]
  retry: []
}>()

const { t } = useI18n()
const nav = ref<HTMLElement | null>(null)
trackOverlay(() => props.open)

const current = computed(() => props.items.find((item) => item.value === props.section) ?? props.items[0])

function onOpenChange(open: boolean) {
  if (!open) emit('close')
}

/**
 * Start in the sub-menu rather than on the first header button; Tab then
 * reaches its entries. (The object may still be loading, so no entry is
 * focused yet.)
 */
function onOpenAutoFocus(event: Event) {
  event.preventDefault()
  void nextTick(() => nav.value?.focus())
}

// On phones the sub-menu is a scrolling row: keep the current entry in view.
watch(
  () => [props.section, props.open, props.loading],
  () => {
    void nextTick(() => {
      nav.value?.querySelector('[aria-current="true"]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
    })
  },
)

function select(value: V) {
  emit('update:section', value)
}

const itemClass =
  'inline-flex h-[34px] shrink-0 items-center gap-2 rounded-md px-3 text-start whitespace-nowrap outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 max-md:h-11 md:w-full [&_svg]:size-4 [&_svg]:shrink-0'
</script>

<template>
  <Sheet :open="open" @update:open="onOpenChange">
    <SheetContent
      side="right"
      :show-close-button="false"
      class="h-dvh w-full max-w-none gap-0 bg-background p-0 sm:max-w-[1040px] md:w-[min(1040px,100%)]"
      @open-auto-focus="onOpenAutoFocus"
      @interact-outside="keepOpenOnCompanion"
      @escape-key-down="keepOpenOnEscape"
    >
      <header class="flex flex-wrap items-center gap-x-4 gap-y-3 border-b bg-card px-4 py-3.5 md:px-5">
        <span
          class="order-1 inline-flex size-[38px] shrink-0 items-center justify-center rounded-lg bg-primary-soft text-primary-soft-foreground [&_svg]:size-[18px]"
          aria-hidden="true"
        >
          <component :is="icon" v-if="icon" />
        </span>
        <div class="order-2 flex min-w-0 flex-1 flex-col gap-0.5 md:flex-[1_1_240px]">
          <div class="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
            <SheetTitle class="m-0 min-w-0 truncate text-lg font-semibold tracking-[-0.01em]" dir="auto">
              {{ title }}
            </SheetTitle>
            <StatusPill v-if="status" :tone="status.tone" dot>{{ status.label }}</StatusPill>
          </div>
          <SheetDescription class="m-0 truncate text-detail text-muted-foreground" :title="meta">
            <template v-if="meta">{{ meta }}</template>
            <span v-else class="sr-only">{{ title }}</span>
          </SheetDescription>
        </div>
        <!-- On phones the actions go on a row of their own, under the title and Close. -->
        <div v-if="$slots.actions" class="order-4 flex w-full flex-wrap items-center gap-2 md:order-3 md:w-auto">
          <slot name="actions" />
        </div>
        <Button
          variant="ghost"
          size="icon-lg"
          class="order-3 shrink-0 max-md:size-11 md:order-4"
          :aria-label="t('common.action.close')"
          @click="emit('close')"
        >
          <X class="size-[18px]" aria-hidden="true" />
        </Button>
      </header>

      <div class="flex min-h-0 flex-1 flex-col md:flex-row">
        <nav
          ref="nav"
          tabindex="-1"
          :aria-label="t('common.drawer.sections', { name: title })"
          class="flex shrink-0 gap-0.5 overflow-x-auto border-b bg-card px-2 py-2 outline-none md:w-[232px] md:flex-col md:overflow-x-visible md:overflow-y-auto md:border-e md:border-b-0 md:px-2.5 md:py-3.5"
        >
          <button
            v-for="item in items"
            :key="item.value"
            type="button"
            :aria-current="item.value === current?.value ? 'true' : undefined"
            :class="
              cn(
                itemClass,
                item.value === current?.value
                  ? 'bg-accent font-semibold text-foreground'
                  : 'font-medium text-muted-foreground hover:bg-accent/60 hover:text-foreground',
              )
            "
            @click="select(item.value)"
          >
            <component :is="item.icon" v-if="item.icon" aria-hidden="true" />
            {{ item.label }}
          </button>
          <template v-if="dangerItems && dangerItems.length > 0">
            <span class="mx-1 my-1.5 shrink-0 self-stretch bg-border max-md:w-px md:my-2.5 md:h-px" aria-hidden="true" />
            <button
              v-for="item in dangerItems"
              :key="item.value"
              type="button"
              :class="cn(itemClass, 'font-medium text-destructive hover:bg-destructive-soft')"
              @click="emit('danger', item.value)"
            >
              <component :is="item.icon" v-if="item.icon" aria-hidden="true" />
              {{ item.label }}
            </button>
          </template>
        </nav>

        <!-- Focusable so the keyboard can scroll a pane that has no controls. -->
        <div class="min-h-0 min-w-0 flex-1 overflow-y-auto focus-visible:-outline-offset-2" tabindex="0" role="region" :aria-label="current?.label">
          <div class="flex flex-col gap-4 px-4 pt-5 pb-10 md:px-6">
            <ErrorState v-if="error" :error="error" @retry="emit('retry')" />
            <template v-else-if="loading">
              <Skeleton class="h-6 w-40" />
              <Skeleton class="h-40 w-full rounded-lg" />
              <Skeleton class="h-28 w-full rounded-lg" />
            </template>
            <template v-else>
              <h2 v-if="current" class="m-0 text-base font-semibold">{{ current.label }}</h2>
              <slot />
            </template>
          </div>
        </div>
      </div>
    </SheetContent>
  </Sheet>
</template>
