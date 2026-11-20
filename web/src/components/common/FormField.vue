<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, type HTMLAttributes } from 'vue'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'

// Label, control, help text and error, in that order (interface spec,
// section 9). The default slot receives the attributes the control needs to
// be tied to them: `id`, `aria-describedby` and `aria-invalid`.
const props = defineProps<{
  id: string
  label: string
  hint?: string
  /** Validation or server error for this field; replaces nothing, shown below the hint. */
  error?: string | null
  class?: HTMLAttributes['class']
}>()

const hintId = computed(() => `${props.id}-hint`)
const errorId = computed(() => `${props.id}-error`)

const control = computed(() => {
  const described = [props.hint ? hintId.value : '', props.error ? errorId.value : ''].filter(Boolean)
  return {
    id: props.id,
    'aria-describedby': described.length > 0 ? described.join(' ') : undefined,
    'aria-invalid': props.error ? true : undefined,
  }
})
</script>

<template>
  <div :class="cn('flex flex-col gap-1.5', props.class)">
    <Label :for="id" class="text-detail font-medium">{{ label }}</Label>
    <slot v-bind="control" />
    <p v-if="hint" :id="hintId" class="m-0 text-xs text-muted-foreground">{{ hint }}</p>
    <p v-if="error" :id="errorId" class="m-0 text-xs font-medium text-destructive">{{ error }}</p>
  </div>
</template>
