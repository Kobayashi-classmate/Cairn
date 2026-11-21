<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { Skeleton } from '@/components/ui/skeleton'
import { formatPercent } from '@/i18n/format'
import { cn } from '@/lib/utils'
import { TONE_DOT, usageTone } from '@/lib/tone'

// A metric (interface spec, section 9): label, value, one line of detail and
// a usage bar coloured by the 70 / 85 % thresholds. The `extra` slot takes
// the place of the bar (a small bar chart, for example).
const props = defineProps<{
  label: string
  value?: string
  caption?: string
  /** Used share on a 0-100 scale; draws the usage bar. */
  percent?: number | null
  loading?: boolean
}>()

const clamped = computed(() =>
  props.percent === undefined || props.percent === null ? null : Math.min(100, Math.max(0, props.percent)),
)
const barClass = computed(() => (clamped.value === null ? '' : TONE_DOT[usageTone(clamped.value)]))
</script>

<template>
  <section class="flex min-w-0 flex-col gap-1.5 rounded-lg border bg-card px-4 py-4 md:px-[18px]">
    <h2 class="m-0 text-detail font-medium text-muted-foreground">{{ label }}</h2>
    <template v-if="loading">
      <Skeleton class="h-8 w-24" />
      <Skeleton class="h-4 w-40" />
      <Skeleton class="mt-1 h-1.5 w-full" />
    </template>
    <template v-else>
      <span class="text-metric tabular-nums">{{ value }}</span>
      <span v-if="caption" class="truncate text-xs text-muted-foreground tabular-nums" :title="caption">
        {{ caption }}
      </span>
      <div
        v-if="clamped !== null"
        role="meter"
        :aria-label="label"
        aria-valuemin="0"
        aria-valuemax="100"
        :aria-valuenow="Math.round(clamped)"
        :aria-valuetext="formatPercent(clamped)"
        class="mt-1 h-1.5 overflow-hidden rounded-full bg-track"
      >
        <div :class="cn('h-full rounded-full transition-[width] duration-200', barClass)" :style="{ width: `${clamped}%` }" />
      </div>
      <slot name="extra" />
    </template>
  </section>
</template>
