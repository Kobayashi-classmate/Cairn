<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { FileUp } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import { uploadCert } from '@/api/certs'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { runCertJob } from './actions'

// Upload a certificate chain and its private key (PEM).
/** PEM files are small; anything bigger is not a certificate or key. */
const MAX_FILE_BYTES = 256 * 1024

const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()

const form = reactive({ name: '', certPem: '', keyPem: '' })
const errors = ref<Partial<Record<'certPem' | 'keyPem', string>>>({})
const submitting = ref(false)
const error = ref<unknown>(null)

watch(open, (value) => {
  if (!value) return
  Object.assign(form, { name: '', certPem: '', keyPem: '' })
  errors.value = {}
  error.value = null
})

const isPem = (value: string) => /-----BEGIN [A-Z0-9 ]+-----/.test(value)

const steps = computed(() => [t('certs.upload.steps.check'), t('certs.upload.steps.store')])

/** Reads a chosen file into a field; it leaves the browser only with the upload. */
async function pick(event: Event, field: 'certPem' | 'keyPem') {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  if (file.size > MAX_FILE_BYTES) {
    errors.value = { ...errors.value, [field]: t('certs.upload.tooBig') }
    return
  }
  form[field] = await file.text()
}

function validate(): boolean {
  const next: typeof errors.value = {}
  for (const field of ['certPem', 'keyPem'] as const) {
    const value = form[field].trim()
    if (!value) next[field] = validationMessage(field, 'required')
    else if (!isPem(value)) next[field] = validationMessage(field, 'pem')
  }
  errors.value = next
  return Object.keys(next).length === 0
}

async function submit() {
  if (!validate()) return
  submitting.value = true
  error.value = null
  try {
    await runCertJob(
      () => uploadCert({ name: form.name.trim() || undefined, certPem: form.certPem, keyPem: form.keyPem }),
      { rethrow: true },
    )
    open.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="t('certs.upload.title')"
    :description="t('certs.upload.intro')"
    :submit-label="t('certs.upload.submit')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <FormField id="upload-name" :label="t('common.field.name')" :hint="t('certs.upload.nameHint')">
      <template #default="field">
        <Input v-bind="field" v-model="form.name" maxlength="100" autocomplete="off" />
      </template>
    </FormField>

    <FormField id="upload-cert" :label="t('common.field.certPem')" :hint="t('certs.upload.certHint')" :error="errors.certPem">
      <template #default="field">
        <Textarea
          v-bind="field"
          v-model="form.certPem"
          spellcheck="false"
          dir="ltr"
          rows="6"
          class="max-h-60 font-mono text-xs"
          placeholder="-----BEGIN CERTIFICATE-----"
        />
      </template>
    </FormField>
    <label class="-mt-3 inline-flex cursor-pointer items-center gap-1.5 self-start rounded-sm text-detail text-link focus-within:ring-2 focus-within:ring-ring/50 hover:underline">
      <FileUp class="size-4" aria-hidden="true" />
      {{ t('certs.upload.chooseCert') }}
      <input type="file" accept=".pem,.crt,.cer,.txt" class="sr-only" @change="pick($event, 'certPem')" />
    </label>

    <FormField id="upload-key" :label="t('common.field.keyPem')" :hint="t('certs.upload.keyHint')" :error="errors.keyPem">
      <template #default="field">
        <Textarea
          v-bind="field"
          v-model="form.keyPem"
          spellcheck="false"
          dir="ltr"
          rows="6"
          class="max-h-60 font-mono text-xs"
          placeholder="-----BEGIN PRIVATE KEY-----"
        />
      </template>
    </FormField>
    <label class="-mt-3 inline-flex cursor-pointer items-center gap-1.5 self-start rounded-sm text-detail text-link focus-within:ring-2 focus-within:ring-ring/50 hover:underline">
      <FileUp class="size-4" aria-hidden="true" />
      {{ t('certs.upload.chooseKey') }}
      <input type="file" accept=".pem,.key,.txt" class="sr-only" @change="pick($event, 'keyPem')" />
    </label>

    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
