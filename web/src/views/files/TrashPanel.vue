<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// A root's trash: put entries back, or delete them for good.
import { computed, h, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCheckbox, NDataTable, NEmpty, NIcon, NSpace, NText, type DataTableColumns } from 'naive-ui'
import { DocumentOutline, FolderOutline, LinkOutline } from '@vicons/ionicons5'
import { isAbortError } from '@/api/client'
import { emptyTrash, fetchTrash, purgeTrash, restoreTrash, type TrashEntry } from '@/api/files'
import type { Job } from '@/api/jobs'
import JobProgress from '@/components/JobProgress.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useJobRun } from '@/composables/useJobRun'
import { absolutePath } from '@/files/paths'
import { errorMessage } from '@/i18n/errors'
import { formatBytes, formatDateTime } from '@/i18n/format'

const props = defineProps<{ root: string; canManage: boolean }>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const confirm = useConfirm()
const entries = ref<TrashEntry[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)
const checked = ref<string[]>([])
const overwrite = ref(false)
let controller: AbortController | null = null

const { jobId, busy, run, finished, dismiss } = useJobRun((_job: Job) => {
  void load()
  emit('changed')
})

async function load() {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  try {
    entries.value = await fetchTrash(props.root, current.signal)
    loadError.value = null
    const ids = new Set(entries.value.map((entry) => entry.id))
    checked.value = checked.value.filter((id) => ids.has(id))
  } catch (err) {
    if (!isAbortError(err)) loadError.value = err
  } finally {
    if (controller === current) loading.value = false
  }
}

watch(
  () => props.root,
  () => {
    checked.value = []
    void load()
  },
  { immediate: true },
)
onBeforeUnmount(() => controller?.abort())

const totalSize = computed(() => entries.value.reduce((sum, entry) => sum + entry.size, 0))

function restore() {
  void run(() => restoreTrash(props.root, checked.value, overwrite.value))
}

async function purge() {
  const count = checked.value.length
  const ok = await confirm({
    title: t('files.trash.purgeTitle', { count }, count),
    content: t('files.trash.purgeText'),
    confirmText: t('files.trash.purge'),
    danger: true,
  })
  // Step-up protected: the client asks for the password and retries.
  if (ok) void run(() => purgeTrash(props.root, checked.value))
}

async function empty() {
  const ok = await confirm({
    title: t('files.trash.emptyTitle'),
    content: t('files.trash.purgeText'),
    confirmText: t('files.trash.empty'),
    danger: true,
  })
  if (ok) void run(() => emptyTrash(props.root))
}

const columns = computed<DataTableColumns<TrashEntry>>(() => [
  ...(props.canManage ? [{ type: 'selection' as const }] : []),
  {
    key: 'path',
    title: t('files.trash.from'),
    minWidth: 260,
    render: (entry) =>
      h('span', { class: 'trash__name' }, [
        h(NIcon, { size: 16 }, () => h(entry.type === 'dir' ? FolderOutline : entry.type === 'link' ? LinkOutline : DocumentOutline)),
        h('span', { class: 'mono', dir: 'ltr' }, absolutePath(props.root, entry.path)),
      ]),
  },
  { key: 'size', title: t('files.column.size'), width: 110, render: (entry) => (entry.type === 'dir' ? '-' : formatBytes(entry.size)) },
  { key: 'user', title: t('files.trash.by'), width: 130, render: (entry) => entry.user || '-' },
  { key: 'deletedAt', title: t('files.trash.deletedAt'), width: 170, render: (entry) => formatDateTime(entry.deletedAt) },
])
</script>

<template>
  <div class="trash">
    <NText depth="3" tag="p" class="intro">{{ t('files.trash.intro') }}</NText>

    <div v-if="jobId" class="job">
      <JobProgress :job-id="jobId" @finished="finished" />
      <NButton size="small" quaternary @click="dismiss">{{ t('sites.create.close') }}</NButton>
    </div>

    <div v-if="canManage" class="toolbar">
      <NSpace :size="8" align="center">
        <NButton type="primary" :disabled="checked.length === 0 || busy" @click="restore">
          {{ t('files.trash.restore') }}
        </NButton>
        <NCheckbox v-model:checked="overwrite">{{ t('files.trash.overwrite') }}</NCheckbox>
      </NSpace>
      <NSpace :size="8">
        <NButton type="error" ghost :disabled="checked.length === 0 || busy" @click="purge">{{ t('files.trash.purge') }}</NButton>
        <NButton type="error" quaternary :disabled="entries.length === 0 || busy" @click="empty">{{ t('files.trash.empty') }}</NButton>
      </NSpace>
    </div>

    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NDataTable
      v-if="entries.length > 0"
      v-model:checked-row-keys="checked"
      :columns="columns"
      :data="entries"
      :row-key="(entry: TrashEntry) => entry.id"
      :loading="loading"
      :scroll-x="720"
      size="small"
    />
    <NEmpty v-else-if="!loading" :description="t('files.trash.emptyList')" class="empty" />
    <NText v-if="entries.length > 0" depth="3" tag="p" class="total">
      {{ t('files.trash.total', { count: entries.length, size: formatBytes(totalSize) }, entries.length) }}
    </NText>
  </div>
</template>

<style scoped>
.intro {
  margin-block: 0 12px;
  font-size: 13px;
}

.toolbar {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  gap: 8px;
  margin-block-end: 12px;
}

.block {
  margin-block-end: 12px;
}

.empty {
  padding-block: 40px;
}

.total {
  margin-block: 8px 0;
  font-size: 12px;
}

.job {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 4px;
  margin-block-end: 12px;
}

.job > :first-child {
  align-self: stretch;
}

:deep(.trash__name) {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
  overflow-wrap: anywhere;
}
</style>
