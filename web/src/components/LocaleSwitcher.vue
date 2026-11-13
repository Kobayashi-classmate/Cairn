<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { NButton, NIcon, NPopselect } from 'naive-ui'
import { LanguageOutline } from '@vicons/ionicons5'
import { localeNativeName } from '@/i18n'
import { useLocaleSwitch } from '@/composables/useLocaleSwitch'

const { t } = useI18n()
const { locale, options, renderLabel, switching, select } = useLocaleSwitch()

function onSelect(value: string | number) {
  void select(String(value))
}
</script>

<template>
  <NPopselect
    :value="locale"
    :options="options"
    :render-label="renderLabel"
    :disabled="switching"
    trigger="click"
    @update:value="onSelect"
  >
    <NButton quaternary size="small" :loading="switching" :aria-label="t('common.language.label')">
      <template #icon>
        <NIcon><LanguageOutline /></NIcon>
      </template>
      <span :lang="locale">{{ localeNativeName(locale) }}</span>
    </NButton>
  </NPopselect>
</template>
