<!-- SPDX-License-Identifier: MIT -->
<!-- Based on shadcn-vue; see ../LICENSE. -->
<script setup lang="ts">
import type { HTMLAttributes } from "vue"
import { cn } from "@/lib/utils"

const props = defineProps<{
  class?: HTMLAttributes["class"]
}>()
</script>

<template>
  <kbd
    :class="cn(
      'border bg-card text-muted-foreground pointer-events-none inline-flex h-5 w-fit min-w-5 items-center justify-center gap-1 rounded-sm px-1 font-sans text-[11px] leading-none font-medium select-none',
      `[&_svg:not([class*='size-'])]:size-3`,
      props.class,
    )"
  >
    <slot />
  </kbd>
</template>
