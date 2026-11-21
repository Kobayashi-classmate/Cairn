<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { RadioGroupIndicator, RadioGroupItem, RadioGroupRoot, type AcceptableValue } from 'reka-ui'
import { cn } from '@/lib/utils'
import type { OptionItem } from './types'

// Radio cards (interface spec, section 9 "OptionCard"): versions, site types,
// validation methods. Arrow keys move between the choices.
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  options: readonly OptionItem[]
  /** Names the group when no visible label points at it. */
  label?: string
  disabled?: boolean
  /** Lay the cards out in columns from this width up. */
  grid?: boolean
  class?: HTMLAttributes['class']
}>()

const model = defineModel<string>({ required: true })

function onUpdate(value: AcceptableValue) {
  if (typeof value === 'string') model.value = value
}
</script>

<template>
  <RadioGroupRoot
    v-bind="$attrs"
    :model-value="model"
    :disabled="disabled"
    :aria-label="label"
    :class="cn(grid ? 'grid gap-2 sm:grid-cols-2' : 'flex flex-col gap-2', props.class)"
    @update:model-value="onUpdate"
  >
    <RadioGroupItem
      v-for="option in options"
      :key="option.value"
      :value="option.value"
      :disabled="option.disabled"
      class="group flex w-full items-start gap-2.5 rounded-md border bg-card px-3 py-2.5 text-start outline-none transition-colors hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:border-primary data-[state=checked]:bg-primary-soft"
    >
      <span
        class="mt-[3px] inline-flex size-4 shrink-0 items-center justify-center rounded-full border-[1.5px] border-muted-foreground group-data-[state=checked]:border-primary"
        aria-hidden="true"
      >
        <RadioGroupIndicator class="size-2 rounded-full bg-primary" />
      </span>
      <span class="flex min-w-0 flex-col">
        <span class="font-medium">{{ option.label }}</span>
        <span v-if="option.hint" class="text-detail text-muted-foreground">{{ option.hint }}</span>
      </span>
    </RadioGroupItem>
  </RadioGroupRoot>
</template>
