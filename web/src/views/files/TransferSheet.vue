<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isStepUpRequired } from '@/api/client'
import { copyEntries, moveEntries } from '@/api/files'
import DirBrowser from '@/components/common/DirBrowser.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import { absolutePath, parentPath } from '@/files/paths'
import { errorMessage } from '@/i18n/errors'
import { useFilesStore } from '@/stores/files'
import { entriesLabel } from './actions'

// "Copy to…" / "Move to…" (form sheet, a job): entries go into a folder of
// the same root, picked by browsing.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{
  mode: 'copy' | 'move'
  root: string
  /** Where browsing starts (the folder shown). */
  dir: string
  paths: string[]
}>()

const { t } = useI18n()
const store = useFilesStore()
const dest = ref('')
const overwrite = ref(false)
const submitting = ref(false)
const error = ref<unknown>(null)

watch(open, (value) => {
  if (!value) return
  dest.value = props.dir
  overwrite.value = false
  error.value = null
})

const name = computed(() => entriesLabel(props.paths))
/** A folder cannot go into itself. */
const intoItself = computed(() => props.paths.some((p) => dest.value === p || dest.value.startsWith(`${p}/`)))
/** Moving entries to where they are does nothing. */
const sameDir = computed(() => props.mode === 'move' && props.paths.every((p) => parentPath(p) === dest.value))
const target = computed(() => absolutePath(props.root, dest.value))

const steps = computed(() => {
  const count = props.paths.length
  const list: string[] = []
  if (props.mode === 'move') {
    list.push(t('files.transfer.stepMove', { count, from: absolutePath(props.root, props.dir), to: target.value }, count))
  } else {
    list.push(t('files.transfer.stepCopy', { count, to: target.value }, count))
  }
  if (overwrite.value) list.push(t('files.transfer.stepOverwrite'))
  return list
})

async function submit() {
  submitting.value = true
  error.value = null
  try {
    const start = props.mode === 'copy' ? copyEntries : moveEntries
    const job = await store.runJob(() => start(props.root, props.paths, dest.value, overwrite.value), { rethrow: true })
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
    :title="mode === 'copy' ? t('files.transfer.copyTitle', { name }) : t('files.transfer.moveTitle', { name })"
    :description="t('files.transfer.pick')"
    :submit-label="mode === 'copy' ? t('files.transfer.copySubmit') : t('files.transfer.moveSubmit')"
    :submitting="submitting"
    :submit-disabled="intoItself || sameDir"
    :steps="steps"
    @submit="submit"
  >
    <DirBrowser v-model:path="dest" :root="root" :label="t('files.transfer.dest')" />
    <SwitchField v-model="overwrite" :label="t('files.transfer.overwrite')" />
    <Notice v-if="intoItself" tone="warning">{{ t('errors.file.into_itself') }}</Notice>
    <Notice v-else-if="sameDir" tone="info">{{ t('files.transfer.sameDir') }}</Notice>
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
