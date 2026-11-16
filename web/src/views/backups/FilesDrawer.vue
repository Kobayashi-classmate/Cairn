<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// A plan's backups, here and in the bucket: download, restore, delete.
import { computed, h, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import {
  NAlert,
  NButton,
  NCheckbox,
  NDataTable,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NModal,
  NSpace,
  NTag,
  NText,
  type DataTableColumns,
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import {
  backupFileUrl,
  deleteBackupFiles,
  fetchBackupFiles,
  type BackupFile,
  type BackupPlan,
} from '@/api/backups'
import type { Job } from '@/api/jobs'
import { ensureElevated } from '@/auth/stepUp'
import JobProgress from '@/components/JobProgress.vue'
import { useErrorToast } from '@/composables/useErrorToast'
import { useJobRun } from '@/composables/useJobRun'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatBytes, formatDateTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'
import RestoreDialog from './RestoreDialog.vue'

const props = defineProps<{ plan: BackupPlan | null }>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const showError = useErrorToast()
const session = useSessionStore()
const files = ref<BackupFile[]>([])
const remoteError = ref<string>('')
const loading = ref(false)
const loadError = ref<unknown>(null)
const checked = ref<string[]>([])
const restoring = ref<BackupFile | null>(null)
const showRestore = ref(false)
const showDelete = ref(false)
const deleteRemote = ref(false)
/** A database backup restored here became a dump of this engine. */
const dumpEngine = ref<string | null>(null)

const canManage = computed(() => session.can('backups.manage'))
const remote = computed(() => !!props.plan?.storageId)

const { jobId, busy, run, follow, finished, dismiss } = useJobRun((job: Job) => {
  void load()
  // Database restores name the engine whose dumps now have the new one.
  if (job.kind === 'backup.restore' && job.state === 'succeeded' && typeof job.params.engine === 'string') {
    dumpEngine.value = job.params.engine
  }
})

async function load() {
  const plan = props.plan
  if (!plan) return
  loading.value = true
  try {
    const list = await fetchBackupFiles(plan.id)
    files.value = list.files
    remoteError.value = list.remoteError ? (errorText(list.remoteError.code, list.remoteError.params ?? {}) ?? list.remoteError.code) : ''
    loadError.value = null
    const names = new Set(files.value.map((file) => file.name))
    checked.value = checked.value.filter((name) => names.has(name))
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

watch(
  () => props.plan?.id,
  (id) => {
    files.value = []
    checked.value = []
    dumpEngine.value = null
    dismiss()
    if (id) void load()
  },
  { immediate: true },
)

async function download(file: BackupFile) {
  const plan = props.plan
  if (!plan) return
  try {
    // The download is step-up protected and goes through a plain link.
    await ensureElevated()
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
    return
  }
  // A copy only in the bucket comes from the bucket through a short-lived link.
  const link = document.createElement('a')
  link.href = backupFileUrl(plan.id, file.name)
  document.body.append(link)
  link.click()
  link.remove()
}

function restore(file: BackupFile) {
  restoring.value = file
  showRestore.value = true
}

function restoreStarted(job: Job) {
  dumpEngine.value = null
  follow(job)
}

function confirmDelete() {
  const plan = props.plan
  if (!plan) return
  const names = [...checked.value]
  showDelete.value = false
  // Step-up protected: the client asks for the password and retries.
  void run(() => deleteBackupFiles(plan.id, names, remote.value && deleteRemote.value))
}

function openDelete() {
  deleteRemote.value = remote.value
  showDelete.value = true
}

const columns = computed<DataTableColumns<BackupFile>>(() => [
  ...(canManage.value ? [{ type: 'selection' as const }] : []),
  {
    key: 'name',
    title: t('common.field.name'),
    minWidth: 260,
    render: (file) =>
      h('div', { class: 'cell' }, [
        h('span', { class: 'mono', dir: 'ltr' }, file.name),
        h(NSpace, { size: 4 }, () => [
          file.local ? h(NTag, { size: 'tiny', bordered: false }, () => t('backups.files.local')) : null,
          file.remote ? h(NTag, { size: 'tiny', type: 'info', bordered: false }, () => t('backups.files.remote')) : null,
          file.encrypted ? h(NTag, { size: 'tiny', type: 'success', bordered: false }, () => t('backups.files.encrypted')) : null,
        ]),
      ]),
  },
  { key: 'size', title: t('files.column.size'), width: 100, render: (file) => formatBytes(file.size) },
  { key: 'created', title: t('backups.files.created'), width: 170, render: (file) => formatDateTime(file.created) },
  {
    key: 'actions',
    title: '',
    width: 190,
    render: (file) =>
      canManage.value
        ? h(NSpace, { size: 4, wrap: false }, () => [
            h(NButton, { size: 'small', onClick: () => void download(file) }, () => t('files.action.download')),
            h(NButton, { size: 'small', onClick: () => restore(file) }, () => t('backups.files.restore')),
          ])
        : null,
  },
])
</script>

<template>
  <NDrawer :show="plan !== null" placement="right" width="min(960px, 100vw)" @update:show="(open: boolean) => !open && emit('close')">
    <NDrawerContent :title="plan ? t('backups.files.title', { name: plan.name }) : ''" closable :native-scrollbar="false">
      <div class="toolbar">
        <NButton v-if="canManage" size="small" type="error" ghost :disabled="checked.length === 0 || busy" @click="openDelete">
          {{ t('backups.files.delete', { count: checked.length }) }}
        </NButton>
        <span class="toolbar__spacer" />
        <NButton size="small" :loading="loading" @click="load">{{ t('common.action.refresh') }}</NButton>
      </div>

      <div v-if="jobId" class="job">
        <JobProgress :job-id="jobId" @finished="finished" />
        <NButton size="small" quaternary :disabled="busy" @click="dismiss">{{ t('sites.create.close') }}</NButton>
      </div>
      <NAlert v-if="dumpEngine" type="success" class="block">
        {{ t('backups.restore.dumpReady') }}
        <RouterLink :to="{ name: 'databases', query: { engine: dumpEngine, tab: 'dumps' } }">{{ t('backups.restore.openDumps') }}</RouterLink>
      </NAlert>

      <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
      <NAlert v-if="remoteError" type="warning" class="block">{{ t('backups.files.remoteError', { reason: remoteError }) }}</NAlert>
      <NEmpty v-if="!loading && !loadError && files.length === 0" :description="t('backups.files.empty')" class="empty" />
      <NDataTable
        v-else-if="files.length > 0"
        v-model:checked-row-keys="checked"
        :columns="columns"
        :data="files"
        :row-key="(file: BackupFile) => file.name"
        :loading="loading"
        :scroll-x="760"
        size="small"
      />
      <NText depth="3" tag="p" class="hint">{{ t('backups.files.hint') }}</NText>
    </NDrawerContent>
  </NDrawer>

  <RestoreDialog v-model:show="showRestore" :plan="plan" :file="restoring" @started="restoreStarted" />

  <NModal
    :show="showDelete"
    preset="card"
    :title="t('backups.files.deleteTitle', { count: checked.length }, checked.length)"
    class="delete-dialog"
    @update:show="(open: boolean) => (showDelete = open)"
  >
    <NText tag="p" class="intro">{{ t('backups.files.deleteText') }}</NText>
    <NCheckbox v-if="remote" v-model:checked="deleteRemote">{{ t('backups.files.deleteRemote') }}</NCheckbox>
    <div class="actions">
      <NButton @click="showDelete = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="error" @click="confirmDelete">{{ t('common.action.delete') }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-block-end: 12px;
}

.toolbar__spacer {
  flex: 1;
}

.block {
  margin-block-end: 12px;
}

.empty {
  padding-block: 40px;
}

.hint {
  margin-block: 12px 0;
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

.delete-dialog {
  inline-size: min(460px, calc(100vw - 32px));
}

.intro {
  margin-block: 0 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}

:deep(.cell) {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  overflow-wrap: anywhere;
}
</style>
