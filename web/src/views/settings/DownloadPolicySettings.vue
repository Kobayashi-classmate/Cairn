<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NRadio, NRadioGroup, NSpin, NText, useMessage } from 'naive-ui'
import {
  DOWNLOAD_POLICIES,
  fetchSoftwareSettings,
  saveSoftwareSettings,
  type DownloadPolicy,
} from '@/api/software'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const message = useMessage()
const session = useSessionStore()

const canManage = computed(() => session.can('software.manage'))
const saved = ref<DownloadPolicy | null>(null)
const policy = ref<DownloadPolicy | null>(null)
const loading = ref(true)
const saving = ref(false)
const error = ref<unknown>(null)

onMounted(async () => {
  try {
    saved.value = (await fetchSoftwareSettings()).downloadPolicy
    policy.value = saved.value
  } catch (err) {
    error.value = err
  } finally {
    loading.value = false
  }
})

const changed = computed(() => policy.value !== null && policy.value !== saved.value)

async function save() {
  if (!policy.value) return
  error.value = null
  saving.value = true
  try {
    saved.value = (await saveSoftwareSettings({ downloadPolicy: policy.value })).downloadPolicy
    policy.value = saved.value
    message.success(t('settings.panel.saved'))
  } catch (err) {
    error.value = err
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <NCard :title="t('software.policy.title')" size="small">
    <NText depth="3" tag="p" class="intro">{{ t('software.policy.intro') }}</NText>
    <NAlert v-if="!canManage" type="info" class="block">{{ t('software.policy.readOnly') }}</NAlert>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <NSpin :show="loading">
      <NRadioGroup v-model:value="policy" :disabled="!canManage || saving" class="options">
        <NRadio v-for="option in DOWNLOAD_POLICIES" :key="option" :value="option" class="option">
          <div class="option__label">{{ t(`software.policy.${option}.short`) }}</div>
          <NText depth="3" class="option__hint">{{ t(`software.policy.${option}.description`) }}</NText>
        </NRadio>
      </NRadioGroup>
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
.intro {
  margin-block: 0 12px;
}

.block {
  margin-block-end: 12px;
}

.options {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.option__label {
  font-weight: 500;
}

.option__hint {
  font-size: 13px;
}

.save {
  margin-block-start: 16px;
}
</style>
