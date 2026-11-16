<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Copies or moves entries into a directory of the same root.
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCheckbox, NModal, NText } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { copyEntries, moveEntries } from '@/api/files'
import type { Job } from '@/api/jobs'
import { absolutePath, baseName, parentPath } from '@/files/paths'
import { errorMessage } from '@/i18n/errors'
import DirPicker from './DirPicker.vue'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{
  mode: 'copy' | 'move'
  root: string
  /** Where the dialog starts browsing. */
  dir: string
  paths: string[]
}>()
const emit = defineEmits<{ started: [job: Job] }>()

const { t } = useI18n()
const dest = ref('')
const overwrite = ref(false)
const submitting = ref(false)
const error = ref<unknown>(null)

watch(show, (open) => {
  if (!open) return
  dest.value = props.dir
  overwrite.value = false
  error.value = null
})

const summary = computed(() =>
  props.paths.length === 1 ? baseName(props.paths[0] ?? '') : t('files.selection.count', { count: props.paths.length }, props.paths.length),
)
/** A directory cannot go into itself. */
const intoItself = computed(() => props.paths.some((p) => dest.value === p || dest.value.startsWith(`${p}/`)))
const sameDir = computed(() => props.mode === 'move' && props.paths.every((p) => parentPath(p) === dest.value))

async function submit() {
  submitting.value = true
  error.value = null
  try {
    const start = props.mode === 'copy' ? copyEntries : moveEntries
    emit('started', await start(props.root, props.paths, dest.value, overwrite.value))
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
    :title="mode === 'copy' ? t('files.transfer.copyTitle', { name: summary }) : t('files.transfer.moveTitle', { name: summary })"
    class="transfer-dialog"
  >
    <NText depth="3" tag="p" class="intro">{{ t('files.transfer.pick') }}</NText>
    <DirPicker v-model:path="dest" :root="root" />
    <NCheckbox v-model:checked="overwrite" class="option">{{ t('files.transfer.overwrite') }}</NCheckbox>
    <NAlert v-if="intoItself" type="warning" class="block">{{ t('errors.file.into_itself') }}</NAlert>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" :disabled="intoItself || sameDir" @click="submit">
        {{ mode === 'copy' ? t('files.transfer.copyHere', { dir: absolutePath(root, dest) }) : t('files.transfer.moveHere', { dir: absolutePath(root, dest) }) }}
      </NButton>
    </div>
  </NModal>
</template>

<style scoped>
.transfer-dialog {
  inline-size: min(560px, calc(100vw - 32px));
}

.intro {
  margin-block: 0 8px;
  font-size: 13px;
}

.option {
  margin-block-start: 12px;
}

.block {
  margin-block-start: 12px;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
