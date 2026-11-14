<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { NTag } from 'naive-ui'
import { jobStateLabel, jobStateTagType } from '@/jobs/labels'

const props = defineProps<{ state: string }>()

const type = computed(() => jobStateTagType(props.state))
</script>

<template>
  <NTag size="small" :bordered="false" :type="type">{{ jobStateLabel(state) }}</NTag>
</template>
