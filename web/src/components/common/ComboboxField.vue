<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, useId, type HTMLAttributes } from 'vue'
import { ChevronDown, X } from '@lucide/vue'
import { cn } from '@/lib/utils'

// A text field with suggestions (interface spec, section 9 "Combobox"): any
// text may be typed, and the known values that contain it are offered below.
// Arrow keys move through them, Enter takes the highlighted one or the typed
// text, Escape closes the list. `commit` fires when a value is chosen.
// The list is not portalled: keep the field out of containers that clip.
defineOptions({ inheritAttrs: false })

const props = withDefaults(
  defineProps<{
    options: readonly string[]
    placeholder?: string
    /** Most suggestions shown at once. */
    limit?: number
    mono?: boolean
    disabled?: boolean
    /** Accessible name of the clear button. */
    clearLabel?: string
    class?: HTMLAttributes['class']
  }>(),
  { placeholder: undefined, limit: 60, clearLabel: undefined, class: undefined },
)

const model = defineModel<string>({ required: true })
const emit = defineEmits<{ commit: [value: string] }>()

const listId = `combobox-${useId()}`
const open = ref(false)
const active = ref(-1)
const input = ref<HTMLInputElement | null>(null)
/** What was typed since the list opened; the list shows everything before typing. */
const typed = ref(false)

const shown = computed(() => {
  const query = typed.value ? model.value.trim().toLowerCase() : ''
  const matches = query ? props.options.filter((option) => option.toLowerCase().includes(query)) : props.options
  return matches.slice(0, props.limit)
})

function optionId(index: number): string {
  return `${listId}-${index}`
}

function show() {
  if (props.disabled) return
  open.value = true
}

function hide() {
  open.value = false
  active.value = -1
  typed.value = false
}

function onInput(event: Event) {
  model.value = (event.target as HTMLInputElement).value
  typed.value = true
  active.value = -1
  show()
}

function pick(value: string) {
  model.value = value
  hide()
  emit('commit', value)
}

function move(step: number) {
  if (!open.value) {
    show()
    return
  }
  const count = shown.value.length
  if (count === 0) return
  active.value = active.value < 0 ? (step > 0 ? 0 : count - 1) : (active.value + step + count) % count
  document.getElementById(optionId(active.value))?.scrollIntoView({ block: 'nearest' })
}

function onKeydown(event: KeyboardEvent) {
  switch (event.key) {
    case 'ArrowDown':
      event.preventDefault()
      move(1)
      break
    case 'ArrowUp':
      event.preventDefault()
      move(-1)
      break
    case 'Enter': {
      event.preventDefault()
      const option = open.value && active.value >= 0 ? shown.value[active.value] : undefined
      pick(option ?? model.value.trim())
      break
    }
    case 'Escape':
      if (open.value) {
        event.preventDefault()
        event.stopPropagation()
        hide()
      }
      break
  }
}

function toggle() {
  if (open.value) {
    hide()
    return
  }
  show()
  input.value?.focus()
}

function clear() {
  pick('')
  input.value?.focus()
}
</script>

<template>
  <div :class="cn('relative min-w-0', props.class)">
    <div
      class="flex h-9 w-full items-center rounded-md border border-input bg-card transition-[color,box-shadow] focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/50 max-md:h-11"
      :class="disabled && 'pointer-events-none opacity-50'"
    >
      <input
        ref="input"
        v-bind="$attrs"
        type="text"
        role="combobox"
        aria-autocomplete="list"
        :aria-expanded="open && shown.length > 0"
        :aria-controls="listId"
        :aria-activedescendant="open && active >= 0 ? optionId(active) : undefined"
        :value="model"
        :placeholder="placeholder"
        :disabled="disabled"
        autocomplete="off"
        autocapitalize="none"
        spellcheck="false"
        dir="ltr"
        :class="cn('h-full min-w-0 flex-1 bg-transparent px-3 text-base outline-none placeholder:text-muted-foreground md:text-sm', mono && 'font-mono md:text-[13px]')"
        @input="onInput"
        @keydown="onKeydown"
        @click="show"
        @blur="hide"
      />
      <button
        v-if="model && clearLabel"
        type="button"
        tabindex="-1"
        class="inline-flex size-7 shrink-0 items-center justify-center rounded-sm text-muted-foreground outline-none hover:text-foreground"
        :aria-label="clearLabel"
        @mousedown.prevent
        @click="clear"
      >
        <X class="size-3.5" aria-hidden="true" />
      </button>
      <button
        type="button"
        tabindex="-1"
        class="me-1 inline-flex size-7 shrink-0 items-center justify-center rounded-sm text-muted-foreground outline-none hover:text-foreground"
        aria-hidden="true"
        @mousedown.prevent
        @click="toggle"
      >
        <ChevronDown class="size-4" />
      </button>
    </div>
    <ul
      v-show="open && shown.length > 0"
      :id="listId"
      role="listbox"
      class="absolute inset-x-0 top-full z-50 m-0 mt-1 max-h-64 list-none overflow-y-auto rounded-md border bg-popover p-1 text-popover-foreground shadow-overlay"
    >
      <li
        v-for="(option, index) in shown"
        :id="optionId(index)"
        :key="option"
        role="option"
        :aria-selected="index === active"
        :class="
          cn(
            'cursor-default truncate rounded-sm px-2 py-1.5 text-sm select-none max-md:py-3',
            mono && 'font-mono text-[13px]',
            index === active ? 'bg-accent text-accent-foreground' : 'hover:bg-accent/60',
          )
        "
        dir="ltr"
        @mousedown.prevent="pick(option)"
      >
        {{ option }}
      </li>
    </ul>
  </div>
</template>
