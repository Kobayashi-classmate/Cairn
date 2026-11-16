<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// A plan's runs, newest first.
import { computed, h, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NDataTable, NDrawer, NDrawerContent, NEmpty, NTag, type DataTableColumns } from 'naive-ui'
import { fetchPlanRuns, type BackupPlan, type BackupRun } from '@/api/backups'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatBytes, formatDateTime, formatDuration } from '@/i18n/format'

const props = defineProps<{ plan: BackupPlan | null }>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const runs = ref<BackupRun[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)

async function load() {
  const plan = props.plan
  if (!plan) return
  loading.value = true
  try {
    runs.value = await fetchPlanRuns(plan.id)
    loadError.value = null
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

watch(
  () => props.plan?.id,
  (id) => {
    runs.value = []
    if (id) void load()
  },
  { immediate: true },
)

const columns = computed<DataTableColumns<BackupRun>>(() => [
  { key: 'started', title: t('backups.runs.started'), width: 170, render: (run) => formatDateTime(run.started) },
  {
    key: 'duration',
    title: t('backups.runs.duration'),
    width: 110,
    render: (run) => (run.finished ? formatDuration(Math.max(0, run.finished - run.started)) : '-'),
  },
  {
    key: 'result',
    title: t('backups.runs.result'),
    minWidth: 220,
    render: (run) =>
      run.ok
        ? h(NTag, { size: 'small', type: 'success', bordered: false }, () => t('backups.runs.ok'))
        : h('div', { class: 'cell' }, [
            h(NTag, { size: 'small', type: 'error', bordered: false }, () => t('backups.runs.failed')),
            run.error ? h('span', { class: 'cell__reason' }, errorText(run.error.code, run.error.params ?? {}) ?? run.error.code) : null,
          ]),
  },
  { key: 'files', title: t('backups.runs.files'), width: 90, render: (run) => String(run.files?.length ?? 0) },
  { key: 'size', title: t('files.column.size'), width: 100, render: (run) => formatBytes(run.size) },
  { key: 'deleted', title: t('backups.runs.deleted'), width: 100, render: (run) => String(run.deleted ?? 0) },
])
</script>

<template>
  <NDrawer :show="plan !== null" placement="right" width="min(860px, 100vw)" @update:show="(open: boolean) => !open && emit('close')">
    <NDrawerContent :title="plan ? t('backups.runs.title', { name: plan.name }) : ''" closable :native-scrollbar="false">
      <div class="toolbar">
        <NButton size="small" :loading="loading" @click="load">{{ t('common.action.refresh') }}</NButton>
      </div>
      <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
      <NEmpty v-else-if="!loading && runs.length === 0" :description="t('backups.runs.empty')" class="empty" />
      <NDataTable v-else :columns="columns" :data="runs" :loading="loading" :scroll-x="700" size="small" />
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.toolbar {
  display: flex;
  justify-content: flex-end;
  margin-block-end: 12px;
}

.block {
  margin-block-end: 12px;
}

.empty {
  padding-block: 40px;
}

:deep(.cell) {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
}

:deep(.cell__reason) {
  font-size: 12px;
}
</style>
