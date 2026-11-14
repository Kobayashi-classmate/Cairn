<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NTag } from 'naive-ui'

/** Severity of a notification or official message: info, warning or critical. */
const props = defineProps<{ level: string }>()

const { t, te } = useI18n()

const type = computed(() =>
  props.level === 'critical' ? 'error' : props.level === 'warning' ? 'warning' : 'info',
)
const label = computed(() => {
  const key = `notify.level.${props.level}`
  return te(key) ? t(key) : props.level
})
</script>

<template>
  <NTag size="small" :bordered="false" :type="type">{{ label }}</NTag>
</template>
