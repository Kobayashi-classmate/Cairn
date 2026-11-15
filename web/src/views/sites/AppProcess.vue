<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NSwitch, NTag, NText } from 'naive-ui'
import { isAbortError } from '@/api/client'
import type { Job } from '@/api/jobs'
import { fetchAppLog, fetchAppStatus, restartSite, type AppStatus } from '@/api/sites'
import JobProgress from '@/components/JobProgress.vue'
import { useErrorToast } from '@/composables/useErrorToast'
import { useNow } from '@/composables/useNow'
import { errorMessage } from '@/i18n/errors'
import { formatBytes, formatDateTime, formatDuration, formatNumber } from '@/i18n/format'

/** Status refresh while the tab is open. */
const STATUS_MS = 5000
/** Journal polling while following. */
const LOG_MS = 2000
/** Journal lines kept in memory. */
const MAX_LINES = 2000

const props = defineProps<{ code: string; canManage: boolean }>()

const { t, te } = useI18n()
const showError = useErrorToast()
const now = useNow(1000)

const status = ref<AppStatus | null>(null)
const statusError = ref<unknown>(null)
const restarting = ref(false)
const jobId = ref<string | null>(null)
/** The restart job has not finished yet. */
const jobRunning = ref(false)

const lines = ref<string[]>([])
const follow = ref(true)
const logError = ref<unknown>(null)
const box = ref<HTMLElement | null>(null)

let cursor: string | undefined
let statusTimer: ReturnType<typeof setTimeout> | undefined
let logTimer: ReturnType<typeof setTimeout> | undefined
let logController: AbortController | null = null
let unmounted = false

async function loadStatus() {
  clearTimeout(statusTimer)
  try {
    status.value = await fetchAppStatus(props.code)
    statusError.value = null
  } catch (err) {
    statusError.value = err
  }
  if (!unmounted) statusTimer = setTimeout(() => void loadStatus(), STATUS_MS)
}

async function loadLog() {
  clearTimeout(logTimer)
  logController?.abort()
  const current = new AbortController()
  logController = current
  try {
    const chunk = await fetchAppLog(props.code, cursor, current.signal)
    if (chunk.lines.length) {
      const next = [...lines.value, ...chunk.lines]
      lines.value = next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next
      if (follow.value) {
        void nextTick(() => {
          if (box.value) box.value.scrollTop = box.value.scrollHeight
        })
      }
    }
    if (chunk.cursor) cursor = chunk.cursor
    logError.value = null
  } catch (err) {
    if (isAbortError(err)) return
    logError.value = err
  }
  if (!unmounted && follow.value) logTimer = setTimeout(() => void loadLog(), LOG_MS)
}

onMounted(() => {
  void loadStatus()
  void loadLog()
})

watch(follow, (on) => {
  if (on) void loadLog()
  else clearTimeout(logTimer)
})

onBeforeUnmount(() => {
  unmounted = true
  clearTimeout(statusTimer)
  clearTimeout(logTimer)
  logController?.abort()
})

async function restart() {
  restarting.value = true
  try {
    jobId.value = (await restartSite(props.code)).id
    jobRunning.value = true
  } catch (err) {
    showError(err)
  } finally {
    restarting.value = false
  }
}

function onFinished(_job: Job) {
  jobRunning.value = false
  void loadStatus()
}

/** running / starting / stopping / failed / stopped, from systemd's states. */
const state = computed(() => {
  const s = status.value
  if (!s) return null
  if (s.activeState === 'active') return s.subState === 'running' || !s.subState ? 'running' : 'starting'
  if (s.activeState === 'activating' || s.activeState === 'reloading') return 'starting'
  if (s.activeState === 'deactivating') return 'stopping'
  if (s.activeState === 'failed') return 'failed'
  return 'stopped'
})

const stateType = computed(() => {
  switch (state.value) {
    case 'running':
      return 'success'
    case 'failed':
      return 'error'
    case 'starting':
    case 'stopping':
      return 'info'
    default:
      return 'default'
  }
})

function resultLabel(result: string): string {
  const key = `sites.process.result.${result}`
  return te(key) ? t(key) : result
}

const uptime = computed(() => {
  const since = status.value?.since
  return since && state.value === 'running' ? formatDuration(Math.max(0, now.value - since)) : ''
})
</script>

<template>
  <div class="process">
    <NCard size="small" class="block">
      <NAlert v-if="statusError" type="error" class="block">{{ errorMessage(statusError) }}</NAlert>
      <template v-if="status && state">
        <div class="head">
          <NTag :type="stateType" :bordered="false">{{ t(`sites.process.state.${state}`) }}</NTag>
          <NTag v-if="status.health" :type="status.health === 'ok' ? 'success' : 'warning'" :bordered="false">
            {{
              status.health === 'ok'
                ? t('sites.process.healthOk')
                : t('sites.process.healthFailing', { count: status.failures ?? 0 }, status.failures ?? 0)
            }}
          </NTag>
          <span class="spacer" />
          <NButton v-if="canManage" :loading="restarting || jobRunning" @click="restart">
            {{ t('sites.process.restart') }}
          </NButton>
        </div>
        <dl class="facts">
          <template v-if="status.mainPid">
            <dt>{{ t('sites.process.pid') }}</dt>
            <dd class="mono">{{ status.mainPid }}</dd>
          </template>
          <template v-if="status.since">
            <dt>{{ t('sites.process.since') }}</dt>
            <dd>
              {{ formatDateTime(status.since) }}
              <NText v-if="uptime" depth="3">({{ uptime }})</NText>
            </dd>
          </template>
          <dt>{{ t('sites.process.restarts') }}</dt>
          <dd>{{ formatNumber(status.restarts) }}</dd>
          <template v-if="status.memoryBytes">
            <dt>{{ t('sites.process.memory') }}</dt>
            <dd>{{ formatBytes(status.memoryBytes) }}</dd>
          </template>
          <template v-if="status.cpuNanos">
            <dt>{{ t('sites.process.cpu') }}</dt>
            <dd>{{ formatDuration(Math.round(status.cpuNanos / 1e9)) }}</dd>
          </template>
          <template v-if="status.result && status.result !== 'success'">
            <dt>{{ t('sites.process.lastExit') }}</dt>
            <dd>{{ resultLabel(status.result) }}</dd>
          </template>
        </dl>
      </template>
      <JobProgress v-if="jobId" :job-id="jobId" class="job" @finished="onFinished" />
    </NCard>

    <NCard :title="t('sites.process.journal')" size="small">
      <template #header-extra>
        <label class="follow">
          <NSwitch v-model:value="follow" size="small" />
          <span>{{ t('sites.logs.follow') }}</span>
        </label>
      </template>
      <NAlert v-if="logError" type="error" class="block">{{ errorMessage(logError) }}</NAlert>
      <div ref="box" class="journal">
        <NText v-if="lines.length === 0" depth="3">{{ t('sites.logs.empty') }}</NText>
        <pre v-else dir="ltr">{{ lines.join('\n') }}</pre>
      </div>
    </NCard>
  </div>
</template>

<style scoped>
.block {
  margin-block-end: 12px;
}

.head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-block-end: 12px;
}

.spacer {
  flex: 1;
}

.facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 6px 20px;
  margin: 0;
}

.facts dt {
  opacity: 0.7;
}

.facts dd {
  margin: 0;
}

.job {
  margin-block-start: 12px;
}

.follow {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}

.journal {
  max-block-size: min(60vh, 560px);
  overflow: auto;
  padding: 10px 12px;
  border-radius: 4px;
  background: rgb(128 128 128 / 8%);
}

.journal pre {
  margin: 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
