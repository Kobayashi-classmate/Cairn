<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { History, Pencil, Play, Plus, Trash2 } from '@lucide/vue'
import type { CronTask } from '@/api/cron'
import DataTable from '@/components/common/DataTable.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import RowActions from '@/components/common/RowActions.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { describeSchedule } from '@/cron/schedule'
import { useCronStore } from '@/stores/cron'
import { askDeleteTask, canChange, runTaskNow, setEnabled, taskTarget } from './actions'
import { resultText, resultTone, taskFailure, taskState } from './labels'

// The scheduled tasks: what each runs, its schedule in words with the next
// run, how the last run went, and a switch to turn it on or off; Run now,
// Edit and a "more" menu.
const props = defineProps<{
  tasks: readonly CronTask[]
  loading: boolean
  error: unknown
  canManage: boolean
  canCreate: boolean
}>()

const emit = defineEmits<{ open: [id: string, section?: string]; create: []; edit: [task: CronTask]; retry: [] }>()

const { t } = useI18n()
const store = useCronStore()
const search = ref('')

const columns = computed<ColumnDef<CronTask>[]>(() => [
  { id: 'name', header: t('common.field.name'), accessorFn: (task) => task.name.toLowerCase(), meta: { class: 'min-w-[220px]' } },
  { id: 'type', header: t('common.field.type'), accessorFn: (task) => task.type, meta: { class: 'w-[100px]' } },
  { id: 'schedule', header: t('common.field.schedule'), accessorFn: (task) => task.nextRun ?? Number.MAX_SAFE_INTEGER, meta: { class: 'w-[210px]' } },
  { id: 'last', header: t('cron.column.lastRun'), accessorFn: (task) => task.lastRun ?? 0, meta: { class: 'w-[150px]' } },
  { id: 'enabled', header: t('cron.column.enabled'), enableSorting: false, meta: { class: 'w-[110px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[190px] text-end' } },
])

function searchText(task: CronTask): string {
  return [task.name, taskTarget(task, store.planNames), task.schedule, describeSchedule(task.schedule)].join(' ')
}

function actions(task: CronTask): RowAction[] {
  const list: RowAction[] = [{ key: 'runs', label: t('cron.action.history'), icon: History, onSelect: () => emit('open', task.id, 'runs') }]
  if (canChange(task)) {
    list.push({ key: 'delete', label: t('cron.task.delete'), icon: Trash2, destructive: true, onSelect: () => void askDeleteTask(task) })
  }
  return list
}
</script>

<template>
  <DataTable
    v-model:search="search"
    :data="tasks"
    :columns="columns"
    :row-key="(task) => task.id"
    :label="t('cron.title')"
    :loading="loading"
    :error="error"
    :search-placeholder="t('cron.search')"
    :search-text="searchText"
    :empty-title="t('cron.empty.title')"
    :empty-text="t('cron.empty.text')"
    :min-width="960"
    :initial-sort="[{ id: 'name', desc: false }]"
    @retry="emit('retry')"
  >
    <template #cell-name="{ row }">
      <div class="flex min-w-0 flex-col">
        <span class="flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            class="truncate rounded-sm text-start font-semibold text-foreground underline decoration-border underline-offset-[3px] outline-none hover:decoration-current focus-visible:ring-2 focus-visible:ring-ring/50"
            @click="emit('open', row.id)"
          >
            {{ row.name }}
          </button>
          <StatusPill v-if="row.state !== 'active'" :tone="taskState(row).tone" :title="taskFailure(row) || undefined">{{ taskState(row).text }}</StatusPill>
        </span>
        <span class="truncate text-xs text-muted-foreground" :dir="row.type === 'url' ? 'ltr' : undefined" :title="taskTarget(row, store.planNames)">
          {{ taskTarget(row, store.planNames) }}
        </span>
      </div>
    </template>
    <template #cell-type="{ row }"><StatusPill>{{ t(`cron.type.${row.type}`) }}</StatusPill></template>
    <template #cell-schedule="{ row }">
      <span class="flex flex-col">
        <span>{{ describeSchedule(row.schedule) }}</span>
        <span v-if="row.enabled && row.nextRun" class="text-xs text-muted-foreground">
          {{ t('cron.next') }} <RelativeTime :value="row.nextRun" />
        </span>
      </span>
    </template>
    <template #cell-last="{ row }">
      <StatusDot v-if="row.running" tone="progress">{{ resultText('running') }}</StatusDot>
      <span v-else-if="row.lastRun" class="flex flex-col gap-0.5">
        <StatusDot v-if="row.result" :tone="resultTone(row.result)">{{ resultText(row.result) }}</StatusDot>
        <RelativeTime :value="row.lastRun" class="text-xs text-muted-foreground" />
      </span>
      <span v-else class="text-muted-foreground">{{ t('cron.never') }}</span>
    </template>
    <template #cell-enabled="{ row }">
      <Switch
        :model-value="row.enabled"
        :disabled="!canChange(row) || row.state === 'applying'"
        :aria-label="t('cron.enableTask', { name: row.name })"
        @update:model-value="(value: boolean) => setEnabled(row, value)"
      />
    </template>
    <template #cell-actions="{ row }">
      <div class="flex justify-end gap-2">
        <Button v-if="canManage" variant="outline" size="sm" class="max-md:h-11" :disabled="row.running || row.state === 'applying'" @click="runTaskNow(row)">
          <Play aria-hidden="true" />
          {{ t('cron.action.run') }}
        </Button>
        <Button
          v-if="canChange(row)"
          variant="outline"
          size="icon"
          class="max-md:size-11"
          :aria-label="t('cron.action.edit', { name: row.name })"
          :title="t('cron.action.edit', { name: row.name })"
          @click="emit('edit', row)"
        >
          <Pencil aria-hidden="true" />
        </Button>
        <RowActions :actions="actions(row)" :label="t('cron.action.more', { name: row.name })" />
      </div>
    </template>
    <template v-if="props.canCreate" #empty>
      <Button @click="emit('create')">
        <Plus aria-hidden="true" />
        {{ t('cron.task.createTitle') }}
      </Button>
    </template>
  </DataTable>
</template>
