<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isStepUpRequired } from '@/api/client'
import { chmodEntries, type FileEntry } from '@/api/files'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import { DIR_MODE_MASK, FILE_MODE_MASK, octal } from '@/files/modes'
import { errorMessage } from '@/i18n/errors'
import { useFilesStore } from '@/stores/files'
import { entriesLabel } from './actions'
import ModeEditor from './ModeEditor.vue'

// "Permissions" (form sheet, a job): files and folders get modes of their
// own, also below the chosen folders when applied to everything inside.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ root: string; entries: FileEntry[] }>()

const { t } = useI18n()
const store = useFilesStore()
const fileMode = ref(0o644)
const dirMode = ref(0o755)
const recursive = ref(false)
const dirInvalid = ref(false)
const fileInvalid = ref(false)
const submitting = ref(false)
const error = ref<unknown>(null)

const hasDirs = computed(() => props.entries.some((entry) => entry.type === 'dir'))
const hasFiles = computed(() => props.entries.some((entry) => entry.type !== 'dir'))
/** Files inside the folders get a mode too when applied recursively. */
const showFiles = computed(() => hasFiles.value || recursive.value)
const name = computed(() => entriesLabel(props.entries.map((entry) => entry.path)))
/** Octal text in a shown editor that cannot be read. */
const modeInvalid = computed(() => (hasDirs.value && dirInvalid.value) || (showFiles.value && fileInvalid.value))
/**
 * The server gives folders the file mode when the folder mode is 0, so
 * "000" for folders can only go with files that get 000 too (or no files).
 */
const dirNoneRefused = computed(
  () => hasDirs.value && showFiles.value && (dirMode.value & DIR_MODE_MASK) === 0 && (fileMode.value & FILE_MODE_MASK) !== 0,
)

watch(open, (value) => {
  if (!value) return
  // A symbolic link's own mode is always 777: it says nothing about the files.
  const file = props.entries.find((entry) => entry.type !== 'dir' && entry.type !== 'link')
  const dir = props.entries.find((entry) => entry.type === 'dir')
  fileMode.value = file ? file.mode & FILE_MODE_MASK : 0o644
  dirMode.value = dir ? dir.mode & DIR_MODE_MASK : 0o755
  recursive.value = false
  error.value = null
})

const steps = computed(() => {
  if (!recursive.value) return []
  const count = props.entries.length
  return [
    t('files.chmod.stepDirs', { mode: octal(dirMode.value), count }, count),
    t('files.chmod.stepFiles', { mode: octal(fileMode.value & FILE_MODE_MASK) }),
  ]
})

async function submit() {
  if (modeInvalid.value || dirNoneRefused.value) return
  submitting.value = true
  error.value = null
  try {
    const paths = props.entries.map((entry) => entry.path)
    const mode = showFiles.value ? fileMode.value & FILE_MODE_MASK : dirMode.value & FILE_MODE_MASK
    const job = await store.runJob(
      () => chmodEntries(props.root, paths, mode, hasDirs.value ? dirMode.value : mode, recursive.value),
      { rethrow: true },
    )
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
    :title="entries.length === 1 ? t('files.chmod.title', { name }) : t('files.chmod.titleMany', { count: entries.length }, entries.length)"
    :description="t('files.chmod.hint')"
    :submit-label="t('files.chmod.submit')"
    :submitting="submitting"
    :submit-disabled="modeInvalid || dirNoneRefused"
    :steps="steps"
    @submit="submit"
  >
    <ModeEditor v-if="hasDirs" v-model:mode="dirMode" v-model:invalid="dirInvalid" dir :label="t('files.chmod.dirs')" />
    <ModeEditor v-if="showFiles" v-model:mode="fileMode" v-model:invalid="fileInvalid" :dir="false" :label="t('files.chmod.files')" />
    <Notice v-if="dirNoneRefused" tone="warning">{{ t('files.chmod.dirNoneRefused') }}</Notice>
    <SwitchField v-if="hasDirs" v-model="recursive" :label="t('files.chmod.recursive')" :hint="t('files.chmod.recursiveHint')" />
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
