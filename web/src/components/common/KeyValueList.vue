<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { cn } from '@/lib/utils'

// Rows of "label ... value" (interface spec, section 9), one KeyValueRow
// each, separated by lines. Put it in a flush Card.
const props = defineProps<{ class?: HTMLAttributes['class'] }>()
</script>

<template>
  <dl :class="cn('m-0 divide-y', props.class)">
    <slot />
  </dl>
</template>
