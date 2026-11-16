<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Changes permissions: files and directories get modes of their own, also
// below the chosen directories when applied recursively.
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NFormItem, NModal, NSwitch, NText } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { chmodEntries, type FileEntry } from '@/api/files'
import type { Job } from '@/api/jobs'
import { DIR_MODE_MASK, FILE_MODE_MASK } from '@/files/modes'
import { errorMessage } from '@/i18n/errors'
import ModeEditor from './ModeEditor.vue'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ root: string; entries: FileEntry[] }>()
const emit = defineEmits<{ started: [job: Job] }>()

const { t } = useI18n()
const fileMode = ref(0o644)
const dirMode = ref(0o755)
const recursive = ref(false)
const submitting = ref(false)
const error = ref<unknown>(null)

const hasDirs = computed(() => props.entries.some((entry) => entry.type === 'dir'))
const hasFiles = computed(() => props.entries.some((entry) => entry.type !== 'dir'))
const showFiles = computed(() => hasFiles.value || recursive.value)

watch(show, (open) => {
  if (!open) return
  const file = props.entries.find((entry) => entry.type !== 'dir')
  const dir = props.entries.find((entry) => entry.type === 'dir')
  fileMode.value = file ? file.mode & FILE_MODE_MASK : 0o644
  dirMode.value = dir ? dir.mode & DIR_MODE_MASK : 0o755
  recursive.value = false
  error.value = null
})

const title = computed(() =>
  props.entries.length === 1
    ? t('files.chmod.titleOne', { name: props.entries[0]?.name ?? '' })
    : t('files.chmod.titleMany', { count: props.entries.length }, props.entries.length),
)

async function submit() {
  submitting.value = true
  error.value = null
  try {
    const paths = props.entries.map((entry) => entry.path)
    const mode = showFiles.value ? fileMode.value & FILE_MODE_MASK : dirMode.value & FILE_MODE_MASK
    emit('started', await chmodEntries(props.root, paths, mode, hasDirs.value ? dirMode.value : mode, recursive.value))
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="title" class="chmod-dialog">
    <div class="modes">
      <NFormItem v-if="hasDirs" :label="t('files.chmod.dirs')" :show-feedback="false">
        <ModeEditor v-model:mode="dirMode" dir />
      </NFormItem>
      <NFormItem v-if="showFiles" :label="t('files.chmod.files')" :show-feedback="false">
        <ModeEditor v-model:mode="fileMode" :dir="false" />
      </NFormItem>
    </div>
    <div v-if="hasDirs" class="option">
      <NSwitch v-model:value="recursive" />
      <span>{{ t('files.chmod.recursive') }}</span>
    </div>
    <NText depth="3" tag="p" class="hint">{{ t('files.chmod.hint') }}</NText>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" @click="submit">{{ t('common.action.save') }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.chmod-dialog {
  inline-size: min(600px, calc(100vw - 32px));
}

.modes {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 32px;
}

.option {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-block-start: 12px;
}

.hint {
  margin-block: 8px 0;
  font-size: 12px;
}

.block {
  margin-block-start: 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
