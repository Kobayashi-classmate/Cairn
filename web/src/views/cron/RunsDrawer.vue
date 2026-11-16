<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// A task's runs, newest first, with the output of the one picked.
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NDrawer, NDrawerContent, NEmpty, NSpin, NTag, NText } from 'naive-ui'
import { isAbortError } from '@/api/client'
import { fetchRunLog, fetchRuns, type CronLog, type CronRun, type CronTask } from '@/api/cron'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime, formatDuration } from '@/i18n/format'
import { resultType } from './labels'

const props = defineProps<{ task: CronTask | null }>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const runs = ref<CronRun[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)
const picked = ref<string | null>(null)
const log = ref<CronLog | null>(null)
const logLoading = ref(false)
const logError = ref<unknown>(null)
let controller: AbortController | null = null

async function load() {
  const task = props.task
  if (!task) return
  loading.value = true
  try {
    runs.value = await fetchRuns(task.id)
    loadError.value = null
    const first = runs.value.find((run) => run.invocation)
    if (first?.invocation && !picked.value) void showLog(first.invocation)
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

async function showLog(invocation: string) {
  const task = props.task
  if (!task) return
  controller?.abort()
  const current = new AbortController()
  controller = current
  picked.value = invocation
  logLoading.value = true
  logError.value = null
  try {
    log.value = await fetchRunLog(task.id, invocation, current.signal)
  } catch (err) {
    if (isAbortError(err)) return
    log.value = null
    logError.value = err
  } finally {
    if (controller === current) logLoading.value = false
  }
}

watch(
  () => props.task?.id,
  (id) => {
    controller?.abort()
    runs.value = []
    picked.value = null
    log.value = null
    logError.value = null
    if (id) void load()
  },
  { immediate: true },
)

function duration(run: CronRun): string {
  return run.finished ? formatDuration(Math.max(0, run.finished - run.started)) : '-'
}
</script>

<template>
  <NDrawer :show="task !== null" placement="right" width="min(820px, 100vw)" @update:show="(open: boolean) => !open && emit('close')">
    <NDrawerContent :title="task ? t('cron.runs.title', { name: task.name }) : ''" closable :native-scrollbar="false">
      <div class="head">
        <NText depth="3">{{ t('cron.runs.intro') }}</NText>
        <NButton size="small" :loading="loading" @click="load">{{ t('common.action.refresh') }}</NButton>
      </div>
      <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
      <NEmpty v-else-if="!loading && runs.length === 0" :description="t('cron.runs.empty')" class="empty" />
      <div v-else class="runs">
        <ul class="runs__list">
          <li v-for="(run, index) in runs" :key="run.invocation || `run-${index}`">
            <button
              type="button"
              class="run"
              :class="{ 'run--picked': run.invocation && run.invocation === picked }"
              :disabled="!run.invocation"
              @click="run.invocation && showLog(run.invocation)"
            >
              <span class="run__time">{{ formatDateTime(run.started) }}</span>
              <span class="run__tags">
                <NTag size="small" :type="resultType(run.result)" :bordered="false">{{ t(`cron.result.${run.result}`) }}</NTag>
                <NTag v-if="run.manual" size="small" :bordered="false">{{ t('cron.runs.manual') }}</NTag>
              </span>
              <NText depth="3" class="run__meta">
                {{ t('cron.runs.meta', { duration: duration(run), code: run.exitCode ?? 0 }) }}
              </NText>
            </button>
          </li>
        </ul>
        <div class="output">
          <NSpin :show="logLoading">
            <NAlert v-if="logError" type="error">{{ errorMessage(logError) }}</NAlert>
            <template v-else-if="log">
              <NText v-if="log.truncated" depth="3" tag="p" class="output__note">{{ t('cron.runs.truncated') }}</NText>
              <pre v-if="log.lines.length > 0" class="output__text" dir="ltr">{{ log.lines.join('\n') }}</pre>
              <NText v-else depth="3">{{ t('cron.runs.noOutput') }}</NText>
            </template>
            <NText v-else depth="3">{{ t('cron.runs.pick') }}</NText>
          </NSpin>
        </div>
      </div>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-block-end: 12px;
  font-size: 13px;
}

.block {
  margin-block-end: 12px;
}

.empty {
  padding-block: 40px;
}

.runs {
  display: grid;
  grid-template-columns: minmax(200px, 260px) minmax(0, 1fr);
  gap: 12px;
}

.runs__list {
  margin: 0;
  padding: 0;
  list-style: none;
  max-block-size: calc(100vh - 180px);
  overflow-y: auto;
}

.run {
  display: flex;
  flex-direction: column;
  gap: 4px;
  inline-size: 100%;
  padding: 8px;
  border: 0;
  border-radius: 3px;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: start;
  cursor: pointer;
}

.run:disabled {
  cursor: default;
}

.run:hover:not(:disabled),
.run:focus-visible,
.run--picked {
  background: rgba(128, 128, 128, 0.12);
}

.run__tags {
  display: flex;
  gap: 4px;
}

.run__meta {
  font-size: 12px;
}

.output {
  min-inline-size: 0;
}

.output__note {
  margin: 0 0 6px;
  font-size: 12px;
}

.output__text {
  margin: 0;
  padding: 10px;
  max-block-size: calc(100vh - 200px);
  overflow: auto;
  border-radius: 3px;
  background: #1e1e1e;
  color: #d4d4d4;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

@media (max-width: 700px) {
  .runs {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
