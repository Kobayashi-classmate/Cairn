<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, type HTMLAttributes } from 'vue'
import type { AcceptableValue } from 'reka-ui'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { cn } from '@/lib/utils'
import type { SelectOption } from './types'

// A drop-down with a fixed list of choices. The id and aria attributes from
// FormField go to the trigger button, which the field's <label> names.
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  options: readonly SelectOption[]
  placeholder?: string
  disabled?: boolean
  class?: HTMLAttributes['class']
}>()

const model = defineModel<string>({ required: true })

/** Reka treats "" as "nothing selected"; an empty choice uses this value instead. */
const EMPTY = '\u0000empty'

const inner = computed(() => (model.value === '' ? EMPTY : model.value))
const current = computed(() => props.options.find((option) => option.value === model.value))

function onUpdate(value: AcceptableValue) {
  if (typeof value !== 'string') return
  model.value = value === EMPTY ? '' : value
}
</script>

<template>
  <Select :model-value="inner" :disabled="disabled" @update:model-value="onUpdate">
    <SelectTrigger v-bind="$attrs" :class="cn('w-full', props.class)">
      <SelectValue :placeholder="placeholder" class="min-w-0 flex-1 truncate text-start">
        {{ current?.label ?? placeholder }}
      </SelectValue>
    </SelectTrigger>
    <SelectContent>
      <SelectItem
        v-for="option in options"
        :key="option.value"
        :value="option.value === '' ? EMPTY : option.value"
        :disabled="option.disabled"
      >
        {{ option.label }}
      </SelectItem>
    </SelectContent>
  </Select>
</template>
