<!-- SPDX-License-Identifier: MIT -->
<!-- Based on shadcn-vue; see ../LICENSE. -->
<script setup lang="ts">
import type { PrimitiveProps } from "reka-ui"
import type { HTMLAttributes } from "vue"
import type { ButtonVariants } from "."
import { ref, watch } from "vue"
import { cn } from "@/lib/utils"
import { Primitive } from "reka-ui"
import { buttonVariants } from "."

interface Props extends PrimitiveProps {
  variant?: ButtonVariants["variant"]
  size?: ButtonVariants["size"]
  class?: HTMLAttributes["class"]
  disabled?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  as: "button",
  disabled: false,
})

// Cairn: a focused button that turns disabled while its action runs would
// drop keyboard focus to the page body (and out of an open dialog). It stays
// focusable, marked aria-disabled and ignoring clicks, until focus leaves it.
const root = ref<{ $el?: unknown } | null>(null)
const holding = ref(false)

watch(
  () => props.disabled,
  (disabled) => {
    const el = root.value?.$el
    holding.value = disabled && el instanceof HTMLElement && el === document.activeElement
  },
)

function onFocusout() {
  holding.value = false
}

function blockWhileHolding(event: Event) {
  if (!holding.value) return
  event.preventDefault()
  event.stopImmediatePropagation()
}
</script>

<template>
  <Primitive
    ref="root"
    data-slot="button"
    :data-variant="variant"
    :data-size="size"
    :as="as"
    :as-child="asChild"
    :disabled="(props.disabled && !holding) || undefined"
    :aria-disabled="holding || undefined"
    :class="cn(buttonVariants({ variant, size }), props.class)"
    @click.capture="blockWhileHolding"
    @focusout="onFocusout"
  >
    <slot />
  </Primitive>
</template>
