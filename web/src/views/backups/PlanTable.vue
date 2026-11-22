<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Archive, CalendarClock, History, Pencil, Play, Plus, Trash2 } from '@lucide/vue'
import type { BackupPlan } from '@/api/backups'
import DataTable from '@/components/common/DataTable.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import RowActions from '@/components/common/RowActions.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { targetLabel } from '@/backups/targets'
import { errorText } from '@/i18n/errors'
import { useBackupsStore } from '@/stores/backups'
import { askDeletePlan, canHandlePlan, nextRun, planRunning, runPlanNow, scheduleText, targetsSummary } from './actions'

// The backup plans: what each backs up, where to, when it runs and how the
// last run went, with Run now, Edit and a "more" menu.
const props = defineProps<{
  plans: readonly BackupPlan[]
  loading: boolean
  error: unknown
  canManage: boolean
  canSchedule: boolean
}>()

const emit = defineEmits<{
  open: [id: string, section?: string]
  create: []
  edit: [plan: BackupPlan]
  schedule: [plan: BackupPlan]
  retry: []
}>()

const { t } = useI18n()
const store = useBackupsStore()
const search = ref('')

const columns = computed<ColumnDef<BackupPlan>[]>(() => [
  { id: 'name', header: t('common.field.name'), accessorFn: (plan) => plan.name.toLowerCase(), meta: { class: 'min-w-[160px]' } },
  { id: 'targets', header: t('backups.plan.targets'), enableSorting: false, meta: { class: 'min-w-[220px]' } },
  { id: 'storage', header: t('backups.plan.storage'), accessorFn: (plan) => store.storageName(plan.storageId) ?? '', meta: { class: 'w-[150px]' } },
  { id: 'schedule', header: t('backups.plan.scheduled'), enableSorting: false, meta: { class: 'w-[170px]' } },
  { id: 'next', header: t('backups.plan.nextRun'), accessorFn: (plan) => nextRun(store.tasksOf(plan)) ?? Number.MAX_SAFE_INTEGER, meta: { class: 'w-[130px]' } },
  { id: 'last', header: t('backups.plan.lastRun'), accessorFn: (plan) => plan.lastRun?.started ?? 0, meta: { class: 'w-[160px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[190px] text-end' } },
])

function searchText(plan: BackupPlan): string {
  return [plan.name, ...plan.targets.map(targetLabel), store.storageName(plan.storageId) ?? ''].join(' ')
}

function runError(plan: BackupPlan): string {
  const err = plan.lastRun?.error
  return err ? (errorText(err.code, err.params ?? {}) ?? err.code) : ''
}

function run(plan: BackupPlan) {
  void runPlanNow(plan)
}

function actions(plan: BackupPlan): RowAction[] {
  const list: RowAction[] = [
    { key: 'files', label: t('backups.action.files'), icon: Archive, onSelect: () => emit('open', plan.id, 'files') },
    { key: 'runs', label: t('backups.action.runs'), icon: History, onSelect: () => emit('open', plan.id, 'runs') },
  ]
  if (props.canSchedule) list.push({ key: 'schedule', label: t('backups.action.schedule'), icon: CalendarClock, onSelect: () => emit('schedule', plan) })
  // A plan reaching beyond the site directories is an administrator's to delete too.
  if (props.canManage && canHandlePlan(plan)) {
    list.push({ key: 'delete', label: t('backups.plan.delete'), icon: Trash2, destructive: true, onSelect: () => void askDeletePlan(plan) })
  }
  return list
}
</script>

<template>
  <DataTable
    v-model:search="search"
    :data="plans"
    :columns="columns"
    :row-key="(plan) => plan.id"
    :label="t('backups.tabs.plans')"
    :loading="loading"
    :error="error"
    :search-placeholder="t('backups.plan.search')"
    :search-text="searchText"
    :empty-title="t('backups.plan.empty')"
    :empty-text="t('backups.plan.emptyText')"
    :min-width="1080"
    :initial-sort="[{ id: 'name', desc: false }]"
    @retry="emit('retry')"
  >
    <template #cell-name="{ row }">
      <button
        type="button"
        class="self-start truncate rounded-sm text-start font-semibold text-foreground underline decoration-border underline-offset-[3px] tap-y outline-none hover:decoration-current focus-visible:ring-2 focus-visible:ring-ring/50"
        @click="emit('open', row.id)"
      >
        {{ row.name }}
      </button>
    </template>
    <template #cell-targets="{ row }">
      <span class="text-detail [overflow-wrap:anywhere]" dir="auto">{{ targetsSummary(row) }}</span>
    </template>
    <template #cell-storage="{ row }">
      <span class="flex flex-col">
        <span>{{ store.storageName(row.storageId) ?? t('backups.plan.localOnly') }}</span>
        <span v-if="row.storageId && row.keepLocal" class="text-xs text-muted-foreground">{{ t('backups.plan.alsoLocal') }}</span>
      </span>
    </template>
    <template #cell-schedule="{ row }">
      <span :class="store.tasksOf(row).some((task) => task.enabled) ? '' : 'text-muted-foreground'">{{ scheduleText(store.tasksOf(row)) }}</span>
    </template>
    <template #cell-next="{ row }">
      <RelativeTime v-if="nextRun(store.tasksOf(row))" :value="nextRun(store.tasksOf(row))!" />
      <span v-else class="text-muted-foreground">—</span>
    </template>
    <template #cell-last="{ row }">
      <span v-if="row.lastRun" class="flex flex-col gap-0.5">
        <StatusDot :tone="row.lastRun.ok ? 'success' : 'destructive'" :title="runError(row) || undefined">
          {{ row.lastRun.ok ? t('backups.runs.ok') : t('backups.runs.failed') }}
        </StatusDot>
        <RelativeTime :value="row.lastRun.finished ?? row.lastRun.started" class="text-xs text-muted-foreground" />
      </span>
      <StatusDot v-else tone="warning">{{ t('backups.plan.never') }}</StatusDot>
    </template>
    <template #cell-actions="{ row }">
      <div class="flex justify-end gap-2">
        <Button v-if="canManage" variant="outline" size="sm" class="max-md:h-11" :disabled="planRunning(row.id)" @click="run(row)">
          <Play aria-hidden="true" />
          {{ t('backups.action.run') }}
        </Button>
        <Button
          v-if="canHandlePlan(row)"
          variant="outline"
          size="icon"
          class="max-md:size-11"
          :aria-label="t('backups.action.edit', { name: row.name })"
          :title="t('backups.action.edit', { name: row.name })"
          @click="emit('edit', row)"
        >
          <Pencil aria-hidden="true" />
        </Button>
        <RowActions :actions="actions(row)" :label="t('backups.action.more', { name: row.name })" />
      </div>
    </template>
    <template v-if="canManage" #empty>
      <Button @click="emit('create')">
        <Plus aria-hidden="true" />
        {{ t('backups.plan.createTitle') }}
      </Button>
    </template>
  </DataTable>
</template>
