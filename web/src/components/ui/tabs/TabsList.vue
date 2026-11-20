<!-- SPDX-License-Identifier: MIT -->
<!-- Based on shadcn-vue; see ../LICENSE. -->
<script setup lang="ts">
import type { TabsListProps } from "reka-ui"
import type { HTMLAttributes } from "vue"
import { reactiveOmit } from "@vueuse/core"
import { cn } from "@/lib/utils"
import { TabsList } from "reka-ui"

const props = defineProps<TabsListProps & { class?: HTMLAttributes["class"] }>()

const delegatedProps = reactiveOmit(props, "class")
</script>

<template>
  <TabsList
    data-slot="tabs-list"
    v-bind="delegatedProps"
    :class="cn(
      'flex w-full items-end gap-1 overflow-x-auto border-b border-border text-muted-foreground',
      props.class,
    )"
  >
    <slot />
  </TabsList>
</template>
