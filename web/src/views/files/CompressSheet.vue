<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isStepUpRequired } from '@/api/client'
import { ARCHIVE_FORMATS, compressEntries, type ArchiveFormat } from '@/api/files'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import { Input } from '@/components/ui/input'
import { absolutePath, archiveStem, isFileName, joinPath } from '@/files/paths'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { useFilesStore } from '@/stores/files'

// "Compress" (form sheet, a job): entries of one folder go into an archive
// next to them.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{
  root: string
  dir: string
  /** Names of entries of `dir`. */
  names: string[]
}>()

const { t } = useI18n()
const store = useFilesStore()
const format = ref<ArchiveFormat>('zip')
const name = ref('')
const fieldError = ref<string | null>(null)
const submitting = ref(false)
const error = ref<unknown>(null)

const formats = ARCHIVE_FORMATS.map((value) => ({ value, label: value }))

watch(open, (value) => {
  if (!value) return
  const base = props.names.length === 1 ? (props.names[0] ?? 'archive') : props.dir.split('/').pop() || 'archive'
  format.value = 'zip'
  name.value = `${base}.zip`
  fieldError.value = null
  error.value = null
})

/** The archive's name without the format's extension. */
function stem(): string {
  const suffix = `.${format.value}`
  return name.value.endsWith(suffix) ? name.value.slice(0, -suffix.length) : archiveStem(name.value)
}

function setFormat(next: ArchiveFormat) {
  const base = stem()
  format.value = next
  name.value = `${base}.${next}`
}

const steps = computed(() => {
  const count = props.names.length
  return [t('files.compress.step', { count, path: absolutePath(props.root, joinPath(props.dir, name.value || '…')) }, count)]
})

function validate(): boolean {
  const value = name.value
  if (!value) fieldError.value = validationMessage('name', 'required')
  else if (!isFileName(value)) fieldError.value = validationMessage('name', 'file_name')
  else if (!value.endsWith(`.${format.value}`) || value.length <= format.value.length + 1) {
    fieldError.value = validationMessage('name', 'archive_name')
  } else fieldError.value = null
  return fieldError.value === null
}

async function submit() {
  if (!validate()) return
  submitting.value = true
  error.value = null
  try {
    const job = await store.runJob(() => compressEntries(props.root, props.dir, props.names, format.value, name.value), {
      rethrow: true,
    })
    if (job) open.value = false
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
    :title="t('files.compress.title', { count: names.length }, names.length)"
    :submit-label="t('files.compress.submit')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <div class="flex flex-col gap-1.5">
      <span class="text-detail font-medium" aria-hidden="true">{{ t('common.field.format') }}</span>
      <SegmentedControl
        :model-value="format"
        :options="formats"
        :label="t('common.field.format')"
        class="w-full font-mono"
        @update:model-value="setFormat"
      />
    </div>
    <FormField id="compress-name" :label="t('common.field.name')" :error="fieldError">
      <template #default="field">
        <Input v-bind="field" v-model="name" autocomplete="off" autocapitalize="none" spellcheck="false" dir="auto" />
      </template>
    </FormField>
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
