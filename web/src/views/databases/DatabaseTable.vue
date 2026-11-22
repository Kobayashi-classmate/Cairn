<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { HardDriveDownload, Plus, RotateCcw, Trash2 } from '@lucide/vue'
import type { DbDatabase, DbEngine } from '@/api/databases'
import DataTable from '@/components/common/DataTable.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import RowActions from '@/components/common/RowActions.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { formatBytes } from '@/i18n/format'
import { TONE_TEXT, type Tone } from '@/lib/tone'
import { askDropDatabase, dumpDatabase } from './actions'

// The databases of a MySQL/MariaDB or PostgreSQL server: size, owner, the
// last backup (a plan's run or a dump, whichever is newer), with Dump now,
// Restore and Delete.
const props = defineProps<{
  engine: DbEngine
  canManage: boolean
  /** The backups module is on, so a missing backup is worth a warning. */
  backupsKnown: boolean
}>()

const emit = defineEmits<{ create: []; restore: [database: string] }>()

const { t } = useI18n()
const search = ref('')

const columns = computed<ColumnDef<DbDatabase>[]>(() => [
  { id: 'name', header: t('common.field.name'), accessorFn: (db) => db.name, meta: { class: 'min-w-[200px]' } },
  { id: 'size', header: t('databases.size'), accessorFn: (db) => db.sizeBytes, meta: { class: 'w-[110px] tabular-nums' } },
  { id: 'owner', header: t('databases.owner'), accessorFn: (db) => db.owner ?? '', meta: { class: 'w-[160px]' } },
  { id: 'backup', header: t('databases.lastBackup'), accessorFn: (db) => lastAt(db), meta: { class: 'w-[170px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[150px] text-end' } },
])

function lastAt(db: DbDatabase): number {
  return Math.max(db.backup?.lastOkAt ?? 0, db.lastDumpAt ?? 0)
}

interface BackupCell {
  tone: Tone
  text?: string
  at?: number
}

/** The newer of a plan's last successful run and the newest dump; a later failed run shows as failed. */
function backup(db: DbDatabase): BackupCell | null {
  const ok = lastAt(db)
  const failed = db.backup?.lastFailedAt
  if (failed && failed > ok) return { tone: 'destructive', text: t('databases.backup.failed'), at: failed }
  if (ok > 0) return { tone: 'neutral', at: ok }
  if (props.backupsKnown) return { tone: 'warning', text: t('databases.backup.never') }
  return null
}

function actions(db: DbDatabase): RowAction[] {
  if (!props.canManage || db.missing) return []
  return [
    { key: 'restore', label: t('databases.restore.action'), icon: RotateCcw, onSelect: () => emit('restore', db.name) },
    { key: 'delete', label: t('databases.db.drop'), icon: Trash2, destructive: true, onSelect: () => void askDropDatabase(props.engine, db) },
  ]
}
</script>

<template>
  <DataTable
    v-model:search="search"
    :data="engine.databases"
    :columns="columns"
    :row-key="(db) => db.name"
    :label="t('databases.tabs.databases')"
    :search-placeholder="t('databases.db.search')"
    :search-text="(db) => `${db.name} ${db.owner ?? ''}`"
    :empty-title="t('databases.db.empty')"
    :empty-text="canManage ? t('databases.db.emptyText') : undefined"
    :min-width="760"
    :initial-sort="[{ id: 'name', desc: false }]"
  >
    <template v-if="canManage" #toolbar>
      <Button class="max-md:h-11" :disabled="!engine.running" @click="emit('create')">
        <Plus aria-hidden="true" />
        {{ t('databases.db.createTitle') }}
      </Button>
    </template>

    <template #cell-name="{ row }">
      <span class="flex flex-wrap items-center gap-1.5">
        <span class="font-mono text-[13px] font-medium" dir="ltr">{{ row.name }}</span>
        <StatusPill v-if="row.missing" tone="destructive">{{ t('databases.missing') }}</StatusPill>
        <StatusPill v-if="!row.managed">{{ t('databases.unmanagedTag') }}</StatusPill>
      </span>
    </template>
    <template #cell-size="{ row }">{{ formatBytes(row.sizeBytes) }}</template>
    <template #cell-owner="{ row }">
      <span v-if="row.owner" class="font-mono text-[12.5px]" dir="ltr">{{ row.owner }}</span>
      <span v-else class="text-muted-foreground">—</span>
    </template>
    <template #cell-backup="{ row }">
      <span v-if="!backup(row)" class="text-muted-foreground">—</span>
      <span v-else :class="['flex flex-col', backup(row)!.tone === 'neutral' ? 'text-foreground' : TONE_TEXT[backup(row)!.tone]]">
        <span v-if="backup(row)!.text" class="whitespace-nowrap">{{ backup(row)!.text }}</span>
        <RelativeTime v-if="backup(row)!.at" :value="backup(row)!.at!" :class="backup(row)!.text ? 'text-xs' : ''" />
      </span>
    </template>
    <template #cell-actions="{ row }">
      <div v-if="canManage && !row.missing" class="flex justify-end gap-2">
        <Button variant="outline" size="sm" class="max-md:h-11" :disabled="!engine.running" @click="dumpDatabase(engine, row.name)">
          <HardDriveDownload aria-hidden="true" />
          {{ t('databases.dumps.create') }}
        </Button>
        <RowActions :actions="actions(row)" :label="t('databases.db.more', { name: row.name })" />
      </div>
    </template>

    <template v-if="canManage && engine.running" #empty>
      <Button @click="emit('create')">
        <Plus aria-hidden="true" />
        {{ t('databases.db.createTitle') }}
      </Button>
    </template>
  </DataTable>
</template>
