<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Package updates of the system: check, install (all or security only)
// and automatic security updates.
import { computed, h } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NDataTable, NEmpty, NSpace, NSwitch, NTag, NText, type DataTableColumns } from 'naive-ui'
import { checkUpdates, installUpdates, setAutoUpdates, type HostUpdate, type HostView } from '@/api/host'
import type { Job } from '@/api/jobs'
import JobProgress from '@/components/JobProgress.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useJobRun } from '@/composables/useJobRun'
import { useNow } from '@/composables/useNow'
import { formatRelativeTime } from '@/i18n/format'

const props = defineProps<{ view: HostView; canManage: boolean }>()
const emit = defineEmits<{ changed: []; openSources: [] }>()

const { t } = useI18n()
const confirm = useConfirm()
const now = useNow(60_000)

const updates = computed(() => props.view.updates)
const securityCount = computed(() => updates.value.packages.filter((pkg) => pkg.security).length)
/** Sources that hold updates back, from the last check. */
const sourceWarnings = computed(() => updates.value.sources.filter((source) => source.warning))

const { jobId, busy, run, finished, dismiss } = useJobRun((_job: Job) => emit('changed'))

function check() {
  void run(checkUpdates)
}

async function install(securityOnly: boolean) {
  const count = securityOnly ? securityCount.value : updates.value.packages.length
  const ok = await confirm({
    title: securityOnly ? t('host.updates.installSecurityTitle', { count }, count) : t('host.updates.installAllTitle', { count }, count),
    content: t('host.updates.installText'),
    confirmText: t('host.updates.install'),
  })
  // Step-up protected: the client asks for the password and retries.
  if (ok) void run(() => installUpdates(securityOnly))
}

function toggleAuto(enabled: boolean) {
  void run(() => setAutoUpdates(enabled))
}

const columns = computed<DataTableColumns<HostUpdate>>(() => [
  {
    key: 'name',
    title: t('common.field.name'),
    minWidth: 200,
    render: (pkg) =>
      h('span', { class: 'pkg' }, [
        h('span', { class: 'mono' }, pkg.name),
        pkg.security ? h(NTag, { size: 'tiny', type: 'error', bordered: false }, () => t('host.updates.security')) : null,
      ]),
  },
  { key: 'from', title: t('host.updates.from'), minWidth: 160, render: (pkg) => h('span', { class: 'mono' }, pkg.from) },
  { key: 'to', title: t('host.updates.to'), minWidth: 160, render: (pkg) => h('span', { class: 'mono' }, pkg.to) },
])
</script>

<template>
  <div class="updates">
    <NAlert v-for="source in sourceWarnings" :key="source.role" type="warning" class="block">
      {{ t(`host.updates.sourceWarning.${source.warning}`, { role: t(`host.sources.role.${source.role}`) }) }}
      <NButton size="small" text type="primary" class="block__link" @click="emit('openSources')">{{ t('host.updates.openSources') }}</NButton>
    </NAlert>

    <NCard size="small">
      <div class="head">
        <NText>
          {{ updates.checkedAt ? t('host.updates.checkedAt', { time: formatRelativeTime(updates.checkedAt, now) }) : t('host.updates.never') }}
        </NText>
        <span class="head__spacer" />
        <NSpace v-if="canManage" :size="8">
          <NButton :disabled="busy" @click="check">{{ t('host.updates.check') }}</NButton>
          <NButton :disabled="busy || securityCount === 0" @click="install(true)">
            {{ t('host.updates.installSecurity', { count: securityCount }) }}
          </NButton>
          <NButton type="primary" :disabled="busy || updates.packages.length === 0" @click="install(false)">
            {{ t('host.updates.installAll', { count: updates.packages.length }) }}
          </NButton>
        </NSpace>
      </div>
      <div class="auto">
        <label class="switch">
          <NSwitch
            :value="view.autoUpdates.enabled"
            :disabled="!canManage || !view.autoUpdates.available || busy"
            @update:value="toggleAuto"
          />
          <span>{{ t('host.updates.auto') }}</span>
        </label>
        <NText depth="3" class="hint">
          {{ view.autoUpdates.available ? t('host.updates.autoHint', { tool: view.autoUpdates.tool }) : t('errors.host.updates_unavailable') }}
        </NText>
      </div>
      <div v-if="jobId" class="job">
        <JobProgress :job-id="jobId" @finished="finished" />
        <NButton size="small" quaternary :disabled="busy" @click="dismiss">{{ t('sites.create.close') }}</NButton>
      </div>
    </NCard>

    <NEmpty v-if="updates.packages.length === 0" :description="t('host.updates.none')" class="empty" />
    <NDataTable v-else :columns="columns" :data="updates.packages" :row-key="(pkg: HostUpdate) => pkg.name" :scroll-x="560" size="small" class="table" />
  </div>
</template>

<style scoped>
.block {
  margin-block-end: 12px;
}

.block__link {
  margin-inline-start: 8px;
}

.head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
}

.head__spacer {
  flex: 1;
}

.auto {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 16px;
  margin-block-start: 12px;
}

.switch {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}

.hint {
  font-size: 12px;
}

.empty {
  padding-block: 32px;
}

.table {
  margin-block-start: 12px;
}

.job {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 4px;
  margin-block-start: 12px;
}

.job > :first-child {
  align-self: stretch;
}

:deep(.pkg) {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
}
</style>
