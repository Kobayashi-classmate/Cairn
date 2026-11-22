<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { History, RefreshCw } from '@lucide/vue'
import { isAbortError } from '@/api/client'
import { fetchRunLog, fetchRuns, type CronLog, type CronRun, type CronTask } from '@/api/cron'
import Card from '@/components/common/Card.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import SkeletonRows from '@/components/common/SkeletonRows.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { formatDuration } from '@/i18n/format'
import { cn } from '@/lib/utils'
import { resultText, resultTone } from '../labels'

// A task's runs, newest first, as the system journal keeps them, and the
// output of the run picked.
const props = defineProps<{ task: CronTask }>()

const { t } = useI18n()
const runs = ref<CronRun[] | null>(null)
const error = ref<unknown>(null)
const loading = ref(false)
const picked = ref<string | null>(null)
const log = ref<CronLog | null>(null)
const logLoading = ref(false)
const logError = ref<unknown>(null)
let controller: AbortController | null = null
let logController: AbortController | null = null

async function load() {
  controller?.abort()
  const request = new AbortController()
  controller = request
  loading.value = true
  try {
    runs.value = await fetchRuns(props.task.id, request.signal)
    error.value = null
    const first = runs.value.find((run) => run.invocation)
    if (first?.invocation && !picked.value) void showLog(first.invocation)
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

async function showLog(invocation: string) {
  logController?.abort()
  const request = new AbortController()
  logController = request
  picked.value = invocation
  logLoading.value = true
  logError.value = null
  try {
    log.value = await fetchRunLog(props.task.id, invocation, request.signal)
  } catch (err) {
    if (isAbortError(err)) return
    log.value = null
    logError.value = err
  } finally {
    if (logController === request) {
      logController = null
      logLoading.value = false
    }
  }
}

function reloadLog() {
  if (picked.value) void showLog(picked.value)
}

onMounted(load)
onBeforeUnmount(() => {
  controller?.abort()
  logController?.abort()
})
// A run that just ended shows up in the history.
watch(
  () => `${props.task.lastRun ?? 0}:${props.task.running}`,
  () => void load(),
)

function duration(run: CronRun): string {
  return run.finished ? formatDuration(Math.max(0, run.finished - run.started)) : '—'
}
</script>

<template>
  <div class="flex flex-wrap items-center justify-between gap-2">
    <p class="m-0 text-detail text-muted-foreground">{{ t('cron.runs.intro') }}</p>
    <Button variant="ghost" size="sm" :disabled="loading" @click="load">
      <RefreshCw :class="loading ? 'animate-spin' : ''" aria-hidden="true" />
      {{ t('common.action.refresh') }}
    </Button>
  </div>

  <Card v-if="error && !runs" :level="3" flush><ErrorState :error="error" compact @retry="load" /></Card>
  <Card v-else-if="!runs" :level="3" flush><SkeletonRows :rows="4" two-line /></Card>
  <Card v-else-if="runs.length === 0" :level="3" flush>
    <EmptyState :icon="History" compact :title="t('cron.runs.empty')" />
  </Card>
  <div v-else class="grid min-w-0 gap-4 md:grid-cols-[minmax(200px,260px)_minmax(0,1fr)]">
    <Card :level="3" flush>
      <ul class="m-0 max-h-[min(60vh,560px)] list-none divide-y overflow-y-auto p-0" :aria-label="t('cron.runs.list')">
        <li v-for="(run, index) in runs" :key="run.invocation || `run-${index}`">
          <button
            type="button"
            :disabled="!run.invocation"
            :aria-current="run.invocation && run.invocation === picked ? 'true' : undefined"
            :class="
              cn(
                'flex w-full flex-col gap-1 px-4 py-2.5 text-start outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset disabled:cursor-default',
                run.invocation && run.invocation === picked ? 'bg-primary-soft/60' : 'hover:bg-accent/60',
              )
            "
            @click="run.invocation && showLog(run.invocation)"
          >
            <span class="flex flex-wrap items-center gap-1.5">
              <StatusDot :tone="resultTone(run.result)">{{ resultText(run.result) }}</StatusDot>
              <StatusPill v-if="run.manual">{{ t('cron.runs.manual') }}</StatusPill>
            </span>
            <RelativeTime :value="run.started" class="text-detail" />
            <span class="text-xs text-muted-foreground tabular-nums">
              {{ t('cron.runs.meta', { duration: duration(run), code: run.exitCode ?? 0 }) }}
            </span>
          </button>
        </li>
      </ul>
    </Card>

    <Card :title="t('cron.runs.output')" :level="3" flush>
      <template v-if="picked" #actions>
        <Button variant="ghost" size="sm" :disabled="logLoading" @click="reloadLog">
          <RefreshCw :class="logLoading ? 'animate-spin' : ''" aria-hidden="true" />
          {{ t('common.action.refresh') }}
        </Button>
      </template>
      <ErrorState v-if="logError" :error="logError" compact @retry="reloadLog" />
      <div v-else-if="logLoading && !log" class="flex flex-col gap-2 px-4 py-4" aria-hidden="true">
        <Skeleton v-for="row in 5" :key="row" class="h-3.5" :style="{ width: `${90 - row * 9}%` }" />
      </div>
      <template v-else-if="log">
        <p v-if="log.truncated" class="m-0 border-b px-4 py-2 text-xs text-muted-foreground">{{ t('cron.runs.truncated') }}</p>
        <pre
          v-if="log.lines.length > 0"
          class="m-0 max-h-[min(60vh,560px)] overflow-auto px-4 py-3 font-mono text-xs leading-relaxed whitespace-pre-wrap [overflow-wrap:anywhere]"
          dir="ltr"
          role="log"
        >{{ log.lines.join('\n') }}</pre>
        <p v-else class="m-0 px-4 py-4 text-detail text-muted-foreground">{{ t('cron.runs.noOutput') }}</p>
      </template>
      <p v-else class="m-0 px-4 py-4 text-detail text-muted-foreground">{{ t('cron.runs.pick') }}</p>
    </Card>
  </div>
</template>
