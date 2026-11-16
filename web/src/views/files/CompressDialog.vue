<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Packs entries of one directory into an archive next to them.
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NForm, NFormItem, NInput, NModal, NRadioButton, NRadioGroup, type FormInst, type FormRules } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { ARCHIVE_FORMATS, compressEntries, type ArchiveFormat } from '@/api/files'
import type { Job } from '@/api/jobs'
import { archiveStem, isFileName } from '@/files/paths'
import { checkRule, requiredRule } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{
  root: string
  dir: string
  /** Names of entries of `dir`. */
  names: string[]
}>()
const emit = defineEmits<{ started: [job: Job] }>()

const { t } = useI18n()
const formRef = ref<FormInst | null>(null)
const form = reactive<{ format: ArchiveFormat; name: string }>({ format: 'zip', name: '' })
const submitting = ref(false)
const error = ref<unknown>(null)

/** The archive's name without the format's extension. */
function stem(): string {
  const suffix = `.${form.format}`
  return form.name.endsWith(suffix) ? form.name.slice(0, -suffix.length) : archiveStem(form.name)
}

watch(show, (open) => {
  if (!open) return
  const base = props.names.length === 1 ? (props.names[0] ?? 'archive') : props.dir.split('/').pop() || 'archive'
  form.format = 'zip'
  form.name = `${base}.zip`
  error.value = null
})

function setFormat(format: ArchiveFormat) {
  const base = stem()
  form.format = format
  form.name = `${base}.${format}`
}

const rules = computed<FormRules>(() => ({
  name: [
    requiredRule('name'),
    checkRule('name', 'file_name', isFileName),
    checkRule('name', 'archive_name', (v) => v.endsWith(`.${form.format}`) && v.length > form.format.length + 1),
  ],
}))

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  submitting.value = true
  error.value = null
  try {
    emit('started', await compressEntries(props.root, props.dir, props.names, form.format, form.name))
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
    :title="t('files.compress.title', { count: names.length }, names.length)"
    class="compress-dialog"
  >
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting">
      <NFormItem :label="t('common.field.format')" :show-feedback="false" class="format">
        <NRadioGroup :value="form.format" @update:value="setFormat">
          <NRadioButton v-for="format in ARCHIVE_FORMATS" :key="format" :value="format">{{ format }}</NRadioButton>
        </NRadioGroup>
      </NFormItem>
      <NFormItem path="name" :label="t('common.field.name')">
        <NInput v-model:value="form.name" :input-props="{ autocomplete: 'off', spellcheck: false }" @keydown.enter.prevent="submit" />
      </NFormItem>
    </NForm>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" @click="submit">{{ t('files.compress.submit') }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.compress-dialog {
  inline-size: min(480px, calc(100vw - 32px));
}

.format {
  margin-block-end: 16px;
}

.block {
  margin-block-end: 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
