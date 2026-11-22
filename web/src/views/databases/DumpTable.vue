<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Camera, Download, HardDriveDownload, RotateCcw, Trash2, Upload } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import { fetchDumps, uploadDump, type DbEngine, type DumpInfo } from '@/api/databases'
import { ensureElevated } from '@/auth/stepUp'
import DataTable from '@/components/common/DataTable.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import RowActions from '@/components/common/RowActions.vue'
import SelectField from '@/components/common/SelectField.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { isUploadableDump } from '@/databases/validate'
import { formatBytes, formatPercent } from '@/i18n/format'
import { notifyError, notifyErrorText, notifySuccess } from '@/lib/notify'
import { useDatabasesStore } from '@/stores/databases'
import { askDeleteDump, downloadDump, dumpDatabase } from './actions'

// Dumps (MySQL/MariaDB, PostgreSQL) or snapshots (Redis) kept on this
// server: make one, upload one, download, restore or delete them.
const props = defineProps<{ engine: DbEngine; canManage: boolean }>()
const emit = defineEmits<{ restore: [file: string] }>()

const { t } = useI18n()
const store = useDatabasesStore()

const dumps = ref<DumpInfo[] | null>(null)
const error = ref<unknown>(null)
const loading = ref(false)
const search = ref('')
const dumpOf = ref('')
const uploading = ref<number | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)

const isRedis = computed(() => props.engine.kind === 'redis')

async function load() {
  loading.value = true
  try {
    dumps.value = await fetchDumps(props.engine.id)
    error.value = null
  } catch (err) {
    error.value = err
  } finally {
    loading.value = false
  }
}

// The servers reload when a job ends: new dumps show up then.
watch(() => [props.engine.id, store.version], () => void load(), { immediate: true })

defineExpose({ reload: load })

const databaseOptions = computed(() =>
  props.engine.databases.filter((db) => !db.missing).map((db) => ({ value: db.name, label: db.name })),
)

const columns = computed<ColumnDef<DumpInfo>[]>(() => [
  { id: 'file', header: t('databases.dumps.file'), accessorFn: (d) => d.file, meta: { class: 'min-w-[260px]' } },
  ...(isRedis.value
    ? []
    : [{ id: 'database', header: t('common.field.database'), accessorFn: (d: DumpInfo) => d.database ?? '', meta: { class: 'w-[160px]' } }]),
  { id: 'size', header: t('databases.size'), accessorFn: (d) => d.sizeBytes, meta: { class: 'w-[110px] tabular-nums' } },
  { id: 'created', header: t('databases.dumps.created'), accessorFn: (d) => d.createdAt, meta: { class: 'w-[150px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[160px] text-end' } },
])

function actions(dump: DumpInfo): RowAction[] {
  const list: RowAction[] = []
  if (!isRedis.value) list.push({ key: 'restore', label: t('databases.restore.action'), icon: RotateCcw, onSelect: () => emit('restore', dump.file) })
  list.push({
    key: 'delete',
    label: t('databases.dumps.delete'),
    icon: Trash2,
    destructive: true,
    onSelect: async () => {
      if (await askDeleteDump(props.engine, dump)) void load()
    },
  })
  return list
}

function dump() {
  if (isRedis.value) void dumpDatabase(props.engine)
  else if (dumpOf.value) void dumpDatabase(props.engine, dumpOf.value)
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
    notifyErrorText(t('databases.dumps.badName'))
    return
  }
  try {
    // The upload streams from disk with progress, so the client cannot retry it after a step-up.
    await ensureElevated()
  } catch (err) {
    if (!isStepUpRequired(err)) notifyError(err)
    return
  }
  uploading.value = 0
  try {
    await uploadDump(props.engine.id, file, (fraction) => (uploading.value = fraction))
    notifySuccess(t('databases.dumps.uploaded', { file: file.name }))
    await load()
  } catch (err) {
    notifyError(err)
  } finally {
    uploading.value = null
  }
}
</script>

<template>
  <p class="m-0 text-detail text-muted-foreground">
    {{ isRedis ? t('databases.dumps.redisIntro') : t('databases.dumps.intro') }}
  </p>
  <div v-if="uploading !== null" class="flex items-center gap-3" role="status">
    <span class="text-detail text-muted-foreground">{{ t('databases.dumps.uploading', { percent: formatPercent(uploading * 100, 0) }) }}</span>
    <div class="h-1.5 flex-1 overflow-hidden rounded-full bg-track" aria-hidden="true">
      <div class="h-full rounded-full bg-primary transition-[width] duration-200" :style="{ width: `${Math.round(uploading * 100)}%` }" />
    </div>
  </div>
  <DataTable
    v-model:search="search"
    :data="dumps ?? []"
    :columns="columns"
    :row-key="(d) => d.file"
    :label="isRedis ? t('databases.tabs.snapshots') : t('databases.tabs.dumps')"
    :loading="loading && !dumps"
    :error="error"
    :search-placeholder="t('databases.dumps.search')"
    :search-text="(d) => `${d.file} ${d.database ?? ''}`"
    :empty-title="isRedis ? t('databases.dumps.emptySnapshots') : t('databases.dumps.empty')"
    :min-width="isRedis ? 620 : 760"
    :initial-sort="[{ id: 'created', desc: true }]"
    @retry="load"
  >
    <template v-if="canManage" #toolbar>
      <Button v-if="isRedis" class="max-md:h-11" :disabled="!engine.running" @click="dump">
        <Camera aria-hidden="true" />
        {{ t('databases.dumps.snapshot') }}
      </Button>
      <template v-else>
        <SelectField
          v-model="dumpOf"
          :options="databaseOptions"
          :placeholder="t('databases.dumps.pickDatabase')"
          :aria-label="t('databases.dumps.pickDatabase')"
          class="w-48 max-md:h-11"
        />
        <Button variant="outline" class="max-md:h-11" :disabled="!dumpOf || !engine.running" @click="dump">
          <HardDriveDownload aria-hidden="true" />
          {{ t('databases.dumps.create') }}
        </Button>
        <Button variant="outline" class="max-md:h-11" :disabled="uploading !== null" @click="pickUpload">
          <Upload aria-hidden="true" />
          {{ t('databases.dumps.upload') }}
        </Button>
        <input ref="fileInput" type="file" accept=".sql,.gz,.dump" class="hidden" @change="onUpload" />
      </template>
    </template>

    <template #cell-file="{ row }">
      <span class="font-mono text-[12.5px] [overflow-wrap:anywhere]" dir="ltr">{{ row.file }}</span>
    </template>
    <template #cell-database="{ row }">
      <span v-if="row.database" class="font-mono text-[12.5px]" dir="ltr">{{ row.database }}</span>
      <span v-else class="text-muted-foreground">—</span>
    </template>
    <template #cell-size="{ row }">{{ formatBytes(row.sizeBytes) }}</template>
    <template #cell-created="{ row }"><RelativeTime :value="row.createdAt" /></template>
    <template #cell-actions="{ row }">
      <div v-if="canManage" class="flex justify-end gap-2">
        <Button variant="outline" size="sm" class="max-md:h-11" @click="downloadDump(engine, row)">
          <Download aria-hidden="true" />
          {{ t('common.action.download') }}
        </Button>
        <RowActions :actions="actions(row)" :label="t('databases.dumps.more', { file: row.file })" />
      </div>
    </template>
  </DataTable>
</template>
