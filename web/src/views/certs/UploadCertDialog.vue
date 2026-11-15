<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NForm, NFormItem, NInput, NModal, NText, type FormInst, type FormRules } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { uploadCert } from '@/api/certs'
import type { Job } from '@/api/jobs'
import JobProgress from '@/components/JobProgress.vue'
import { checkRule, requiredRule } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'

/** PEM files are small; anything bigger is not a certificate or key. */
const MAX_FILE_BYTES = 256 * 1024

const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()

const formRef = ref<FormInst | null>(null)
const form = reactive({ name: '', certPem: '', keyPem: '' })
const submitting = ref(false)
const error = ref<unknown>(null)
const jobId = ref<string | null>(null)

watch(show, (open) => {
  if (!open) return
  Object.assign(form, { name: '', certPem: '', keyPem: '' })
  error.value = null
  jobId.value = null
})

const isPem = (value: string) => /-----BEGIN [A-Z0-9 ]+-----/.test(value)

const rules: FormRules = {
  certPem: [requiredRule('certPem'), checkRule('certPem', 'pem', isPem)],
  keyPem: [requiredRule('keyPem'), checkRule('keyPem', 'pem', isPem)],
}

/** Reads a chosen file into a field; the file never leaves the browser except in the upload. */
async function pick(event: Event, field: 'certPem' | 'keyPem') {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file || file.size > MAX_FILE_BYTES) return
  form[field] = await file.text()
}

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  submitting.value = true
  error.value = null
  try {
    jobId.value = (
      await uploadCert({ name: form.name.trim() || undefined, certPem: form.certPem, keyPem: form.keyPem })
    ).id
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
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="t('certs.upload.title')" class="cert-dialog">
    <template v-if="!jobId">
      <NText depth="3" tag="p" class="intro">{{ t('certs.upload.intro') }}</NText>
      <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting">
        <NFormItem path="name" :label="t('common.field.name')">
          <NInput v-model:value="form.name" :input-props="{ autocomplete: 'off' }" />
        </NFormItem>
        <NFormItem path="certPem" :label="t('common.field.certPem')">
          <div class="field">
            <NInput
              v-model:value="form.certPem"
              type="textarea"
              :autosize="{ minRows: 4, maxRows: 10 }"
              :input-props="{ spellcheck: false }"
              class="mono"
            />
            <input type="file" accept=".pem,.crt,.cer,.txt" class="file" @change="pick($event, 'certPem')" />
          </div>
        </NFormItem>
        <NFormItem path="keyPem" :label="t('common.field.keyPem')">
          <div class="field">
            <NInput
              v-model:value="form.keyPem"
              type="textarea"
              :autosize="{ minRows: 4, maxRows: 10 }"
              :input-props="{ spellcheck: false }"
              class="mono"
            />
            <input type="file" accept=".pem,.key,.txt" class="file" @change="pick($event, 'keyPem')" />
          </div>
        </NFormItem>
      </NForm>
      <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
      <div class="actions">
        <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
        <NButton type="primary" :loading="submitting" @click="submit">{{ t('certs.upload.submit') }}</NButton>
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
.cert-dialog {
  inline-size: min(640px, calc(100vw - 32px));
}

.intro {
  margin-block: 0 12px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  inline-size: 100%;
}

.file {
  font-size: 13px;
}

.mono :deep(textarea) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
}

.block {
  margin-block: 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
