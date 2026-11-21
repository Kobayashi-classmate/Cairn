<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { Component, HTMLAttributes } from 'vue'
import { Inbox } from '@lucide/vue'
import { cn } from '@/lib/utils'

// Nothing to show yet (interface spec, section 7): one sentence and the main
// action in the `actions` slot.
const props = defineProps<{
  title: string
  text?: string
  icon?: Component
  /** Less padding, for cards. */
  compact?: boolean
  class?: HTMLAttributes['class']
}>()
</script>

<template>
  <div
    :class="
      cn(
        'flex flex-col items-center gap-2 px-4 text-center',
        compact ? 'py-6' : 'py-12',
        props.class,
      )
    "
  >
    <component :is="icon ?? Inbox" class="size-6 text-muted-foreground" aria-hidden="true" />
    <p class="m-0 font-medium">{{ title }}</p>
    <p v-if="text" class="m-0 max-w-[520px] text-detail text-muted-foreground">{{ text }}</p>
    <div v-if="$slots.actions" class="mt-2 flex flex-wrap items-center justify-center gap-2">
      <slot name="actions" />
    </div>
  </div>
</template>
