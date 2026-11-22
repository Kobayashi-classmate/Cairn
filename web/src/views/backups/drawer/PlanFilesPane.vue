<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { Download, RefreshCw, RotateCcw, Trash2 } from '@lucide/vue'
import { isAbortError } from '@/api/client'
import { fetchBackupFiles, type BackupFile, type BackupPlan } from '@/api/backups'
import type { Job } from '@/api/jobs'
import DataTable from '@/components/common/DataTable.vue'
import Notice from '@/components/common/Notice.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import { errorText } from '@/i18n/errors'
import { formatBytes } from '@/i18n/format'
import { askDeleteFiles, canTakeBackup, downloadBackup } from '../actions'
import RestoreSheet from '../RestoreSheet.vue'

// A plan's backups, on this server and in the bucket: download, restore
// with its options, or select several to delete (the plan's name is typed).
// Backups of folders beyond the sites root (by their names) are downloaded
// and restored by those who may open those folders.
const props = defineProps<{ plan: BackupPlan; canManage: boolean }>()

const { t } = useI18n()
const files = ref<BackupFile[]>([])
const loaded = ref(false)
const loading = ref(false)
const error = ref<unknown>(null)
const remoteError = ref('')
const selection = ref<string[]>([])
const search = ref('')
const restoring = ref<BackupFile | null>(null)
const showRestore = ref(false)
/** A database backup restored here became a dump of this engine. */
const dumpEngine = ref<string | null>(null)
let controller: AbortController | null = null

async function load() {
  controller?.abort()
  const request = new AbortController()
  controller = request
  loading.value = true
  try {
    const list = await fetchBackupFiles(props.plan.id, request.signal)
    files.value = list.files
    remoteError.value = list.remoteError ? (errorText(list.remoteError.code, list.remoteError.params ?? {}) ?? list.remoteError.code) : ''
    loaded.value = true
    error.value = null
  } catch (err) {
    if (isAbortError(err)) return
    error.value = err
  } finally {
    if (controller === request) {
      controller = null
      loading.value = false
    }
  }
}

onMounted(load)
onBeforeUnmount(() => controller?.abort())
watch(() => props.plan.lastRun?.finished, (now, before) => {
  if (now !== before) void load()
})

/** Some backups here cannot be downloaded or restored by this account (see canTakeBackup). */
const someLocked = computed(() => props.canManage && files.value.some((file) => !canTakeBackup(file)))

const columns = computed<ColumnDef<BackupFile>[]>(() => [
  { id: 'name', header: t('common.field.name'), accessorFn: (file) => file.name, meta: { class: 'min-w-[260px]' } },
  { id: 'size', header: t('files.column.size'), accessorFn: (file) => file.size, meta: { class: 'w-[100px] tabular-nums' } },
  { id: 'created', header: t('backups.files.created'), accessorFn: (file) => file.created, meta: { class: 'w-[140px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[200px] text-end' } },
])

function restore(file: BackupFile) {
  restoring.value = file
  showRestore.value = true
}

function onRestored(job: Job) {
  // Database restores name the engine whose dumps now have the new one.
  if (job.state === 'succeeded' && typeof job.params.engine === 'string') dumpEngine.value = job.params.engine
}

async function remove(names: string[], clear: () => void) {
  if (await askDeleteFiles(props.plan, names, () => void load())) clear()
}
</script>

<template>
  <Notice v-if="dumpEngine" tone="success" dismissible @dismiss="dumpEngine = null">
    {{ t('backups.restore.dumpReady') }}
    <template #action>
      <RouterLink :to="{ name: 'databases', query: { tab: dumpEngine, view: 'dumps' } }" class="text-current underline underline-offset-2">
        {{ t('backups.restore.openDumps') }}
      </RouterLink>
    </template>
  </Notice>
  <Notice v-if="remoteError" tone="warning">{{ t('backups.files.remoteError', { reason: remoteError }) }}</Notice>
  <Notice v-if="someLocked" tone="info">{{ t('backups.files.adminOnly') }}</Notice>

  <DataTable
    v-model:search="search"
    v-model:selection="selection"
    :data="files"
    :columns="columns"
    :row-key="(file) => file.name"
    :label="t('backups.action.files')"
    :loading="loading && !loaded"
    :error="error"
    :search-placeholder="t('backups.files.search')"
    :search-text="(file) => `${file.name} ${file.target}`"
    :selectable="canManage"
    :empty-title="t('backups.files.empty')"
    :min-width="680"
    :initial-sort="[{ id: 'created', desc: true }]"
    @retry="load"
  >
    <template #toolbar>
      <Button variant="ghost" size="sm" :disabled="loading" @click="load">
        <RefreshCw :class="loading ? 'animate-spin' : ''" aria-hidden="true" />
        {{ t('common.action.refresh') }}
      </Button>
    </template>
    <template #cell-name="{ row }">
      <span class="flex min-w-0 flex-col gap-1">
        <span class="font-mono text-[12.5px] [overflow-wrap:anywhere]" dir="ltr">{{ row.name }}</span>
        <span class="flex flex-wrap gap-1">
          <StatusPill v-if="row.local">{{ t('backups.files.local') }}</StatusPill>
          <StatusPill v-if="row.remote" tone="progress">{{ t('backups.files.remote') }}</StatusPill>
          <StatusPill v-if="row.encrypted" tone="success">{{ t('backups.files.encrypted') }}</StatusPill>
        </span>
      </span>
    </template>
    <template #cell-size="{ row }">{{ formatBytes(row.size) }}</template>
    <template #cell-created="{ row }"><RelativeTime :value="row.created" /></template>
    <template #cell-actions="{ row }">
      <div v-if="canManage && canTakeBackup(row)" class="flex justify-end gap-2">
        <Button variant="outline" size="sm" class="max-md:h-11" @click="downloadBackup(plan, row)">
          <Download aria-hidden="true" />
          {{ t('common.action.download') }}
        </Button>
        <Button variant="outline" size="sm" class="max-md:h-11" @click="restore(row)">
          <RotateCcw aria-hidden="true" />
          {{ t('backups.files.restore') }}
        </Button>
      </div>
    </template>
    <template #bulk="{ keys, clear }">
      <Button size="sm" variant="outline" class="text-destructive hover:text-destructive" @click="remove([...keys], clear)">
        <Trash2 aria-hidden="true" />
        {{ t('backups.files.delete', { count: keys.length }) }}
      </Button>
    </template>
  </DataTable>
  <p class="m-0 text-xs text-muted-foreground">{{ t('backups.files.hint') }}</p>

  <RestoreSheet v-if="canManage" v-model:open="showRestore" :plan="plan" :file="restoring" @finished="onRestored" />
</template>
