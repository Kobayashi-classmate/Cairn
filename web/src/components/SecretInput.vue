<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { NInput, NRadio, NRadioGroup, NTag } from 'naive-ui'
import type { SecretMode } from '@/forms/secret'

// A write-only secret (password, signing key). The server never returns the
// stored value, only whether one exists, so an existing secret can be kept,
// replaced or removed.

defineProps<{
  /** Whether a value is stored on the server. */
  stored: boolean
}>()

const mode = defineModel<SecretMode>('mode', { required: true })
const value = defineModel<string>('value', { required: true })

const { t } = useI18n()
</script>

<template>
  <div class="secret">
    <NTag size="small" :bordered="false" :type="stored ? 'success' : 'default'" class="secret__state">
      {{ stored ? t('common.secret.set') : t('common.secret.notSet') }}
    </NTag>
    <NRadioGroup v-if="stored" v-model:value="mode" size="small">
      <NRadio value="keep">{{ t('common.secret.keep') }}</NRadio>
      <NRadio value="set">{{ t('common.secret.replace') }}</NRadio>
      <NRadio value="clear">{{ t('common.secret.clear') }}</NRadio>
    </NRadioGroup>
    <NInput
      v-if="!stored || mode === 'set'"
      v-model:value="value"
      type="password"
      show-password-on="click"
      :input-props="{ autocomplete: 'new-password' }"
      @update:value="mode = 'set'"
    />
  </div>
</template>

<style scoped>
.secret {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 8px;
  inline-size: 100%;
}
</style>
