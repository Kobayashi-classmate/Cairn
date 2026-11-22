<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { SETGID, STICKY, modeString, octal, parseOctal } from '@/files/modes'

// Edits a permission mode both as octal text and as a grid of checkboxes;
// both stay in step. Folders may also get setgid and sticky.
const mode = defineModel<number>('mode', { required: true })
const props = defineProps<{
  /** Folders: setgid and sticky are offered. */
  dir: boolean
  /** Names the editor ("Folders", "Files"). */
  label: string
}>()

const { t } = useI18n()
const id = `mode-${useId()}`
const text = ref(octal(mode.value))
const mask = computed(() => (props.dir ? 0o3777 : 0o777))
/** The octal text cannot be read, so `mode` is not what it shows: the form must not submit. */
const invalid = defineModel<boolean>('invalid', { default: false })
// A field that is gone holds no bad input.
onBeforeUnmount(() => (invalid.value = false))

watch(mode, (value) => {
  if (parseOctal(text.value, mask.value) !== value) text.value = octal(value)
})

function onText(value: string | number) {
  text.value = String(value)
  const parsed = parseOctal(text.value, mask.value)
  invalid.value = parsed === null
  if (parsed !== null) mode.value = parsed
}

const WHO = ['owner', 'group', 'others'] as const
const WHAT = ['read', 'write', 'execute'] as const

function bit(who: number, what: number): number {
  return 1 << (8 - who * 3 - what)
}

function toggle(flag: number, on: boolean | 'indeterminate') {
  mode.value = on === true ? mode.value | flag : mode.value & ~flag
  invalid.value = false
}
</script>

<template>
  <fieldset class="m-0 flex min-w-0 flex-col gap-3 rounded-md border p-3">
    <legend class="px-1 text-detail font-medium">{{ label }}</legend>
    <div class="flex flex-wrap items-center gap-3">
      <label :for="`${id}-octal`" class="sr-only">{{ t('files.chmod.octal') }}</label>
      <Input
        :id="`${id}-octal`"
        :model-value="text"
        inputmode="numeric"
        autocomplete="off"
        maxlength="4"
        class="w-20 font-mono"
        :aria-invalid="invalid || undefined"
        :aria-describedby="invalid ? `${id}-error` : undefined"
        dir="ltr"
        @update:model-value="onText"
      />
      <span class="font-mono text-detail text-muted-foreground" dir="ltr">{{ modeString(dir ? 'dir' : 'file', mode) }}</span>
    </div>
    <p v-if="invalid" :id="`${id}-error`" class="m-0 text-xs font-medium text-destructive">{{ t('files.chmod.octalInvalid') }}</p>
    <table class="w-full max-w-[340px] border-collapse text-detail">
      <thead>
        <tr class="text-caption text-muted-foreground">
          <th scope="col" class="py-1 text-start font-medium"><span class="sr-only">{{ t('files.chmod.who') }}</span></th>
          <th v-for="what in WHAT" :key="what" scope="col" class="px-2 py-1 text-center font-medium">{{ t(`files.chmod.${what}`) }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(who, w) in WHO" :key="who" class="border-t">
          <th scope="row" class="py-1.5 text-start font-normal">{{ t(`files.chmod.${who}`) }}</th>
          <td v-for="(what, i) in WHAT" :key="what" class="px-2 py-1.5 text-center">
            <span class="inline-flex max-md:size-11 max-md:items-center max-md:justify-center">
              <Checkbox
                :model-value="(mode & bit(w, i)) !== 0"
                :aria-label="`${t(`files.chmod.${who}`)}: ${t(`files.chmod.${what}`)}`"
                @update:model-value="(on: boolean | 'indeterminate') => toggle(bit(w, i), on)"
              />
            </span>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-if="dir" class="flex flex-col gap-2">
      <label class="flex cursor-pointer items-start gap-2.5 text-detail">
        <Checkbox
          :model-value="(mode & SETGID) !== 0"
          class="mt-0.5"
          @update:model-value="(on: boolean | 'indeterminate') => toggle(SETGID, on)"
        />
        <span>{{ t('files.chmod.setgid') }}</span>
      </label>
      <label class="flex cursor-pointer items-start gap-2.5 text-detail">
        <Checkbox
          :model-value="(mode & STICKY) !== 0"
          class="mt-0.5"
          @update:model-value="(on: boolean | 'indeterminate') => toggle(STICKY, on)"
        />
        <span>{{ t('files.chmod.sticky') }}</span>
      </label>
    </div>
  </fieldset>
</template>
