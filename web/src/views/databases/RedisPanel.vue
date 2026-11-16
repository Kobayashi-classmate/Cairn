<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NTabPane, NTabs } from 'naive-ui'
import type { DbEngine } from '@/api/databases'
import type { Job } from '@/api/jobs'
import JobProgress from '@/components/JobProgress.vue'
import { useJobRun } from '@/composables/useJobRun'
import DumpsPanel from './DumpsPanel.vue'
import RedisKeys from './RedisKeys.vue'
import RedisSettings from './RedisSettings.vue'

defineProps<{ engine: DbEngine; canManage: boolean; version: number }>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const { jobId, follow, finished, dismiss } = useJobRun((_job: Job) => emit('changed'))
const tab = ref<'settings' | 'keys' | 'snapshots'>('settings')
</script>

<template>
  <div>
    <div v-if="jobId" class="job">
      <JobProgress :job-id="jobId" @finished="finished" />
      <NButton size="small" quaternary @click="dismiss">{{ t('sites.create.close') }}</NButton>
    </div>
    <NTabs v-model:value="tab" type="segment" size="small">
      <NTabPane name="settings" :tab="t('databases.tabs.settings')">
        <div class="pane"><RedisSettings :engine="engine" :can-manage="canManage" @started="follow" /></div>
      </NTabPane>
      <NTabPane name="keys" :tab="t('databases.tabs.keys')">
        <div class="pane"><RedisKeys :can-manage="canManage" /></div>
      </NTabPane>
      <NTabPane name="snapshots" :tab="t('databases.tabs.snapshots')">
        <div class="pane">
          <DumpsPanel :engine="engine" :can-manage="canManage" :version="version" @changed="emit('changed')" />
        </div>
      </NTabPane>
    </NTabs>
  </div>
</template>

<style scoped>
.pane {
  padding-block-start: 12px;
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
</style>
