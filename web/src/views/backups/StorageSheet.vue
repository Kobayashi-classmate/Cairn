<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { LoaderCircle, PlugZap } from '@lucide/vue'
import { isApiError, isStepUpRequired } from '@/api/client'
import { BUCKET_NAME, createStorage, testNewStorage, updateStorage, type StorageRequest, type StorageView } from '@/api/backups'
import { STORAGE_PROVIDERS, isEndpoint, providerOf } from '@/backups/providers'
import { reachesBeyondSites } from '@/backups/targets'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import SelectField from '@/components/common/SelectField.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import type { SelectOption } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { notifySuccess } from '@/lib/notify'
import { useBackupsStore } from '@/stores/backups'
import { useSessionStore } from '@/stores/session'

// "Add storage" / "Change storage" (form sheet, step-up protected): an
// S3-compatible bucket, with a connection test before saving. The secret
// key is never shown again; leaving it empty keeps the saved one (testing
// it against the address in the form then asks for the password too).
// A bucket that plans beyond the site directories use is changed by holders
// of `files.admin` only.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ storage: StorageView | null }>()

const { t } = useI18n()
const store = useBackupsStore()
const session = useSessionStore()

const provider = ref('aws')
const form = reactive({
  name: '',
  endpoint: '',
  region: '',
  bucket: '',
  prefix: '',
  accessKey: '',
  secretKey: '',
  pathStyle: false,
  insecure: false,
})
type Field = 'name' | 'endpoint' | 'region' | 'bucket' | 'prefix' | 'accessKey' | 'secretKey'
const errors = ref<Partial<Record<Field, string>>>({})
const submitting = ref(false)
const testing = ref(false)
const error = ref<unknown>(null)
const testedOk = ref(false)

/** What a connection test checks; a passed test no longer holds once it changes. */
const connection = computed(() =>
  JSON.stringify([provider.value, form.endpoint, form.region, form.bucket, form.prefix, form.accessKey, form.secretKey, form.pathStyle, form.insecure]),
)
watch(connection, () => {
  testedOk.value = false
})

const editing = computed(() => props.storage !== null)
/** Plans that back up beyond the site directories use this bucket, and this account may not open those. */
const lockedByPlans = computed(() => {
  const id = props.storage?.id
  if (!id || session.can('files.admin')) return false
  return (store.plans ?? []).some((plan) => plan.storageId === id && reachesBeyondSites(plan))
})
const current = computed(() => STORAGE_PROVIDERS.find((p) => p.id === provider.value) ?? STORAGE_PROVIDERS[0]!)
const providerOptions = computed<SelectOption[]>(() =>
  STORAGE_PROVIDERS.map((p) => ({ value: p.id, label: p.name || t('backups.storage.otherProvider') })),
)

watch(open, (value) => {
  if (!value) return
  const s = props.storage
  Object.assign(form, {
    name: s?.name ?? '',
    endpoint: s?.endpoint ?? '',
    region: s?.region ?? '',
    bucket: s?.bucket ?? '',
    prefix: s?.prefix ?? '',
    accessKey: s?.accessKey ?? '',
    secretKey: '',
    pathStyle: s?.pathStyle ?? false,
    insecure: s?.insecure ?? false,
  })
  provider.value = s ? (s.pathStyle && providerOf(s.endpoint) === 'other' ? 'minio' : providerOf(s.endpoint)) : 'aws'
  errors.value = {}
  error.value = null
  testedOk.value = false
})

function pickProvider(id: string) {
  provider.value = id
  // Path style follows the service, unless the user already set it.
  const p = STORAGE_PROVIDERS.find((entry) => entry.id === id)
  if (p && !editing.value) form.pathStyle = p.pathStyle
}

/** A folder inside the bucket ("" for none); surrounding slashes are dropped. */
function isPrefix(value: string): boolean {
  const prefix = value.trim().replace(/^\/+|\/+$/g, '')
  return prefix === '' || (prefix.length <= 200 && prefix.split('/').every((part) => part !== '' && part !== '.' && part !== '..'))
}

/** Checks the connection fields, and the name and secret when saving. */
function validate(saving: boolean): boolean {
  const next: Partial<Record<Field, string>> = {}
  if (saving) {
    if (!form.name.trim()) next.name = validationMessage('name', 'required')
    else if ([...form.name.trim()].length > 64) next.name = validationMessage('name', 'max_length', { max: 64 })
    if (!editing.value && !form.secretKey) next.secretKey = validationMessage('secretKey', 'required')
  }
  if (!form.endpoint.trim()) next.endpoint = validationMessage('endpoint', 'required')
  else if (!isEndpoint(form.endpoint)) next.endpoint = validationMessage('endpoint', 'endpoint')
  if (!form.bucket.trim()) next.bucket = validationMessage('bucket', 'required')
  else if (!BUCKET_NAME.test(form.bucket.trim())) next.bucket = validationMessage('bucket', 'bucket')
  if (form.region.length > 64 || /[ /]/.test(form.region)) next.region = validationMessage('region', 'region')
  if (!isPrefix(form.prefix)) next.prefix = validationMessage('prefix', 'path')
  if (!form.accessKey.trim()) next.accessKey = validationMessage('accessKey', 'required')
  errors.value = next
  return Object.keys(next).length === 0
}

/** Shows an error by its field where it belongs to one, otherwise below the form. */
function showError(err: unknown) {
  if (isApiError(err) && err.code === 'backup.endpoint_not_allowed') {
    errors.value = { ...errors.value, endpoint: errorMessage(err) }
    return
  }
  error.value = err
}

function body(): StorageRequest {
  return {
    name: form.name.trim(),
    endpoint: form.endpoint.trim(),
    region: form.region.trim(),
    bucket: form.bucket.trim(),
    prefix: form.prefix.trim().replace(/^\/+|\/+$/g, ''),
    accessKey: form.accessKey.trim(),
    secretKey: form.secretKey,
    pathStyle: form.pathStyle,
    insecure: form.insecure,
  }
}

async function test() {
  if (!validate(false)) return
  testing.value = true
  testedOk.value = false
  error.value = null
  const tested = connection.value
  try {
    // Without a new secret key, the saved storage's key is used.
    const { name: _name, ...settings } = body()
    // Step-up protected then: the client asks for the password and retries.
    await testNewStorage({ ...settings, ...(props.storage ? { id: props.storage.id } : {}) })
    // Edited while the test ran: it says nothing about the settings now.
    if (connection.value !== tested) return
    testedOk.value = true
    notifySuccess(t('backups.storage.testOk'))
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  } finally {
    testing.value = false
  }
}

async function submit() {
  if (!validate(true)) return
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    const saved = props.storage ? await updateStorage(props.storage.id, body()) : await createStorage(body())
    notifySuccess(t('backups.storage.saved', { name: saved.name }))
    void store.load()
    open.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="editing ? t('backups.storage.editTitle', { name: storage?.name ?? '' }) : t('backups.storage.createTitle')"
    :description="t('backups.storage.sheetIntro')"
    :submit-label="editing ? t('common.action.save') : t('backups.storage.add')"
    :submitting="submitting"
    :submit-disabled="lockedByPlans"
    @submit="submit"
  >
    <Notice v-if="lockedByPlans" tone="warning">{{ t('backups.storage.adminOnly') }}</Notice>
    <FormField id="storage-provider" :label="t('backups.storage.provider')" :hint="t(`backups.storage.hint.${current.id}`)">
      <template #default="field">
        <SelectField v-bind="field" :model-value="provider" :options="providerOptions" @update:model-value="pickProvider" />
      </template>
    </FormField>
    <FormField id="storage-name" :label="t('common.field.name')" :error="errors.name">
      <template #default="field">
        <Input v-bind="field" v-model="form.name" maxlength="64" autocomplete="off" />
      </template>
    </FormField>
    <FormField id="storage-endpoint" :label="t('common.field.endpoint')" :error="errors.endpoint">
      <template #default="field">
        <Input v-bind="field" v-model="form.endpoint" :placeholder="current.endpoint" autocomplete="off" spellcheck="false" autocapitalize="none" class="font-mono" />
      </template>
    </FormField>
    <div class="grid gap-5 sm:grid-cols-2">
      <FormField id="storage-region" :label="t('common.field.region')" :error="errors.region">
        <template #default="field">
          <Input v-bind="field" v-model="form.region" :placeholder="current.region" autocomplete="off" spellcheck="false" autocapitalize="none" class="font-mono" />
        </template>
      </FormField>
      <FormField id="storage-bucket" :label="t('common.field.bucket')" :error="errors.bucket">
        <template #default="field">
          <Input v-bind="field" v-model="form.bucket" autocomplete="off" spellcheck="false" autocapitalize="none" class="font-mono" />
        </template>
      </FormField>
    </div>
    <FormField id="storage-prefix" :label="t('common.field.prefix')" :error="errors.prefix">
      <template #default="field">
        <Input v-bind="field" v-model="form.prefix" :placeholder="t('backups.storage.prefixPlaceholder')" autocomplete="off" spellcheck="false" autocapitalize="none" class="font-mono" />
      </template>
    </FormField>
    <FormField id="storage-access" :label="t('common.field.accessKey')" :error="errors.accessKey">
      <template #default="field">
        <Input v-bind="field" v-model="form.accessKey" autocomplete="off" spellcheck="false" autocapitalize="none" class="font-mono" />
      </template>
    </FormField>
    <FormField id="storage-secret" :label="t('common.field.secretKey')" :error="errors.secretKey">
      <template #default="field">
        <PasswordInput
          v-bind="field"
          v-model="form.secretKey"
          autocomplete="new-password"
          :placeholder="editing && storage?.secretSet ? t('backups.storage.secretKeep') : ''"
        />
      </template>
    </FormField>
    <SwitchField v-model="form.pathStyle" :label="t('backups.storage.pathStyle')" />
    <SwitchField v-model="form.insecure" :label="t('backups.storage.insecure')" />
    <Notice v-if="form.insecure" tone="warning">{{ t('backups.storage.insecureWarning') }}</Notice>
    <Notice v-if="testedOk && !error" tone="success">{{ t('backups.storage.testOk') }}</Notice>
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>

    <template #footer-start>
      <Button type="button" variant="outline" class="me-auto max-md:h-11" :disabled="testing || submitting" @click="test">
        <LoaderCircle v-if="testing" class="animate-spin" aria-hidden="true" />
        <PlugZap v-else aria-hidden="true" />
        {{ t('backups.storage.testConnection') }}
      </Button>
    </template>
  </FormSheet>
</template>
