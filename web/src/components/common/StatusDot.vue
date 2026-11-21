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
  }>(),
  { tone: 'neutral', class: undefined },
)
</script>

<template>
  <span :class="cn('inline-flex min-w-0 items-center gap-1.5', props.class)">
    <span
      :class="cn('size-2 shrink-0 rounded-full', TONE_DOT[props.tone], props.tone === 'progress' && 'animate-pulse')"
      aria-hidden="true"
    />
    <span class="min-w-0 truncate"><slot /></span>
  </span>
</template>
