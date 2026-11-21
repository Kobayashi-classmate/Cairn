<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { cn } from '@/lib/utils'
import { TONE_SOFT, type Tone } from '@/lib/tone'

// A rounded label on the soft background of its tone ("Valid · 89 days left").
const props = withDefaults(
  defineProps<{
    tone?: Tone
    /** Lead with a small dot (headers of drawers). */
    dot?: boolean
    class?: HTMLAttributes['class']
  }>(),
  { tone: 'neutral', class: undefined },
)
</script>

<template>
  <span
    :class="
      cn(
        'inline-flex w-fit max-w-full shrink-0 items-center gap-1.5 rounded-full px-2 py-px text-caption whitespace-nowrap',
        TONE_SOFT[props.tone],
        props.class,
      )
    "
  >
    <span v-if="dot" class="size-1.5 shrink-0 rounded-full bg-current" aria-hidden="true" />
    <span class="truncate"><slot /></span>
  </span>
</template>
