<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NSpace, NSwitch, NTag, NText, useMessage } from 'naive-ui'
import { controlService, createCluster, setRemoteAccess, type DbEngine } from '@/api/databases'
import type { Job } from '@/api/jobs'
import JobProgress from '@/components/JobProgress.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useJobRun } from '@/composables/useJobRun'
import { errorText } from '@/i18n/errors'
import { formatBytes, formatNumber } from '@/i18n/format'

const props = defineProps<{ engine: DbEngine; canManage: boolean }>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const confirm = useConfirm()
const message = useMessage()
const { jobId, busy, run, finished, dismiss } = useJobRun((_job: Job) => emit('changed'))

const engineError = computed(() => {
  const error = props.engine.error
  return error ? (errorText(error.code, error.params ?? {}) ?? error.code) : ''
})

const engineErrorDetail = computed(() => {
  const detail = props.engine.error?.params?.detail
  return typeof detail === 'string' ? detail : ''
})

function service(action: 'start' | 'stop' | 'restart') {
  void run(() => controlService(props.engine.id, action))
}

async function toggleRemote(enabled: boolean) {
  // Redis refuses remote access without a password.
  if (enabled && props.engine.kind === 'redis' && !props.engine.redis?.passwordSet) {
    message.warning(t('errors.redis.password_required'))
    return
  }
  const ok = await confirm({
    title: enabled ? t('databases.remote.enableTitle') : t('databases.remote.disableTitle'),
    content: enabled
      ? props.engine.kind === 'postgresql'
        ? t('databases.remote.enablePg')
        : t('databases.remote.enableRestart')
      : t('databases.remote.disable'),
    confirmText: t('common.action.confirm'),
  })
  // Step-up protected: the client asks for the password and retries.
  if (ok) void run(() => setRemoteAccess(props.engine.id, enabled))
}

function cluster() {
  void run(() => createCluster(props.engine.id))
}
</script>

<template>
  <NCard size="small" class="header">
    <div class="header__row">
      <NTag :type="engine.running ? 'success' : 'default'" :bordered="false">
        {{ engine.running ? t('databases.running') : t('databases.stopped') }}
      </NTag>
      <NText v-if="engine.version" depth="3">{{ t('databases.version', { version: engine.version }) }}</NText>
      <NText v-if="engine.port" depth="3">{{ t('databases.port', { port: engine.port }) }}</NText>
      <NText v-if="engine.redis" depth="3">
        {{ t('databases.redisStats', { keys: formatNumber(engine.redis.keys), memory: formatBytes(engine.redis.usedMemory) }) }}
      </NText>
      <span class="header__spacer" />
      <label class="remote">
        <NSwitch
          :value="engine.remote"
          size="small"
          :disabled="!canManage || busy || !!jobId"
          @update:value="toggleRemote"
        />
        <span>{{ t('databases.remote.label') }}</span>
      </label>
      <NSpace v-if="canManage" :size="4">
        <NButton v-if="!engine.running" size="small" :disabled="busy" @click="service('start')">
          {{ t('sites.action.start') }}
        </NButton>
        <NButton v-else size="small" :disabled="busy" @click="service('stop')">{{ t('sites.action.stop') }}</NButton>
        <NButton size="small" :disabled="busy" @click="service('restart')">{{ t('sites.process.restart') }}</NButton>
      </NSpace>
    </div>

    <NText v-if="engine.remote" depth="3" tag="p" class="note">{{ t('databases.remote.firewallNote') }}</NText>

    <NAlert v-if="engine.noCluster" type="warning" class="block">
      {{ t('databases.noCluster') }}
      <NButton v-if="canManage" size="small" class="block__action" :disabled="busy" @click="cluster">
        {{ t('databases.createCluster') }}
      </NButton>
    </NAlert>

    <NAlert v-if="engineError" type="error" class="block">
      <div>{{ engineError }}</div>
      <pre v-if="engineErrorDetail" class="detail" dir="ltr">{{ engineErrorDetail }}</pre>
    </NAlert>

    <div v-if="jobId" class="job">
      <JobProgress :job-id="jobId" @finished="finished" />
      <NButton size="small" quaternary @click="dismiss">{{ t('sites.create.close') }}</NButton>
    </div>
  </NCard>
</template>

<style scoped>
.header {
  margin-block-end: 12px;
}

.header__row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 14px;
}

.header__spacer {
  flex: 1;
}

.remote {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}

.note {
  margin: 8px 0 0;
  font-size: 13px;
}

.block {
  margin-block-start: 12px;
}

.block__action {
  margin-inline-start: 12px;
}

.detail {
  margin: 8px 0 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
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
</style>
