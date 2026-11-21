<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useClock } from '@/composables/useClock'
import { formatFullDateTime, formatWhen } from '@/i18n/format'

// A time as people say it ("3 hours ago", "today 02:00"); hovering shows
// the full date and time with the time zone (interface spec, section 8).
const props = defineProps<{
  /** Unix seconds. */
  value: number
}>()

const now = useClock()
const text = computed(() => formatWhen(props.value, now.value))
const full = computed(() => formatFullDateTime(props.value))
const iso = computed(() => new Date(props.value * 1000).toISOString())
</script>

<template>
  <time :datetime="iso" :title="full" class="whitespace-nowrap">{{ text }}</time>
</template>
