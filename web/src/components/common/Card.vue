<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, type HTMLAttributes } from 'vue'
import { cn } from '@/lib/utils'

// A bordered card (interface spec, sections 1 and 9): an optional title row
// with actions on the right, then the content. `flush` drops the padding
// for lists and tables that run to the edges.
const props = withDefaults(
  defineProps<{
    title?: string
    description?: string
    /** Heading level of the title: 2 on pages, 3 inside drawers. */
    level?: 2 | 3
    flush?: boolean
    class?: HTMLAttributes['class']
    bodyClass?: HTMLAttributes['class']
  }>(),
  { title: undefined, description: undefined, level: 2, class: undefined, bodyClass: undefined },
)

const heading = computed(() => `h${props.level}`)
</script>

<template>
  <section :class="cn('min-w-0 overflow-hidden rounded-lg border bg-card text-card-foreground', props.class)">
    <header
      v-if="title || $slots.title || $slots.actions"
      class="flex min-h-12 flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b px-4 py-2.5 md:px-[18px]"
    >
      <div v-if="title || $slots.title" class="flex min-w-0 flex-col">
        <component :is="heading" class="m-0 text-sm font-semibold">
          <slot name="title">{{ title }}</slot>
        </component>
        <p v-if="description" class="m-0 text-xs text-muted-foreground">{{ description }}</p>
      </div>
      <div v-if="$slots.actions" class="flex shrink-0 flex-wrap items-center gap-2 [&:only-child]:flex-1 [&:only-child]:justify-between">
        <slot name="actions" />
      </div>
    </header>
    <div :class="cn(flush ? '' : 'px-4 py-4 md:px-[18px]', props.bodyClass)">
      <slot />
    </div>
    <footer v-if="$slots.footer" class="flex flex-wrap items-center gap-2 border-t px-4 py-3 md:px-[18px]">
      <slot name="footer" />
    </footer>
  </section>
</template>
