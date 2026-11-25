<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RotateCw } from '@lucide/vue'
import { isAbortError, isApiError } from '@/api/client'
import { fetchAppLog, fetchAppStatus, type AppStatus, type SiteView } from '@/api/sites'
import Card from '@/components/common/Card.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import LogText from '@/components/logs/LogText'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { useNow } from '@/composables/useNow'
import { formatBytes, formatDateTime, formatDuration, formatNumber } from '@/i18n/format'
import type { Tone } from '@/lib/tone'
import type { InstalledRuntime } from '@/sites/useRuntimes'
import { restartApp } from '../actions'
import AppFields from '../AppFields.vue'
import SaveBar from './SaveBar.vue'
import { useSitePane } from './useSitePane'

// An app site's process: its systemd service (state, health, resources,
// restart), how it is run, and its journal.
const STATUS_MS = 5000
const LOG_MS = 2000
const MAX_LINES = 2000

const props = defineProps<{
  site: SiteView
  canManage: boolean
  runtimes: readonly InstalledRuntime[]
}>()

const { t, te } = useI18n()
const now = useNow(1000)
const site = toRef(props, 'site')
const { form, errors, saving, error, dirty, busy, save, reset } = useSitePane(site, [
  'app.command',
  'app.workDir',
  'app.port',
  'app.env',
  'app.memoryMb',
  'app.cpuPercent',
  'app.healthPath',
])

const status = ref<AppStatus | null>(null)
const statusError = ref<unknown>(null)
const restarting = ref(false)

const lines = ref<string[]>([])
const follow = ref(true)
const logError = ref<unknown>(null)
const box = ref<HTMLElement | null>(null)

let cursor: string | undefined
let statusTimer: ReturnType<typeof setTimeout> | undefined
let logTimer: ReturnType<typeof setTimeout> | undefined
let statusController: AbortController | null = null
let logController: AbortController | null = null
let unmounted = false

/**
 * An answer that stays the same however often it is asked (no systemd, the
 * app or site is gone, not allowed): polling stops; "Retry" starts it again.
 */
function permanent(err: unknown): boolean {
  return isApiError(err) && err.status >= 400 && err.status < 500 && err.status !== 408 && err.status !== 429
}

async function loadStatus() {
  clearTimeout(statusTimer)
  statusController?.abort()
  const controller = new AbortController()
  statusController = controller
  try {
    status.value = await fetchAppStatus(props.site.code, controller.signal)
    statusError.value = null
  } catch (err) {
    if (isAbortError(err)) return
    statusError.value = err
    if (permanent(err)) return
  }
  if (!unmounted) statusTimer = setTimeout(() => void loadStatus(), STATUS_MS)
}

async function loadLog() {
  clearTimeout(logTimer)
  logController?.abort()
  const controller = new AbortController()
  logController = controller
  try {
    const chunk = await fetchAppLog(props.site.code, cursor, controller.signal)
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
    if (permanent(err)) return
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
  statusController?.abort()
  logController?.abort()
})

async function restart() {
  restarting.value = true
  try {
    await restartApp(props.site)
  } finally {
    restarting.value = false
  }
  void loadStatus()
}

type ProcessState = 'running' | 'starting' | 'stopping' | 'failed' | 'stopped'

/** running / starting / stopping / failed / stopped, from systemd's states. */
const state = computed<ProcessState | null>(() => {
  const s = status.value
  if (!s) return null
  if (s.activeState === 'active') return s.subState === 'running' || !s.subState ? 'running' : 'starting'
  if (s.activeState === 'activating' || s.activeState === 'reloading') return 'starting'
  if (s.activeState === 'deactivating') return 'stopping'
  if (s.activeState === 'failed') return 'failed'
  return 'stopped'
})

const STATE_TONE: Record<ProcessState, Tone> = {
  running: 'success',
  starting: 'progress',
  stopping: 'progress',
  failed: 'destructive',
  stopped: 'neutral',
}

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
  <Card :title="t('sites.process.service')" :level="3" flush>
    <template v-if="canManage" #actions>
      <Button variant="outline" size="sm" :disabled="restarting || site.state === 'applying'" @click="restart">
        <RotateCw aria-hidden="true" />
        {{ t('sites.process.restart') }}
      </Button>
    </template>
    <ErrorState v-if="statusError && !status" :error="statusError" compact @retry="loadStatus" />
    <div v-else-if="!status" class="flex flex-col gap-3 px-4 py-4 md:px-[18px]" aria-hidden="true">
      <Skeleton class="h-4 w-1/3" />
      <Skeleton class="h-4 w-1/2" />
      <Skeleton class="h-4 w-2/5" />
    </div>
    <KeyValueList v-else-if="state">
      <KeyValueRow :label="t('sites.list.state')">
        <StatusPill :tone="STATE_TONE[state]" dot>{{ t(`sites.process.state.${state}`) }}</StatusPill>
        <StatusPill v-if="status.health" :tone="status.health === 'ok' ? 'success' : 'warning'">
          {{
            status.health === 'ok'
              ? t('sites.process.healthOk')
              : t('sites.process.healthFailing', { count: status.failures ?? 0 }, status.failures ?? 0)
          }}
        </StatusPill>
      </KeyValueRow>
      <KeyValueRow v-if="status.mainPid" :label="t('sites.process.pid')" mono>{{ status.mainPid }}</KeyValueRow>
      <KeyValueRow v-if="status.since" :label="t('sites.process.since')">
        {{ formatDateTime(status.since) }}
        <span v-if="uptime" class="text-muted-foreground">({{ uptime }})</span>
      </KeyValueRow>
      <KeyValueRow :label="t('sites.process.restarts')">{{ formatNumber(status.restarts) }}</KeyValueRow>
      <KeyValueRow v-if="status.memoryBytes" :label="t('sites.process.memory')">{{ formatBytes(status.memoryBytes) }}</KeyValueRow>
      <KeyValueRow v-if="status.cpuNanos" :label="t('sites.process.cpu')">
        {{ formatDuration(Math.round(status.cpuNanos / 1e9)) }}
      </KeyValueRow>
      <KeyValueRow v-if="status.result && status.result !== 'success'" :label="t('sites.process.lastExit')">
        <span class="text-destructive">{{ resultLabel(status.result) }}</span>
      </KeyValueRow>
    </KeyValueList>
  </Card>

  <Card :title="t('sites.process.config')" :level="3">
    <div class="flex flex-col gap-5">
      <AppFields
        :app="form.app"
        :errors="errors"
        :runtimes="runtimes"
        mode="edit"
        part="all"
        id-prefix="site-drawer-app"
        :disabled="!canManage || busy"
        :env-hidden="!canManage"
      />
    </div>
  </Card>
  <SaveBar v-if="canManage" :dirty="dirty" :saving="saving" :applying="busy && !saving" :error="error" @save="save" @reset="reset" />

  <Card :title="t('sites.process.journal')" :level="3" flush>
    <template #actions>
      <label class="inline-flex cursor-pointer items-center gap-2 text-detail">
        <Switch v-model="follow" :aria-label="t('sites.logs.follow')" />
        {{ t('sites.logs.follow') }}
      </label>
    </template>
    <ErrorState v-if="logError && lines.length === 0" :error="logError" compact @retry="loadLog" />
    <!-- On the card's own background: the log colours are checked against it. -->
    <div v-else ref="box" class="max-h-[min(60vh,520px)] overflow-auto px-4 py-3" role="log" :aria-label="t('sites.process.journal')" tabindex="0">
      <p v-if="lines.length === 0" class="m-0 text-detail text-muted-foreground">{{ t('sites.logs.empty') }}</p>
      <LogText v-else :text="lines.join('\n')" class="m-0 font-mono text-xs leading-relaxed" />
    </div>
  </Card>
</template>
