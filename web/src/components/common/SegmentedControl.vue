<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts" generic="T extends string">
import type { HTMLAttributes } from 'vue'
import { ToggleGroupItem, ToggleGroupRoot } from 'reka-ui'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import type { SegmentedOption } from './segmented'

// A row of mutually exclusive choices (filters, theme switch); exactly one
// is always selected. Built on Reka's ToggleGroup, so arrow keys move between
// the choices.
const props = defineProps<{
  modelValue: T
  options: readonly SegmentedOption<T>[]
  /** Names the whole control for screen readers. */
  label: string
  /** Show only the icons; labels move to tooltips and accessible names. */
  iconOnly?: boolean
  /** Stack the choices (icon rail); arrow keys follow the direction. */
  vertical?: boolean
  disabled?: boolean
  class?: HTMLAttributes['class']
}>()

const emit = defineEmits<{ 'update:modelValue': [value: T] }>()

function onUpdate(value: unknown) {
  // Clicking the selected choice again would clear it; keep it selected.
  if (typeof value !== 'string' || value === '' || value === props.modelValue) return
  const option = props.options.find((o) => o.value === value)
  if (option) emit('update:modelValue', option.value)
}

const itemClass =
  'inline-flex h-7 min-w-0 flex-auto items-center max-md:h-11 justify-center gap-1 rounded-sm px-1.5 text-caption text-muted-foreground transition-colors outline-none select-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 data-[state=on]:bg-card data-[state=on]:text-foreground data-[state=on]:shadow-raise dark:data-[state=on]:bg-track [&_svg]:size-3.5 [&_svg]:shrink-0'
</script>

<template>
  <ToggleGroupRoot
    type="single"
    :model-value="modelValue"
    :disabled="disabled"
    :aria-label="label"
    :orientation="vertical ? 'vertical' : 'horizontal'"
    :class="cn('flex items-center gap-0.5 rounded-md bg-muted p-[3px]', vertical && 'flex-col', props.class)"
    @update:model-value="onUpdate"
  >
    <template v-for="option in options" :key="option.value">
      <Tooltip v-if="iconOnly">
        <TooltipTrigger as-child>
          <ToggleGroupItem
            :value="option.value"
            :aria-label="option.label"
            :class="cn(itemClass, 'h-[26px] w-[30px] flex-none px-0 max-md:size-11')"
          >
            <component :is="option.icon" v-if="option.icon" aria-hidden="true" />
          </ToggleGroupItem>
        </TooltipTrigger>
        <TooltipContent :side="vertical ? 'right' : 'top'">{{ option.label }}</TooltipContent>
      </Tooltip>
      <ToggleGroupItem v-else :value="option.value" :class="itemClass">
        <component :is="option.icon" v-if="option.icon" aria-hidden="true" />
        <span class="truncate">{{ option.label }}</span>
      </ToggleGroupItem>
    </template>
  </ToggleGroupRoot>
</template>
