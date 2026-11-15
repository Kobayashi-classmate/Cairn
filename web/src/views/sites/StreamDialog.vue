<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import {
  NAlert,
  NButton,
  NDynamicTags,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NModal,
  NRadioButton,
  NRadioGroup,
  NSwitch,
  NText,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { isApiError, isStepUpRequired } from '@/api/client'
import type { Job } from '@/api/jobs'
import { createStream, updateStream, type StreamProtocol, type StreamRequest, type StreamView } from '@/api/streams'
import JobProgress from '@/components/JobProgress.vue'
import { checkRule, listRule, requiredListRule } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { MAX_UPSTREAMS, isHostPort, isSiteCode, isStreamPort } from '@/sites/validate'

const show = defineModel<boolean>('show', { required: true })
/** The stream to edit, or null to create one. */
const props = defineProps<{ stream: StreamView | null }>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()

const formRef = ref<FormInst | null>(null)
const form = reactive({
  code: '',
  protocol: 'tcp' as StreamProtocol,
  port: null as number | null,
  upstreams: [] as string[],
  hash: false,
})
const submitting = ref(false)
const error = ref<unknown>(null)
const jobId = ref<string | null>(null)

const editing = computed(() => props.stream !== null)

watch(show, (open) => {
  if (!open) return
  const stream = props.stream
  Object.assign(form, {
    code: '',
    protocol: stream?.protocol ?? 'tcp',
    port: stream?.port ?? null,
    upstreams: [...(stream?.upstreams ?? [])],
    hash: stream?.balance === 'hash',
  })
  error.value = null
  jobId.value = null
})

function onUpstreams(values: string[]) {
  form.upstreams = [...new Set(values.map((v) => v.trim().toLowerCase()).filter(Boolean))]
}

const derivedCode = computed(() => (form.port ? `${form.protocol}-${form.port}` : ''))

const rules = computed<FormRules>(() => ({
  code: [checkRule('code', 'site_code', isSiteCode)],
  port: [
    {
      trigger: ['blur', 'change'],
      validator: (_rule, value: unknown) => typeof value === 'number' && isStreamPort(value),
      renderMessage: () => validationMessage('port', 'port'),
    },
  ],
  upstreams: [
    requiredListRule('upstreams'),
    listRule('upstreams', 'host_port', isHostPort),
    {
      trigger: 'change',
      validator: (_rule, value: unknown) => !Array.isArray(value) || value.length <= MAX_UPSTREAMS,
      renderMessage: () => validationMessage('upstreams', 'count'),
    },
  ],
}))

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  const request: StreamRequest = { protocol: form.protocol, port: form.port ?? 0, upstreams: form.upstreams }
  if (form.hash) request.balance = 'hash'
  if (!editing.value && form.code.trim()) request.code = form.code.trim()
  submitting.value = true
  error.value = null
  try {
    const job = props.stream ? await updateStream(props.stream.code, request) : await createStream(request)
    jobId.value = job.id
    emit('changed')
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}

function onFinished(job: Job) {
  emit('changed')
  if (job.state === 'succeeded') show.value = false
}

/** nginx's stream module comes with nginx from the software page. */
const moduleMissing = computed(() => isApiError(error.value) && error.value.code === 'stream.module_missing')
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    :title="editing ? t('streams.dialog.editTitle', { code: stream?.code ?? '' }) : t('streams.dialog.createTitle')"
    class="stream-dialog"
  >
    <template v-if="!jobId">
      <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting">
        <NFormItem :label="t('common.field.protocol')" :show-feedback="false" class="block">
          <NRadioGroup v-model:value="form.protocol">
            <NRadioButton value="tcp">TCP</NRadioButton>
            <NRadioButton value="udp">UDP</NRadioButton>
          </NRadioGroup>
        </NFormItem>
        <NFormItem path="port" :label="t('common.field.port')">
          <div class="field">
            <NInputNumber v-model:value="form.port" :min="1" :max="65535" :show-button="false" />
            <NText depth="3" class="hint">{{ t('streams.dialog.portHint') }}</NText>
          </div>
        </NFormItem>
        <NFormItem v-if="!editing" path="code" :label="t('streams.code')">
          <NInput
            v-model:value="form.code"
            :maxlength="20"
            :placeholder="derivedCode"
            :input-props="{ autocomplete: 'off', spellcheck: false }"
          />
        </NFormItem>
        <NFormItem path="upstreams" :label="t('common.field.upstreams')">
          <div class="field">
            <NDynamicTags :value="form.upstreams" @update:value="onUpstreams" />
            <NText depth="3" class="hint">{{ t('streams.dialog.upstreamsHint') }}</NText>
          </div>
        </NFormItem>
        <label class="switch">
          <NSwitch v-model:value="form.hash" />
          <span>
            {{ t('streams.dialog.hash') }}
            <NText depth="3" tag="div" class="hint">{{ t('streams.dialog.hashHint') }}</NText>
          </span>
        </label>
      </NForm>
      <NAlert type="info" :show-icon="false" class="block">{{ t('streams.firewallNote') }}</NAlert>
      <NAlert v-if="error" type="error" class="block">
        {{ errorMessage(error) }}
        <RouterLink v-if="moduleMissing" :to="{ name: 'software' }">{{ t('sites.installNginx') }}</RouterLink>
      </NAlert>
      <div class="actions">
        <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
        <NButton type="primary" :loading="submitting" @click="submit">{{ t('common.action.save') }}</NButton>
      </div>
    </template>
    <template v-else>
      <JobProgress :job-id="jobId" @finished="onFinished" />
      <div class="actions">
        <NButton @click="show = false">{{ t('sites.create.close') }}</NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.stream-dialog {
  inline-size: min(560px, calc(100vw - 32px));
}

.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  inline-size: 100%;
}

.hint {
  font-size: 12px;
}

.switch {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin-block-end: 16px;
  cursor: pointer;
}

.block {
  margin-block: 0 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
