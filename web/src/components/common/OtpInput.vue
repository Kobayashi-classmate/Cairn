<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { PinInputInput, PinInputRoot } from 'reka-ui'

// A one-time code typed into one box per digit. Typing moves to the next
// box, Backspace to the previous one, and pasting fills all of them.
const props = withDefaults(
  defineProps<{
    modelValue: string
    length?: number
    id?: string
    disabled?: boolean
    invalid?: boolean
    describedBy?: string
  }>(),
  { length: 6, id: undefined, describedBy: undefined },
)

const emit = defineEmits<{
  'update:modelValue': [value: string]
  complete: [value: string]
}>()

const { t } = useI18n()
const root = ref<HTMLElement | null>(null)

const digits = computed<number[]>(() =>
  [...props.modelValue].filter((c) => c >= '0' && c <= '9').map(Number),
)

function join(value: (number | undefined)[]): string {
  return value.map((d) => (d === undefined || Number.isNaN(d) ? '' : String(d))).join('')
}

function onUpdate(value: (number | undefined)[]) {
  emit('update:modelValue', join(value))
}

function onComplete(value: (number | undefined)[]) {
  emit('complete', join(value))
}

/** Puts the cursor into the first empty box. */
async function focus() {
  await nextTick()
  const inputs = root.value?.querySelectorAll<HTMLInputElement>('input') ?? []
  const target = [...inputs].find((input) => input.value === '') ?? inputs[0]
  target?.focus()
}

defineExpose({ focus })
</script>

<template>
  <div ref="root">
    <PinInputRoot
      :id="id"
      :model-value="digits"
      type="number"
      otp
      :disabled="disabled"
      class="flex justify-between gap-2"
      @update:model-value="onUpdate"
      @complete="onComplete"
    >
      <PinInputInput
        v-for="(_, index) in length"
        :key="index"
        :index="index"
        :aria-label="t('auth.mfa.digit', { position: index + 1, count: length })"
        :aria-invalid="invalid || undefined"
        :aria-describedby="describedBy"
        class="h-13 w-11 min-w-0 flex-1 rounded-md border border-input bg-card text-center font-mono text-xl font-semibold tabular-nums transition-[border-color,box-shadow] outline-none focus:border-ring focus:ring-2 focus:ring-ring/50 disabled:opacity-50 aria-invalid:border-destructive"
      />
    </PinInputRoot>
  </div>
</template>
