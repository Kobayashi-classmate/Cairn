<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref, type HTMLAttributes } from 'vue'
import { useI18n } from 'vue-i18n'
import { X } from '@lucide/vue'
import { cn } from '@/lib/utils'

// A list of short values (domains, file names, upstreams) typed one after
// the other: Enter, a comma, a space or leaving the field adds what was
// typed; Backspace in the empty field removes the last value. The field's
// id and aria attributes (from FormField) go to the text input.
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  placeholder?: string
  /** Turns typed text into the stored value (lower-casing, punycode, ...). */
  normalize?: (value: string) => string
  /** Values failing this are shown in the error colour. */
  valid?: (value: string) => boolean
  max?: number
  disabled?: boolean
  /** Monospace values (paths, host:port). */
  mono?: boolean
  class?: HTMLAttributes['class']
}>()

const model = defineModel<string[]>({ required: true })

const { t } = useI18n()
const draft = ref('')
const input = ref<HTMLInputElement | null>(null)

function add(raw: string) {
  const parts = raw
    .split(/[\s,]+/)
    .map((part) => (props.normalize ? props.normalize(part) : part.trim()))
    .filter(Boolean)
  if (parts.length === 0) return
  const next = [...model.value]
  for (const part of parts) {
    if (!next.includes(part) && (props.max === undefined || next.length < props.max)) next.push(part)
  }
  model.value = next
}

/** Adds what is still typed in the field; forms call it before checking their values. */
function commit() {
  if (draft.value.trim() === '') return
  add(draft.value)
  draft.value = ''
}

function remove(index: number) {
  model.value = model.value.filter((_, i) => i !== index)
  input.value?.focus()
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Enter' || event.key === ',' || (event.key === ' ' && draft.value.trim() !== '')) {
    // Enter adds the value instead of submitting the form.
    if (draft.value.trim() !== '' || event.key === 'Enter') event.preventDefault()
    commit()
  } else if (event.key === 'Backspace' && draft.value === '' && model.value.length > 0) {
    model.value = model.value.slice(0, -1)
  }
}

function onPaste(event: ClipboardEvent) {
  const text = event.clipboardData?.getData('text') ?? ''
  if (!/[\s,]/.test(text.trim())) return
  event.preventDefault()
  add(draft.value + text)
  draft.value = ''
}

defineExpose({ commit, focus: () => input.value?.focus() })
</script>

<template>
  <div
    :class="
      cn(
        'flex min-h-9 w-full flex-wrap items-center gap-1.5 rounded-md border border-input bg-card px-2 py-1 transition-[color,box-shadow] focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/50 has-[input[aria-invalid=true]]:border-destructive',
        disabled && 'pointer-events-none opacity-50',
        props.class,
      )
    "
    @click.self="input?.focus()"
  >
    <span
      v-for="(value, index) in model"
      :key="value"
      :class="
        cn(
          'inline-flex max-w-full items-center gap-1 rounded-sm py-0.5 ps-2 pe-1 text-detail',
          valid && !valid(value) ? 'bg-destructive-soft text-destructive' : 'bg-muted text-foreground',
          mono && 'font-mono text-[12.5px]',
        )
      "
    >
      <span class="min-w-0 truncate" dir="ltr">{{ value }}</span>
      <button
        type="button"
        class="inline-flex size-5 shrink-0 items-center justify-center rounded-sm text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 max-md:size-7"
        :aria-label="t('common.tags.remove', { value })"
        :disabled="disabled"
        @click="remove(index)"
      >
        <X class="size-3.5" aria-hidden="true" />
      </button>
    </span>
    <input
      ref="input"
      v-model="draft"
      v-bind="$attrs"
      type="text"
      autocomplete="off"
      autocapitalize="none"
      spellcheck="false"
      dir="ltr"
      :placeholder="model.length === 0 ? placeholder : undefined"
      :disabled="disabled || (max !== undefined && model.length >= max)"
      :class="cn('h-7 min-w-32 flex-1 bg-transparent px-1 text-base outline-none placeholder:text-muted-foreground md:text-sm', mono && 'font-mono')"
      @keydown="onKeydown"
      @blur="commit"
      @paste="onPaste"
    />
  </div>
</template>
