<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NForm,
  NFormItem,
  NSelect,
  NSpin,
  NText,
  useMessage,
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { fetchSettings, updateDefaultLocale } from '@/api/settings'
import { useLocaleSwitch } from '@/composables/useLocaleSwitch'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const message = useMessage()
const session = useSessionStore()
// Reuses the language list (native names); `select` is not used here.
const { options: localeOptions, renderLabel } = useLocaleSwitch()

const canManage = computed(() => session.can('settings.manage'))
const saved = ref<string | null>(null)
const defaultLocale = ref<string | null>(null)
const loading = ref(true)
const saving = ref(false)
const error = ref<unknown>(null)

onMounted(async () => {
  try {
    const settings = await fetchSettings()
    saved.value = settings.defaultLocale
    defaultLocale.value = settings.defaultLocale
  } catch (err) {
    error.value = err
  } finally {
    loading.value = false
  }
})

const changed = computed(() => defaultLocale.value !== null && defaultLocale.value !== saved.value)

async function save() {
  if (!defaultLocale.value) return
  error.value = null
  saving.value = true
  try {
    await updateDefaultLocale(defaultLocale.value)
    saved.value = defaultLocale.value
    if (session.meta) session.meta.defaultLocale = defaultLocale.value
    message.success(t('settings.panel.saved'))
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <NCard :title="t('settings.panel.title')" size="small">
    <NAlert v-if="!canManage" type="info" class="block">{{ t('settings.panel.readOnly') }}</NAlert>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <NSpin :show="loading">
      <NForm :show-feedback="false" @submit.prevent="save">
        <NFormItem :label="t('settings.panel.defaultLocale')">
          <div class="field">
            <NSelect
              v-model:value="defaultLocale"
              :options="localeOptions"
              :render-label="renderLabel"
              :disabled="!canManage || saving"
              class="field__control"
            />
            <NText depth="3" class="field__hint">{{ t('settings.panel.defaultLocaleHint') }}</NText>
          </div>
        </NFormItem>
      </NForm>
      <NButton
        v-if="canManage"
        type="primary"
        :disabled="!changed"
        :loading="saving"
        class="save"
        @click="save"
      >
        {{ t('common.action.save') }}
      </NButton>
    </NSpin>
  </NCard>
</template>

<style scoped>
.block {
  margin-block-end: 16px;
}

.field {
  display: flex;
  inline-size: 100%;
  flex-direction: column;
  gap: 4px;
}

.field__control {
  max-inline-size: 320px;
}

.field__hint {
  font-size: 13px;
}

.save {
  margin-block-start: 16px;
}
</style>
