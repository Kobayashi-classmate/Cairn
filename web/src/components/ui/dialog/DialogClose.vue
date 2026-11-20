<!-- SPDX-License-Identifier: MIT -->
<!-- Based on shadcn-vue; see ../LICENSE. -->
<script setup lang="ts">
import type { DialogCloseProps } from "reka-ui"
import { DialogClose } from "reka-ui"

const props = defineProps<DialogCloseProps>()
</script>

<template>
  <DialogClose
    data-slot="dialog-close"
    v-bind="props"
  >
    <slot />
  </DialogClose>
</template>
