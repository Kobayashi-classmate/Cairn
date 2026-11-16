<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Extracts an archive into a directory. 7z and rar need a system tool
// (unar), which the Software page installs.
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { NAlert, NButton, NCheckbox, NModal, NText } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { extractArchive, type Archivers, type FileEntry } from '@/api/files'
import type { Job } from '@/api/jobs'
import { absolutePath, archiveKind, parentPath } from '@/files/paths'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'
import DirPicker from './DirPicker.vue'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ root: string; entry: FileEntry | null; archivers: Archivers }>()
const emit = defineEmits<{ started: [job: Job] }>()

const { t } = useI18n()
const session = useSessionStore()
const dest = ref('')
const overwrite = ref(false)
const submitting = ref(false)
const error = ref<unknown>(null)

watch(show, (open) => {
  if (!open || !props.entry) return
  dest.value = parentPath(props.entry.path)
  overwrite.value = false
  error.value = null
})

const kind = computed(() => (props.entry ? archiveKind(props.entry.name) : null))
/** 7z or rar without the tool that reads them. */
const toolMissing = computed(
  () => (kind.value === '7z' && !props.archivers.sevenZip) || (kind.value === 'rar' && !props.archivers.rar),
)

async function submit() {
  if (!props.entry) return
  submitting.value = true
  error.value = null
  try {
    emit('started', await extractArchive(props.root, props.entry.path, dest.value, overwrite.value))
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="t('files.extract.title', { name: entry?.name ?? '' })" class="extract-dialog">
    <NAlert v-if="toolMissing" type="warning" class="block">
      {{ t('files.extract.toolMissing') }}
      <RouterLink v-if="session.can('software.read')" :to="{ name: 'software' }">{{ t('files.extract.installTool') }}</RouterLink>
    </NAlert>
    <template v-else>
      <NText depth="3" tag="p" class="intro">{{ t('files.extract.pick') }}</NText>
      <DirPicker v-model:path="dest" :root="root" />
      <NCheckbox v-model:checked="overwrite" class="option">{{ t('files.extract.overwrite') }}</NCheckbox>
      <NText depth="3" tag="p" class="hint">{{ t('files.extract.hint') }}</NText>
    </template>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" :disabled="toolMissing" @click="submit">
        {{ t('files.extract.submit', { dir: absolutePath(root, dest) }) }}
      </NButton>
    </div>
  </NModal>
</template>

<style scoped>
.extract-dialog {
  inline-size: min(560px, calc(100vw - 32px));
}

.intro {
  margin-block: 0 8px;
  font-size: 13px;
}

.option {
  margin-block-start: 12px;
}

.hint {
  margin-block: 8px 0;
  font-size: 12px;
}

.block {
  margin-block-end: 12px;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
