<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Shows that a firewall change is being confirmed, with the time left.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NProgress } from 'naive-ui'

const props = defineProps<{ remaining: number; total?: number }>()

const { t } = useI18n()
const percent = computed(() => Math.max(0, Math.min(100, (props.remaining / (props.total ?? 60)) * 100)))
</script>

<template>
  <NAlert type="info" :show-icon="false" class="confirm-bar" role="status">
    <div class="confirm-bar__text">{{ t('firewall.confirm.checking', { seconds: remaining }) }}</div>
    <NProgress type="line" :percentage="percent" :show-indicator="false" :height="4" processing />
  </NAlert>
</template>

<style scoped>
.confirm-bar {
  margin-block-end: 12px;
}

.confirm-bar__text {
  margin-block-end: 6px;
}
</style>
