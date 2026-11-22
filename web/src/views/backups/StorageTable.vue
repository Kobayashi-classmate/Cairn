<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { LoaderCircle, Pencil, PlugZap, Plus, Trash2 } from '@lucide/vue'
import type { StorageView } from '@/api/backups'
import DataTable from '@/components/common/DataTable.vue'
import RowActions from '@/components/common/RowActions.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { useBackupsStore } from '@/stores/backups'
import { askDeleteStorage, testSavedStorage } from './actions'

// S3-compatible buckets the plans copy to: where they are, which plans use
// them, with Test and a "more" menu.
const props = defineProps<{
  storages: readonly StorageView[]
  loading: boolean
  error: unknown
  canManage: boolean
}>()

const emit = defineEmits<{ create: []; edit: [storage: StorageView]; retry: [] }>()

const { t } = useI18n()
const store = useBackupsStore()
const search = ref('')
const testing = ref<string | null>(null)

const columns = computed<ColumnDef<StorageView>[]>(() => [
  { id: 'name', header: t('common.field.name'), accessorFn: (s) => s.name.toLowerCase(), meta: { class: 'min-w-[160px]' } },
  { id: 'bucket', header: t('common.field.bucket'), enableSorting: false, meta: { class: 'min-w-[260px]' } },
  { id: 'region', header: t('common.field.region'), accessorFn: (s) => s.region ?? '', meta: { class: 'w-[130px]' } },
  { id: 'usedBy', header: t('backups.storage.usedBy'), enableSorting: false, meta: { class: 'w-[150px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[140px] text-end' } },
])

/** The endpoint with its scheme, as clients reach it. */
function endpointUrl(storage: StorageView): string {
  return `${storage.insecure ? 'http' : 'https'}://${storage.endpoint}`
}

function usedBy(storage: StorageView): number {
  return (store.plans ?? []).filter((plan) => plan.storageId === storage.id).length
}

async function test(storage: StorageView) {
  testing.value = storage.id
  await testSavedStorage(storage)
  testing.value = null
}

function actions(storage: StorageView): RowAction[] {
  if (!props.canManage) return []
  return [
    { key: 'edit', label: t('common.action.edit'), icon: Pencil, onSelect: () => emit('edit', storage) },
    { key: 'delete', label: t('backups.storage.delete'), icon: Trash2, destructive: true, onSelect: () => void askDeleteStorage(storage) },
  ]
}
</script>

<template>
  <p class="m-0 text-detail text-muted-foreground">{{ t('backups.storage.intro') }}</p>
  <DataTable
    v-model:search="search"
    :data="storages"
    :columns="columns"
    :row-key="(s) => s.id"
    :label="t('backups.tabs.storage')"
    :loading="loading"
    :error="error"
    :search-placeholder="t('backups.storage.search')"
    :search-text="(s) => `${s.name} ${s.bucket} ${s.endpoint}`"
    :empty-title="t('backups.storage.empty')"
    :empty-text="t('backups.storage.emptyText')"
    :min-width="860"
    :initial-sort="[{ id: 'name', desc: false }]"
    @retry="emit('retry')"
  >
    <template #cell-name="{ row }"><span class="font-medium">{{ row.name }}</span></template>
    <template #cell-bucket="{ row }">
      <span class="flex min-w-0 flex-col">
        <span class="font-mono text-[12.5px] [overflow-wrap:anywhere]" dir="ltr">{{ row.prefix ? `${row.bucket}/${row.prefix}` : row.bucket }}</span>
        <span class="flex flex-wrap items-center gap-1.5">
          <span class="font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]" dir="ltr">{{ endpointUrl(row) }}</span>
          <StatusPill v-if="row.pathStyle">{{ t('backups.storage.pathStyleTag') }}</StatusPill>
          <StatusPill v-if="row.insecure" tone="warning">{{ t('backups.storage.insecureTag') }}</StatusPill>
        </span>
      </span>
    </template>
    <template #cell-region="{ row }">
      <span v-if="row.region" class="font-mono text-[12.5px]" dir="ltr">{{ row.region }}</span>
      <span v-else class="text-muted-foreground">—</span>
    </template>
    <template #cell-usedBy="{ row }">
      <span :class="usedBy(row) === 0 ? 'text-muted-foreground' : ''">
        {{ usedBy(row) === 0 ? t('backups.storage.unused') : t('backups.storage.planCount', { count: usedBy(row) }, usedBy(row)) }}
      </span>
    </template>
    <template #cell-actions="{ row }">
      <div v-if="canManage" class="flex justify-end gap-2">
        <Button variant="outline" size="sm" class="max-md:h-11" :disabled="testing === row.id" @click="test(row)">
          <LoaderCircle v-if="testing === row.id" class="animate-spin" aria-hidden="true" />
          <PlugZap v-else aria-hidden="true" />
          {{ t('backups.storage.test') }}
        </Button>
        <RowActions :actions="actions(row)" :label="t('backups.storage.more', { name: row.name })" />
      </div>
    </template>
    <template v-if="canManage" #empty>
      <Button @click="emit('create')">
        <Plus aria-hidden="true" />
        {{ t('backups.storage.createTitle') }}
      </Button>
    </template>
  </DataTable>
</template>
