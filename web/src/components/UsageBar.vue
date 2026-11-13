<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { NProgress, NText } from 'naive-ui'
import { formatPercent } from '@/i18n/format'

const props = withDefaults(
  defineProps<{
    /** Used share on a 0–100 scale. */
    percent: number
    /** Text under the bar, for example "2 GiB of 8 GiB". */
    detail?: string
    /** Repeat the percentage under the bar; off when the card already shows it. */
    showPercent?: boolean
  }>(),
  { detail: undefined, showPercent: true },
)

const clamped = computed(() => Math.min(100, Math.max(0, props.percent)))

// Colour the bar by how close the resource is to running out.
const status = computed(() =>
  clamped.value >= 90 ? 'error' : clamped.value >= 75 ? 'warning' : 'success',
)
</script>

<template>
  <div class="usage-bar">
    <NProgress
      type="line"
      :percentage="clamped"
      :status="status"
      :show-indicator="false"
      :height="8"
      :aria-valuetext="formatPercent(clamped)"
    />
    <div class="usage-bar__text">
      <NText depth="3">{{ detail }}</NText>
      <NText v-if="showPercent" strong>{{ formatPercent(clamped) }}</NText>
    </div>
  </div>
</template>

<style scoped>
.usage-bar__text {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  margin-block-start: 4px;
  font-size: 13px;
}
</style>
