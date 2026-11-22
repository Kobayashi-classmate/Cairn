<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RefreshCw } from '@lucide/vue'
import { isAbortError } from '@/api/client'
import { fetchPlanRuns, type BackupPlan, type BackupRun } from '@/api/backups'
import DataTable from '@/components/common/DataTable.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import { errorText } from '@/i18n/errors'
import { formatBytes, formatDuration, formatNumber } from '@/i18n/format'

// A plan's runs, newest first: when, how long, the outcome, how many
// backups were made and how many old ones retention removed.
const props = defineProps<{ plan: BackupPlan }>()

const { t } = useI18n()
const runs = ref<BackupRun[]>([])
const loaded = ref(false)
const loading = ref(false)
const error = ref<unknown>(null)
let controller: AbortController | null = null

async function load() {
  controller?.abort()
  const request = new AbortController()
  controller = request
  loading.value = true
  try {
    runs.value = await fetchPlanRuns(props.plan.id, request.signal)
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
// A run that just ended changes the plan's last run.
watch(() => props.plan.lastRun?.started, (now, before) => {
  if (now !== before) void load()
})

const columns = computed<ColumnDef<BackupRun>[]>(() => [
  { id: 'started', header: t('backups.runs.started'), accessorFn: (run) => run.started, meta: { class: 'w-[150px]' } },
  { id: 'duration', header: t('backups.runs.duration'), enableSorting: false, meta: { class: 'w-[100px]' } },
  { id: 'result', header: t('backups.runs.result'), enableSorting: false, meta: { class: 'min-w-[200px]' } },
  { id: 'files', header: t('backups.runs.files'), enableSorting: false, meta: { class: 'w-[90px] tabular-nums' } },
  { id: 'size', header: t('files.column.size'), accessorFn: (run) => run.size, meta: { class: 'w-[100px] tabular-nums' } },
  { id: 'deleted', header: t('backups.runs.deleted'), enableSorting: false, meta: { class: 'w-[90px] tabular-nums' } },
])

function reason(run: BackupRun): string {
  return run.error ? (errorText(run.error.code, run.error.params ?? {}) ?? run.error.code) : ''
}
</script>

<template>
  <DataTable
    :data="runs"
    :columns="columns"
    :row-key="(run) => `${run.started}`"
    :label="t('backups.action.runs')"
    :loading="loading && !loaded"
    :error="error"
    :empty-title="t('backups.runs.empty')"
    :min-width="720"
    :initial-sort="[{ id: 'started', desc: true }]"
    @retry="load"
  >
    <template #toolbar>
      <Button variant="ghost" size="sm" :disabled="loading" @click="load">
        <RefreshCw :class="loading ? 'animate-spin' : ''" aria-hidden="true" />
        {{ t('common.action.refresh') }}
      </Button>
    </template>
    <template #cell-started="{ row }"><RelativeTime :value="row.started" /></template>
    <template #cell-duration="{ row }">
      <span v-if="row.finished" class="tabular-nums">{{ formatDuration(Math.max(0, row.finished - row.started)) }}</span>
      <span v-else class="text-muted-foreground">—</span>
    </template>
    <template #cell-result="{ row }">
      <span class="flex flex-col gap-0.5">
        <StatusDot :tone="row.ok ? 'success' : row.finished ? 'destructive' : 'progress'">
          {{ row.ok ? t('backups.runs.ok') : row.finished ? t('backups.runs.failed') : t('backups.runs.running') }}
        </StatusDot>
        <span v-if="reason(row)" class="text-xs text-destructive [overflow-wrap:anywhere]">{{ reason(row) }}</span>
      </span>
    </template>
    <template #cell-files="{ row }">{{ formatNumber(row.files?.length ?? 0) }}</template>
    <template #cell-size="{ row }">{{ formatBytes(row.size) }}</template>
    <template #cell-deleted="{ row }">{{ formatNumber(row.deleted ?? 0) }}</template>
  </DataTable>
</template>
