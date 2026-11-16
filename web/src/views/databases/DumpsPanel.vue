<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NDataTable,
  NEmpty,
  NProgress,
  NSelect,
  NSpace,
  NText,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import {
  createDump,
  deleteDump,
  dumpUrl,
  fetchDumps,
  uploadDump,
  type DbEngine,
  type DumpInfo,
} from '@/api/databases'
import type { Job } from '@/api/jobs'
import { ensureElevated } from '@/auth/stepUp'
import JobProgress from '@/components/JobProgress.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { useJobRun } from '@/composables/useJobRun'
import { isUploadableDump } from '@/databases/validate'
import { errorMessage } from '@/i18n/errors'
import { formatBytes, formatDateTime } from '@/i18n/format'
import RestoreDialog from './RestoreDialog.vue'

const props = defineProps<{
  engine: DbEngine
  canManage: boolean
  /** Changes whenever the page reloads, so new dumps show up. */
  version: number
}>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()

const dumps = ref<DumpInfo[] | null>(null)
const loadError = ref<unknown>(null)
const dumpDatabase = ref<string | null>(null)
const uploading = ref<number | null>(null)
const showRestore = ref(false)
const restoreFile = ref<string | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)

const isRedis = computed(() => props.engine.kind === 'redis')
const { jobId, busy, run, follow, finished, dismiss } = useJobRun((_job: Job) => {
  void load()
  emit('changed')
})

async function load() {
  try {
    dumps.value = await fetchDumps(props.engine.id)
    loadError.value = null
  } catch (err) {
    loadError.value = err
  }
}

onMounted(load)
watch(() => props.version, load)

const databaseOptions = computed(() => props.engine.databases.map((db) => ({ value: db.name, label: db.name })))

function dump() {
  if (isRedis.value) void run(() => createDump(props.engine.id))
  else if (dumpDatabase.value) void run(() => createDump(props.engine.id, dumpDatabase.value ?? undefined))
}

/** Downloads through a plain link after re-authenticating, so large dumps stream to disk. */
async function download(entry: DumpInfo) {
  try {
    await ensureElevated()
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
    return
  }
  const link = document.createElement('a')
  link.href = dumpUrl(props.engine.id, entry.file)
  link.download = entry.file
  link.click()
}

async function remove(entry: DumpInfo) {
  const ok = await confirm({
    title: t('databases.dumps.deleteTitle', { file: entry.file }),
    content: t('databases.dumps.deleteContent'),
    confirmText: t('common.action.delete'),
    danger: true,
  })
  if (!ok) return
  try {
    await deleteDump(props.engine.id, entry.file)
    message.success(t('databases.dumps.deleted', { file: entry.file }))
    await load()
  } catch (err) {
    showError(err)
  }
}

function restore(entry: DumpInfo | null) {
  restoreFile.value = entry?.file ?? null
  showRestore.value = true
}

function pickUpload() {
  fileInput.value?.click()
}

async function onUpload(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  if (!isUploadableDump(props.engine.kind, file.name)) {
    message.error(t('databases.dumps.badName'))
    return
  }
  try {
    await ensureElevated()
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
    return
  }
  uploading.value = 0
  try {
    await uploadDump(props.engine.id, file, (fraction) => (uploading.value = fraction))
    message.success(t('databases.dumps.uploaded', { file: file.name }))
    await load()
  } catch (err) {
    showError(err)
  } finally {
    uploading.value = null
  }
}

const columns = computed<DataTableColumns<DumpInfo>>(() => [
  { key: 'file', title: t('databases.dumps.file'), minWidth: 240, render: (d) => h('span', { class: 'mono', dir: 'ltr' }, d.file) },
  ...(isRedis.value ? [] : [{ key: 'database', title: t('databases.restore.database'), width: 160 } as const]),
  { key: 'sizeBytes', title: t('databases.size'), width: 110, render: (d) => formatBytes(d.sizeBytes) },
  { key: 'createdAt', title: t('sites.overview.created'), width: 170, render: (d) => formatDateTime(d.createdAt) },
  {
    key: 'actions',
    title: '',
    width: 260,
    render: (d) =>
      props.canManage
        ? h(NSpace, { size: 4, wrap: false }, () => [
            h(NButton, { size: 'small', onClick: () => void download(d) }, () => t('common.action.download')),
            isRedis.value
              ? null
              : h(NButton, { size: 'small', quaternary: true, onClick: () => restore(d) }, () => t('databases.restore.submit')),
            h(NButton, { size: 'small', quaternary: true, type: 'error', onClick: () => void remove(d) }, () =>
              t('common.action.delete'),
            ),
          ])
        : null,
  },
])
</script>

<template>
  <div>
    <NText depth="3" tag="p" class="intro">
      {{ isRedis ? t('databases.dumps.redisIntro') : t('databases.dumps.intro') }}
    </NText>
    <div v-if="canManage" class="toolbar">
      <template v-if="isRedis">
        <NButton type="primary" :loading="busy" @click="dump">{{ t('databases.dumps.snapshot') }}</NButton>
      </template>
      <template v-else>
        <NSelect
          v-model:value="dumpDatabase"
          :options="databaseOptions"
          :placeholder="t('databases.restore.database')"
          class="toolbar__select"
        />
        <NButton type="primary" :loading="busy" :disabled="!dumpDatabase" @click="dump">{{ t('databases.dumps.create') }}</NButton>
        <NButton :disabled="uploading !== null" @click="pickUpload">{{ t('databases.dumps.upload') }}</NButton>
        <input ref="fileInput" type="file" accept=".sql,.gz,.dump" class="hidden" @change="onUpload" />
      </template>
    </div>
    <NProgress v-if="uploading !== null" type="line" :percentage="Math.round(uploading * 100)" class="block" />
    <div v-if="jobId" class="job">
      <JobProgress :job-id="jobId" @finished="finished" />
      <NButton size="small" quaternary @click="dismiss">{{ t('sites.create.close') }}</NButton>
    </div>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NDataTable
      v-if="!dumps || dumps.length > 0"
      :columns="columns"
      :data="dumps ?? []"
      :loading="!dumps && !loadError"
      :row-key="(d: DumpInfo) => d.file"
      :scroll-x="900"
      size="small"
    />
    <NEmpty v-else :description="t('databases.dumps.empty')" class="empty" />

    <RestoreDialog
      v-if="!isRedis"
      v-model:show="showRestore"
      :engine="engine"
      :dumps="dumps ?? []"
      :file="restoreFile"
      @started="follow"
    />
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
  gap: 8px;
  margin-block-end: 12px;
}

.toolbar__select {
  inline-size: 220px;
}

.hidden {
  display: none;
}

.block {
  margin-block-end: 12px;
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

.empty {
  padding-block: 24px;
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
}
</style>
