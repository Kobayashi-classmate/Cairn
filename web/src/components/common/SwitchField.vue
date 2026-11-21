<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, useId } from 'vue'
import { Switch } from '@/components/ui/switch'

// A switch with its label and an optional explanation, the whole row
// clickable through the label.
const props = defineProps<{
  label: string
  hint?: string
  disabled?: boolean
  id?: string
}>()

const model = defineModel<boolean>({ required: true })

const generated = useId()
const switchId = computed(() => props.id ?? `switch-${generated}`)
const hintId = computed(() => `${switchId.value}-hint`)
</script>

<template>
  <div class="flex items-start gap-3">
    <Switch
      :id="switchId"
      v-model="model"
      :disabled="disabled"
      :aria-describedby="hint ? hintId : undefined"
      class="mt-0.5"
    />
    <div class="flex min-w-0 flex-col gap-0.5">
      <label :for="switchId" class="cursor-pointer text-detail font-medium">{{ label }}</label>
      <p v-if="hint" :id="hintId" class="m-0 text-xs text-muted-foreground">{{ hint }}</p>
    </div>
  </div>
</template>
