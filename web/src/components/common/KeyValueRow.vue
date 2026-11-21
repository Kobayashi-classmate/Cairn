<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { cn } from '@/lib/utils'

// One row of a KeyValueList. Paths, ports and versions are `mono`.
const props = defineProps<{
  label: string
  mono?: boolean
  class?: HTMLAttributes['class']
}>()
</script>

<template>
  <div
    :class="
      cn(
        'flex flex-col gap-0.5 px-4 py-2.5 sm:flex-row sm:items-center sm:justify-between sm:gap-6 md:px-[18px]',
        props.class,
      )
    "
  >
    <dt class="shrink-0 text-muted-foreground">{{ label }}</dt>
    <dd
      :class="
        cn(
          'm-0 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 [overflow-wrap:anywhere] sm:justify-end sm:text-end',
          mono && 'font-mono text-[12.5px]',
        )
      "
    >
      <slot />
    </dd>
  </div>
</template>
