<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Adds or changes an S3-compatible bucket (step-up protected). The secret
// key is never shown again; leaving it empty keeps the saved one.
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NSelect,
  NSwitch,
  NText,
  useMessage,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import {
  BUCKET_NAME,
  createStorage,
  testNewStorage,
  updateStorage,
  type StorageRequest,
  type StorageView,
} from '@/api/backups'
import { STORAGE_PROVIDERS, isEndpoint, providerOf } from '@/backups/providers'
import { checkRule, requiredRule } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ storage: StorageView | null }>()
const emit = defineEmits<{ saved: [storage: StorageView] }>()

const { t } = useI18n()
const message = useMessage()
const formRef = ref<FormInst | null>(null)
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
const submitting = ref(false)
const testing = ref(false)
const error = ref<unknown>(null)

const editing = computed(() => props.storage !== null)
const current = computed(() => STORAGE_PROVIDERS.find((p) => p.id === provider.value) ?? STORAGE_PROVIDERS[0]!)
const providerOptions = computed(() =>
  STORAGE_PROVIDERS.map((p) => ({ value: p.id, label: p.name || t('backups.storage.otherProvider') })),
)

watch(show, async (open) => {
  if (!open) return
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
  error.value = null
  await nextTick()
  formRef.value?.restoreValidation()
})

function pickProvider(id: string) {
  provider.value = id
  // Path style follows the service, unless the user already set it.
  const p = STORAGE_PROVIDERS.find((entry) => entry.id === id)
  if (p && !editing.value) form.pathStyle = p.pathStyle
}

const rules = computed<FormRules>(() => ({
  name: [requiredRule('name'), checkRule('name', 'max_length', (v) => [...v.trim()].length <= 64, { max: 64 })],
  endpoint: [requiredRule('endpoint'), checkRule('endpoint', 'endpoint', isEndpoint)],
  bucket: [requiredRule('bucket'), checkRule('bucket', 'bucket', (v) => BUCKET_NAME.test(v.trim()))],
  region: [checkRule('region', 'region', (v) => v.length <= 64 && !/[ /]/.test(v))],
  prefix: [checkRule('prefix', 'path', isPrefix)],
  accessKey: [requiredRule('accessKey')],
  secretKey: editing.value ? [] : [requiredRule('secretKey')],
}))

/** A folder inside the bucket ("" for none); surrounding slashes are dropped. */
function isPrefix(value: string): boolean {
  const prefix = value.trim().replace(/^\/+|\/+$/g, '')
  return prefix === '' || (prefix.length <= 200 && prefix.split('/').every((part) => part !== '' && part !== '.' && part !== '..'))
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
  // The name is not needed for a check; the connection fields are.
  const ready = isEndpoint(form.endpoint) && BUCKET_NAME.test(form.bucket.trim()) && form.accessKey.trim() !== '' && isPrefix(form.prefix)
  if (!ready) {
    formRef.value?.validate().catch(() => {})
    return
  }
  testing.value = true
  error.value = null
  try {
    // Without a new secret key, the saved storage's key is used.
    const { name: _name, ...settings } = body()
    await testNewStorage({ ...settings, ...(props.storage ? { id: props.storage.id } : {}) })
    message.success(t('backups.storage.testOk'))
  } catch (err) {
    error.value = err
  } finally {
    testing.value = false
  }
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
    // Step-up protected: the client asks for the password and retries.
    const saved = props.storage ? await updateStorage(props.storage.id, body()) : await createStorage(body())
    emit('saved', saved)
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    :title="editing ? t('backups.storage.editTitle', { name: storage?.name ?? '' }) : t('backups.storage.createTitle')"
    class="storage-dialog"
    :mask-closable="false"
  >
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting" label-placement="top">
      <NFormItem :label="t('backups.storage.provider')" :show-feedback="false" class="gap">
        <div class="field">
          <NSelect :value="provider" :options="providerOptions" @update:value="pickProvider" />
          <NText depth="3" class="hint">{{ t(`backups.storage.hint.${current.id}`) }}</NText>
        </div>
      </NFormItem>
      <NFormItem path="name" :label="t('common.field.name')">
        <NInput v-model:value="form.name" :maxlength="64" :input-props="{ autocomplete: 'off' }" />
      </NFormItem>
      <div class="grid">
        <NFormItem path="endpoint" :label="t('common.field.endpoint')">
          <NInput v-model:value="form.endpoint" :placeholder="current.endpoint" :input-props="{ autocomplete: 'off', spellcheck: false }" />
        </NFormItem>
        <NFormItem path="region" :label="t('common.field.region')">
          <NInput v-model:value="form.region" :placeholder="current.region" :input-props="{ autocomplete: 'off', spellcheck: false }" />
        </NFormItem>
        <NFormItem path="bucket" :label="t('common.field.bucket')">
          <NInput v-model:value="form.bucket" :input-props="{ autocomplete: 'off', spellcheck: false }" />
        </NFormItem>
        <NFormItem path="prefix" :label="t('common.field.prefix')">
          <NInput v-model:value="form.prefix" :placeholder="t('backups.storage.prefixPlaceholder')" :input-props="{ autocomplete: 'off', spellcheck: false }" />
        </NFormItem>
        <NFormItem path="accessKey" :label="t('common.field.accessKey')">
          <NInput v-model:value="form.accessKey" :input-props="{ autocomplete: 'off', spellcheck: false }" />
        </NFormItem>
        <NFormItem path="secretKey" :label="t('common.field.secretKey')">
          <NInput
            v-model:value="form.secretKey"
            type="password"
            show-password-on="click"
            :placeholder="editing && storage?.secretSet ? t('backups.storage.secretKeep') : ''"
            :input-props="{ autocomplete: 'new-password' }"
          />
        </NFormItem>
      </div>
      <div class="switches">
        <label class="switch">
          <NSwitch v-model:value="form.pathStyle" />
          <span>{{ t('backups.storage.pathStyle') }}</span>
        </label>
        <label class="switch">
          <NSwitch v-model:value="form.insecure" />
          <span>{{ t('backups.storage.insecure') }}</span>
        </label>
      </div>
      <NAlert v-if="form.insecure" type="warning" class="block">{{ t('backups.storage.insecureWarning') }}</NAlert>
    </NForm>
    <NAlert v-if="error" type="error" class="block">
      {{ errorMessage(error) }}
    </NAlert>
    <div class="actions">
      <NButton :loading="testing" :disabled="submitting" @click="test">{{ t('backups.storage.test') }}</NButton>
      <span class="actions__spacer" />
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" @click="submit">{{ t('common.action.save') }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.storage-dialog {
  inline-size: min(680px, calc(100vw - 32px));
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

.gap {
  margin-block-end: 16px;
}

.grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  column-gap: 16px;
}

.switches {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 24px;
  margin-block-end: 12px;
}

.switch {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}

.block {
  margin-block-end: 12px;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.actions__spacer {
  flex: 1;
}
</style>
