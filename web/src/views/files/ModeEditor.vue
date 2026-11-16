<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Edits a permission mode both as octal text and as checkboxes.
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NCheckbox, NInput } from 'naive-ui'
import { SETGID, STICKY, octal, parseOctal } from '@/files/modes'

const mode = defineModel<number>('mode', { required: true })
const props = defineProps<{
  /** Directories may also get setgid and sticky. */
  dir: boolean
}>()

const { t } = useI18n()
const text = ref(octal(mode.value))
const mask = computed(() => (props.dir ? 0o3777 : 0o777))
const invalid = ref(false)

watch(mode, (value) => {
  if (parseOctal(text.value, mask.value) !== value) text.value = octal(value)
})

function onText(value: string) {
  text.value = value
  const parsed = parseOctal(value, mask.value)
  invalid.value = parsed === null
  if (parsed !== null) mode.value = parsed
}

const WHO = ['owner', 'group', 'others'] as const
const WHAT = ['read', 'write', 'execute'] as const

function bit(who: number, what: number): number {
  return 1 << (8 - who * 3 - what)
}

function toggle(flag: number, on: boolean) {
  mode.value = on ? mode.value | flag : mode.value & ~flag
  invalid.value = false
}
</script>

<template>
  <div class="mode-editor">
    <div class="mode-editor__octal">
      <NInput
        :value="text"
        :status="invalid ? 'error' : undefined"
        class="mono"
        :input-props="{ inputmode: 'numeric', autocomplete: 'off', 'aria-label': t('files.chmod.octal') }"
        @update:value="onText"
      />
    </div>
    <table class="mode-editor__grid">
      <thead>
        <tr>
          <th />
          <th v-for="what in WHAT" :key="what" scope="col">{{ t(`files.chmod.${what}`) }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(who, w) in WHO" :key="who">
          <th scope="row">{{ t(`files.chmod.${who}`) }}</th>
          <td v-for="(what, i) in WHAT" :key="what">
            <NCheckbox
              :checked="(mode & bit(w, i)) !== 0"
              :aria-label="`${t(`files.chmod.${who}`)} ${t(`files.chmod.${what}`)}`"
              @update:checked="(on: boolean) => toggle(bit(w, i), on)"
            />
          </td>
        </tr>
      </tbody>
    </table>
    <div v-if="dir" class="mode-editor__special">
      <NCheckbox :checked="(mode & SETGID) !== 0" @update:checked="(on: boolean) => toggle(SETGID, on)">
        {{ t('files.chmod.setgid') }}
      </NCheckbox>
      <NCheckbox :checked="(mode & STICKY) !== 0" @update:checked="(on: boolean) => toggle(STICKY, on)">
        {{ t('files.chmod.sticky') }}
      </NCheckbox>
    </div>
  </div>
</template>

<style scoped>
.mode-editor {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.mode-editor__octal {
  inline-size: 120px;
}

.mode-editor__grid {
  border-collapse: collapse;
}

.mode-editor__grid th,
.mode-editor__grid td {
  padding: 2px 10px;
  text-align: center;
  font-weight: normal;
  font-size: 13px;
}

.mode-editor__grid th[scope='row'] {
  text-align: start;
  padding-inline-start: 0;
}

.mode-editor__special {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
}

.mono :deep(input) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
