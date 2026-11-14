<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NForm,
  NFormItem,
  NInput,
  NSelect,
  NSpin,
  NSwitch,
  useMessage,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import {
  CLOUD_CHANNELS,
  fetchCloudSettings,
  saveCloudSettings,
  type CloudChannel,
} from '@/api/settings'
import { checkRule, isHttpsUrl, requiredRule } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

const emit = defineEmits<{ saved: [] }>()

const { t } = useI18n()
const message = useMessage()
const session = useSessionStore()

const canManage = computed(() => session.can('settings.manage'))
const formRef = ref<FormInst | null>(null)
const form = reactive({ enabled: true, baseUrl: '', channel: 'stable' as CloudChannel })
const loading = ref(true)
const saving = ref(false)
const error = ref<unknown>(null)

onMounted(async () => {
  try {
    Object.assign(form, await fetchCloudSettings())
  } catch (err) {
    error.value = err
  } finally {
    loading.value = false
  }
})

const rules: FormRules = {
  baseUrl: [requiredRule('baseUrl'), checkRule('baseUrl', 'https', isHttpsUrl)],
  channel: [requiredRule('channel')],
}

const channelOptions = computed(() =>
  CLOUD_CHANNELS.map((value) => ({ value, label: t(`updates.channel.${value}`) })),
)

async function save() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  error.value = null
  saving.value = true
  try {
    await saveCloudSettings({
      enabled: form.enabled,
      baseUrl: form.baseUrl.trim(),
      channel: form.channel,
    })
    message.success(t('settings.panel.saved'))
    emit('saved')
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <NCard :title="t('updates.cloud.title')" size="small">
    <NAlert type="info" :show-icon="false" class="block">
      <ul class="help">
        <li>{{ t('updates.cloud.helpDownloadOnly') }}</li>
        <li>{{ t('updates.cloud.helpDisable') }}</li>
        <li>{{ t('updates.cloud.helpMirror') }}</li>
      </ul>
    </NAlert>
    <NAlert v-if="!canManage" type="info" class="block">{{ t('settings.panel.readOnly') }}</NAlert>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <NSpin :show="loading">
      <NForm
        ref="formRef"
        :model="form"
        :rules="rules"
        :disabled="!canManage || saving"
        class="form"
        @submit.prevent="save"
      >
        <NFormItem :label="t('updates.cloud.enabled')">
          <NSwitch v-model:value="form.enabled" />
        </NFormItem>
        <NFormItem path="baseUrl" :label="t('common.field.baseUrl')">
          <NInput
            v-model:value="form.baseUrl"
            :input-props="{ autocomplete: 'off', spellcheck: false, type: 'url' }"
          />
        </NFormItem>
        <NFormItem path="channel" :label="t('common.field.channel')">
          <NSelect v-model:value="form.channel" :options="channelOptions" />
        </NFormItem>
        <NButton v-if="canManage" type="primary" attr-type="submit" :loading="saving">
          {{ t('common.action.save') }}
        </NButton>
      </NForm>
    </NSpin>
  </NCard>
</template>

<style scoped>
.block {
  margin-block-end: 12px;
}

.help {
  margin: 0;
  padding-inline-start: 1.2em;
}

.form {
  max-inline-size: 480px;
}
</style>
