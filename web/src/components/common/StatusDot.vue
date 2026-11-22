<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { cn } from '@/lib/utils'
import { TONE_DOT, type Tone } from '@/lib/tone'

// A coloured dot followed by the state in words ("Running"); the text is
// what screen readers get, the dot only repeats it.
const props = withDefaults(
  defineProps<{
    tone?: Tone
    class?: HTMLAttributes['class']
    /** Let long text (an error message) wrap instead of cutting it off. */
    wrap?: boolean
  }>(),
  { tone: 'neutral', class: undefined, wrap: false },
)
</script>

<template>
  <span :class="cn('inline-flex min-w-0 max-w-full gap-1.5', props.wrap ? 'items-start' : 'items-center', props.class)">
    <span
      :class="
        cn('size-2 shrink-0 rounded-full', TONE_DOT[props.tone], props.tone === 'progress' && 'animate-pulse', props.wrap && 'mt-[calc((1lh-8px)/2)]')
      "
      aria-hidden="true"
    />
    <span :class="props.wrap ? 'min-w-0 [overflow-wrap:anywhere]' : 'min-w-0 truncate'"><slot /></span>
  </span>
</template>
