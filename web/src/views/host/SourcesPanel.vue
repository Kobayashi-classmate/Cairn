<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The distribution's package sources: compare the mirrors and switch the
// main or security source; earlier files are backed up and can come back.
// Comparing takes a while, so it only runs when asked.
import { computed, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NDataTable, NEmpty, NSpin, NText, type DataTableColumns } from 'naive-ui'
import { fetchSourceBackups, fetchSystemSources, restoreSources, switchSystemSource, type SourceBackup } from '@/api/host'
import type { Job } from '@/api/jobs'
import type { SourceCandidate, SourceSlot } from '@/api/software'
import JobProgress from '@/components/JobProgress.vue'
import SourceSlots from '@/components/SourceSlots.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useJobRun } from '@/composables/useJobRun'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'

const props = defineProps<{ canManage: boolean }>()

const { t } = useI18n()
const confirm = useConfirm()
const slots = ref<SourceSlot[] | null>(null)
const loading = ref(false)
const loadError = ref<unknown>(null)
const backups = ref<SourceBackup[]>([])
const backupsError = ref<unknown>(null)
/** The slots component wants a selection; system sources have none. */
const noSelection = ref<Record<string, string>>({})
const noRepos = new Map()

async function load() {
  loading.value = true
  try {
    slots.value = await fetchSystemSources()
    loadError.value = null
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

async function loadBackups() {
  try {
    backups.value = await fetchSourceBackups()
    backupsError.value = null
  } catch (err) {
    backupsError.value = err
  }
}

// Shown only when the tab is opened; the comparison takes some seconds.
onMounted(() => {
  void load()
  void loadBackups()
})

const { jobId, busy, run, finished, dismiss } = useJobRun((_job: Job) => {
  void load()
  void loadBackups()
})

async function switchTo(slot: SourceSlot, candidate: SourceCandidate) {
  const role = slot.role ?? 'main'
  const ok = await confirm({
    title: t('host.sources.switchTitle', { role: t(`host.sources.role.${role}`) }),
    content: t('host.sources.switchText', { name: candidate.name }),
    confirmText: t('host.sources.switch'),
  })
  // Step-up protected: the client asks for the password and retries.
  if (ok) void run(() => switchSystemSource(role, candidate.id))
}

async function restore(backup: SourceBackup) {
  const ok = await confirm({
    title: t('host.sources.restoreTitle'),
    content: t('host.sources.restoreText', { from: backup.from, to: backup.to }),
    confirmText: t('host.sources.restore'),
  })
  if (ok) void run(() => restoreSources(backup.id))
}

const columns = computed<DataTableColumns<SourceBackup>>(() => [
  { key: 'at', title: t('host.sources.backupAt'), width: 170, render: (backup) => formatDateTime(backup.at) },
  { key: 'role', title: t('common.field.role'), width: 120, render: (backup) => t(`host.sources.role.${backup.role}`) },
  {
    key: 'change',
    title: t('host.sources.change'),
    minWidth: 260,
    render: (backup) =>
      h('div', { class: 'cell' }, [
        h('span', { class: 'mono' }, `${backup.from} → ${backup.to}`),
        h(NText, { depth: 3, class: 'cell__sub mono' }, () => backup.files.join(', ')),
      ]),
  },
  {
    key: 'actions',
    title: '',
    width: 120,
    render: (backup) =>
      props.canManage
        ? h(NButton, { size: 'small', disabled: busy.value, onClick: () => void restore(backup) }, () => t('host.sources.restore'))
        : null,
  },
])
</script>

<template>
  <div class="sources">
    <div class="head">
      <NText depth="3" class="intro">{{ t('host.sources.intro') }}</NText>
      <NButton size="small" :loading="loading" @click="load">{{ t('common.action.refresh') }}</NButton>
    </div>
    <div v-if="jobId" class="job">
      <JobProgress :job-id="jobId" @finished="finished" />
      <NButton size="small" quaternary :disabled="busy" @click="dismiss">{{ t('sites.create.close') }}</NButton>
    </div>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NSpin :show="loading" :description="loading ? t('host.sources.probing') : undefined">
      <SourceSlots
        v-if="slots && slots.length > 0"
        v-model:selection="noSelection"
        :slots="slots"
        :repos="noRepos"
        :switchable="canManage"
        :switch-disabled="busy"
        @switch="switchTo"
      />
      <NEmpty v-else-if="slots" :description="t('errors.syssrc.unsupported')" class="empty" />
      <div v-else class="placeholder" />
    </NSpin>

    <NCard size="small" :title="t('host.sources.backups')" class="backups">
      <NAlert v-if="backupsError" type="error" class="block">{{ errorMessage(backupsError) }}</NAlert>
      <NEmpty v-else-if="backups.length === 0" :description="t('host.sources.noBackups')" />
      <NDataTable v-else :columns="columns" :data="backups" :row-key="(backup: SourceBackup) => backup.id" :scroll-x="660" size="small" />
    </NCard>
  </div>
</template>

<style scoped>
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-block-end: 12px;
}

.intro {
  font-size: 13px;
}

.block {
  margin-block-end: 12px;
}

.placeholder {
  min-block-size: 160px;
}

.empty {
  padding-block: 32px;
}

.backups {
  margin-block-start: 16px;
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

:deep(.cell) {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

:deep(.cell__sub) {
  font-size: 11px;
  overflow-wrap: anywhere;
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
}
</style>
