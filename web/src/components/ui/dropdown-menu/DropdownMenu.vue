<!-- SPDX-License-Identifier: MIT -->
<!-- Based on shadcn-vue; see ../LICENSE. -->
<script setup lang="ts">
import type { DropdownMenuRootEmits, DropdownMenuRootProps } from "reka-ui"
import { DropdownMenuRoot, useForwardPropsEmits } from "reka-ui"

// Cairn: menus are not modal (as in the WAI-ARIA menu button pattern). A
// modal menu hides the rest of the page from screen readers with
// aria-hidden while its links and buttons stay in the tab order.
const props = withDefaults(defineProps<DropdownMenuRootProps>(), { modal: false })
const emits = defineEmits<DropdownMenuRootEmits>()

const forwarded = useForwardPropsEmits(props, emits)
</script>

<template>
  <DropdownMenuRoot
    v-slot="slotProps"
    data-slot="dropdown-menu"
    v-bind="forwarded"
  >
    <slot v-bind="slotProps" />
  </DropdownMenuRoot>
</template>
