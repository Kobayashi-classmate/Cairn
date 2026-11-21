<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Skeleton } from '@/components/ui/skeleton'

// Placeholder rows of a list while it loads (interface spec, section 7): the
// layout keeps its shape instead of showing a spinner.
withDefaults(
  defineProps<{
    rows?: number
    /** Two lines per row (title and detail). */
    twoLine?: boolean
  }>(),
  { rows: 4 },
)

const { t } = useI18n()
</script>

<template>
  <div role="status" aria-busy="true" class="divide-y">
    <span class="sr-only">{{ t('common.state.loading') }}</span>
    <div v-for="row in rows" :key="row" class="flex items-center gap-3 px-4 py-3 md:px-[18px]" aria-hidden="true">
      <Skeleton class="size-2 rounded-full" />
      <div class="flex flex-1 flex-col gap-1.5">
        <Skeleton class="h-3.5" :style="{ width: `${60 - ((row * 13) % 25)}%` }" />
        <Skeleton v-if="twoLine" class="h-3 w-1/3" />
      </div>
      <Skeleton class="h-5 w-16 rounded-full" />
    </div>
  </div>
</template>
