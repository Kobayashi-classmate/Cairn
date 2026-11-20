<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { cn } from '@/lib/utils'

// The mark: three stacked stones on an accent tile. Decorative; the product
// name is always written next to it or given as accessible text.
defineProps<{ class?: HTMLAttributes['class'] }>()
</script>

<template>
  <span
    aria-hidden="true"
    :class="cn('inline-flex size-7 shrink-0 items-center justify-center rounded-md bg-primary text-primary-foreground', $props.class)"
  >
    <svg viewBox="0 0 24 24" class="size-[64%]" fill="currentColor">
      <rect x="9" y="4" width="6" height="4" rx="2" />
      <rect x="7" y="10" width="10" height="4" rx="2" />
      <rect x="4.5" y="16" width="15" height="4" rx="2" />
    </svg>
  </span>
</template>
