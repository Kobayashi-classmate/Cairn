<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { isStepUpRequired } from '@/api/client'
import { extractArchive, type Archivers, type FileEntry } from '@/api/files'
import DirBrowser from '@/components/common/DirBrowser.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import { absolutePath, archiveKind, parentPath } from '@/files/paths'
import { errorMessage } from '@/i18n/errors'
import { useFilesStore } from '@/stores/files'
import { useSessionStore } from '@/stores/session'

// "Extract" (form sheet, a job): an archive goes into a folder picked by
// browsing. 7z and rar need a system tool (unar) the Software page installs.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ root: string; entry: FileEntry | null; archivers: Archivers }>()

const { t } = useI18n()
const store = useFilesStore()
const session = useSessionStore()
const dest = ref('')
const overwrite = ref(false)
const submitting = ref(false)
const error = ref<unknown>(null)

watch(open, (value) => {
  if (!value || !props.entry) return
  dest.value = parentPath(props.entry.path)
  overwrite.value = false
  error.value = null
})

const kind = computed(() => (props.entry ? archiveKind(props.entry.name) : null))
/** 7z or rar without the tool that reads them. */
const toolMissing = computed(
  () => (kind.value === '7z' && !props.archivers.sevenZip) || (kind.value === 'rar' && !props.archivers.rar),
)

const steps = computed(() => {
  if (!props.entry || toolMissing.value) return []
  const list = [t('files.extract.stepInto', { name: props.entry.name, dir: absolutePath(props.root, dest.value) })]
  list.push(overwrite.value ? t('files.extract.stepOverwrite') : t('files.extract.stepKeep'))
  list.push(t('files.extract.stepSkip'))
  return list
})

async function submit() {
  const entry = props.entry
  if (!entry) return
  submitting.value = true
  error.value = null
  try {
    const job = await store.runJob(() => extractArchive(props.root, entry.path, dest.value, overwrite.value), { rethrow: true })
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
    :title="t('files.extract.title', { name: entry?.name ?? '' })"
    :description="t('files.extract.pick')"
    :submit-label="t('files.extract.submit')"
    :submitting="submitting"
    :submit-disabled="toolMissing"
    :steps="steps"
    @submit="submit"
  >
    <Notice v-if="toolMissing" tone="warning">
      {{ t('files.extract.toolMissing') }}
      <template v-if="session.can('software.read')" #action>
        <RouterLink :to="{ name: 'software' }" class="text-current underline underline-offset-2 hover:no-underline">
          {{ t('files.extract.installTool') }}
        </RouterLink>
      </template>
    </Notice>
    <template v-else>
      <DirBrowser v-model:path="dest" :root="root" :label="t('files.extract.dest')" />
      <SwitchField v-model="overwrite" :label="t('files.extract.overwrite')" />
    </template>
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
