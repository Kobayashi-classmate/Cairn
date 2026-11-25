<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts" generic="V extends string">
import { RouterLink, type RouteLocationRaw } from 'vue-router'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'
import type { PageTab } from './types'

// The top of a page (interface spec, sections 1 and 6): the title, one
// sentence about how things stand, at most one primary button with any
// secondary ones (the `actions` slot), and the page's underline tabs with
// counts. The tabs are links that change `?tab=` (see usePageTab).
defineProps<{
  title: string
  /** One sentence on the current state ("4 sites · nginx running"). */
  status?: string
  /** Show a placeholder while the status is not known yet. */
  statusLoading?: boolean
  tabs?: readonly PageTab<V>[]
  /** The selected tab. */
  tab?: V
  /** Builds a tab's link (usePageTab's tabLocation). */
  tabLocation?: (value: V) => RouteLocationRaw
}>()
</script>

<template>
  <header class="mb-5 flex flex-col gap-4 md:mb-6">
    <div class="flex flex-wrap items-end justify-between gap-x-4 gap-y-3">
      <div class="flex min-w-0 flex-col gap-1">
        <h1 class="m-0 text-page-title">{{ title }}</h1>
        <Skeleton v-if="statusLoading && !status" class="h-4 w-56" />
        <p v-else-if="status || $slots.status" class="m-0 text-muted-foreground">
          <slot name="status">{{ status }}</slot>
        </p>
      </div>
      <div v-if="$slots.actions" class="flex flex-wrap items-center gap-2">
        <slot name="actions" />
      </div>
    </div>
    <!--
      The baseline is an inset shadow rather than a border: the selected
      tab's underline is drawn over it, and nothing reaches below the box,
      which would give the bar a scroll bar of its own.
    -->
    <nav
      v-if="tabs && tabs.length > 0 && tabLocation"
      :aria-label="title"
      class="flex gap-1 overflow-x-auto overflow-y-hidden shadow-[inset_0_-1px_0_var(--border)] [scrollbar-width:thin]"
    >
      <RouterLink
        v-for="item in tabs"
        :key="item.value"
        v-slot="{ href, navigate }"
        :to="tabLocation(item.value)"
        custom
      >
        <a
          :href="href"
          :aria-current="item.value === tab ? 'page' : undefined"
          :class="
            cn(
              'inline-flex h-10 shrink-0 items-center gap-1.5 rounded-t-sm border-b-2 px-3 font-medium whitespace-nowrap no-underline outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset max-md:h-11',
              item.value === tab
                ? 'border-primary font-semibold text-foreground'
                : 'border-transparent text-muted-foreground hover:text-foreground',
            )
          "
          @click="navigate"
        >
          {{ item.label }}
          <span
            v-if="item.count !== undefined"
            :class="
              cn(
                'rounded-full px-1.5 text-caption tabular-nums',
                item.value === tab ? 'bg-primary-soft text-primary-soft-foreground' : 'bg-muted text-muted-foreground',
              )
            "
          >
            {{ item.count }}
          </span>
        </a>
      </RouterLink>
    </nav>
  </header>
</template>
