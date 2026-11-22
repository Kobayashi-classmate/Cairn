<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import PasswordInput from '@/components/common/PasswordInput.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import type { SegmentedOption } from '@/components/common/segmented'
import StatusPill from '@/components/common/StatusPill.vue'
import type { SecretMode } from '@/forms/secret'

// A write-only secret (password, signing key). The server never returns the
// stored value, only whether one exists, so an existing secret can be kept,
// replaced or removed. The field's id and aria attributes (from FormField)
// go to the text field.
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  /** Whether a value is stored on the server. */
  stored: boolean
  /** Names the keep / replace / remove choice for screen readers. */
  label: string
  disabled?: boolean
}>()

const mode = defineModel<SecretMode>('mode', { required: true })
const value = defineModel<string>('value', { required: true })

const { t } = useI18n()

const options = computed<SegmentedOption<SecretMode>[]>(() => [
  { value: 'keep', label: t('common.secret.keep') },
  { value: 'set', label: t('common.secret.replace') },
  { value: 'clear', label: t('common.secret.clear') },
])

const showInput = computed(() => !props.stored || mode.value === 'set')

function onInput(next: string) {
  value.value = next
  mode.value = 'set'
}
</script>

<template>
  <div class="flex w-full flex-col gap-2">
    <div class="flex flex-wrap items-center gap-2">
      <StatusPill :tone="stored ? 'success' : 'neutral'">
        {{ stored ? t('common.secret.set') : t('common.secret.notSet') }}
      </StatusPill>
      <SegmentedControl v-if="stored" v-model="mode" :options="options" :label="label" :disabled="disabled" />
    </div>
    <PasswordInput
      v-if="showInput"
      v-bind="$attrs"
      :model-value="value"
      autocomplete="new-password"
      :disabled="disabled"
      @update:model-value="onInput"
    />
  </div>
</template>
