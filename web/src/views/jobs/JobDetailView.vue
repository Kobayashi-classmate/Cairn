<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute } from 'vue-router'
import {
  NAlert,
  NButton,
  NCard,
  NH2,
  NIcon,
  NProgress,
  NSpin,
  NTag,
  NText,
  useMessage,
} from 'naive-ui'
import { ArrowBackOutline } from '@vicons/ionicons5'
import { isAbortError, isApiError } from '@/api/client'
import { cancelJob, followJobLog, type Job } from '@/api/jobs'
import JobStateTag from '@/components/JobStateTag.vue'
import { useErrorToast } from '@/composables/useErrorToast'
import { useNow } from '@/composables/useNow'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatDateTime, formatDuration, formatPercent } from '@/i18n/format'
import { isFinalState, jobActorLabel, jobKindLabel, sourceLabel } from '@/jobs/labels'

/** Longest wait the server allows for one long-poll request. */
const WAIT_SECONDS = 25
/** Pause before retrying after a failed request. */
const RETRY_MS = 3000
/** Keep at most this many characters of log in memory; older output is dropped. */
const MAX_LOG_CHARS = 1024 * 1024
/** Distance from the bottom (px) that still counts as "following" the log. */
const FOLLOW_SLACK_PX = 24

const { t } = useI18n()
const route = useRoute()
const message = useMessage()
const showError = useErrorToast()
const now = useNow(1000)

const job = ref<Job | null>(null)
const log = ref('')
const trimmed = ref(false)
const loadError = ref<unknown>(null)
/** The last request failed; the view keeps retrying. */
const reconnecting = ref(false)
const canceling = ref(false)

const logBox = ref<HTMLElement | null>(null)
/** Whether new output scrolls into view; off once the user scrolls up. */
const follow = ref(true)

let controller: AbortController | null = null

const final = computed(() => (job.value ? isFinalState(job.value.state) : false))

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms)
    signal.addEventListener('abort', () => {
      clearTimeout(timer)
      resolve()
    })
  })
}

function append(chunk: string) {
  if (!chunk) return
  let next = log.value + chunk
  if (next.length > MAX_LOG_CHARS) {
    next = next.slice(next.length - MAX_LOG_CHARS)
    trimmed.value = true
  }
  log.value = next
  if (follow.value) {
    void nextTick(() => {
      if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
    })
  }
}

function onLogScroll() {
  const box = logBox.value
  if (!box) return
  follow.value = box.scrollHeight - box.scrollTop - box.clientHeight <= FOLLOW_SLACK_PX
}

/** Follows the job's state and log until it is final and the log is complete. */
async function followJob(id: string, signal: AbortSignal) {
  let offset = 0
  let first = true
  while (!signal.aborted) {
    const started = Date.now()
    try {
      const chunk = await followJobLog(id, offset, first ? 0 : WAIT_SECONDS, signal)
      first = false
      reconnecting.value = false
      loadError.value = null
      const changed = chunk.log !== '' || chunk.job.state !== job.value?.state
      job.value = chunk.job
      append(chunk.log)
      offset = chunk.offset
      if (isFinalState(chunk.job.state) && offset >= chunk.logSize) return
      // Guard against a server that answers immediately without news.
      if (!changed && Date.now() - started < 500) await sleep(1000, signal)
    } catch (err) {
      if (isAbortError(err)) return
      if (isApiError(err) && (err.status === 404 || err.status === 401 || err.status === 403)) {
        loadError.value = err
        return
      }
      // Network trouble or a restarting panel: keep trying quietly.
      reconnecting.value = true
      if (!job.value) loadError.value = err
      await sleep(RETRY_MS, signal)
    }
  }
}

function start(id: string) {
  controller?.abort()
  const current = new AbortController()
  controller = current
  job.value = null
  log.value = ''
  trimmed.value = false
  loadError.value = null
  reconnecting.value = false
  follow.value = true
  void followJob(id, current.signal)
}

watch(
  () => route.params.id,
  (id) => {
    if (typeof id === 'string' && id) start(id)
  },
  { immediate: true },
)

onBeforeUnmount(() => controller?.abort())

async function cancel() {
  if (!job.value) return
  canceling.value = true
  try {
    job.value = await cancelJob(job.value.id)
    message.success(t('jobs.detail.cancelRequested'))
  } catch (err) {
    showError(err)
  } finally {
    canceling.value = false
  }
}

const duration = computed(() => {
  const current = job.value
  if (!current || current.startedAt === 0) return null
  const end = current.finishedAt > 0 ? current.finishedAt : now.value
  return formatDuration(Math.max(0, end - current.startedAt))
})

/** The job's own parameters, shown as plain text. */
const params = computed(() =>
  Object.entries(job.value?.params ?? {}).map(([key, value]) => ({
    key,
    value: typeof value === 'string' ? value : JSON.stringify(value),
  })),
)

const errorReason = computed(() => {
  const reason = job.value?.error?.params?.reason
  return typeof reason === 'string' ? reason : ''
})

function jobErrorText(code: string, errorParams: Record<string, string | number | boolean> = {}): string {
  return errorText(code, errorParams) ?? code
}
</script>

<template>
  <div class="job">
    <RouterLink :to="{ name: 'jobs' }" class="job__back">
      <NIcon><ArrowBackOutline /></NIcon>
      {{ t('jobs.title') }}
    </RouterLink>

    <NAlert v-if="loadError" type="error" class="job__block">{{ errorMessage(loadError) }}</NAlert>

    <NSpin :show="!job && !loadError">
      <template v-if="job">
        <header class="job__header">
          <NH2 class="job__title">{{ jobKindLabel(job.kind) }}</NH2>
          <JobStateTag :state="job.state" />
          <NTag v-if="reconnecting" size="small" :bordered="false" type="warning">
            {{ t('jobs.detail.reconnecting') }}
          </NTag>
          <NButton
            v-if="job.cancelable && !final"
            type="error"
            ghost
            size="small"
            :loading="canceling"
            class="job__cancel"
            @click="cancel"
          >
            {{ t('jobs.detail.cancel') }}
          </NButton>
        </header>

        <NCard size="small" class="job__block">
          <dl class="facts">
            <div>
              <dt><NText depth="3">{{ t('jobs.column.startedBy') }}</NText></dt>
              <dd>{{ jobActorLabel(job.username) }} · {{ sourceLabel(job.source) }}</dd>
            </div>
            <div>
              <dt><NText depth="3">{{ t('jobs.detail.created') }}</NText></dt>
              <dd>{{ formatDateTime(job.createdAt) }}</dd>
            </div>
            <div>
              <dt><NText depth="3">{{ t('jobs.column.started') }}</NText></dt>
              <dd>{{ job.startedAt > 0 ? formatDateTime(job.startedAt) : t('jobs.notStarted') }}</dd>
            </div>
            <div v-if="job.finishedAt > 0">
              <dt><NText depth="3">{{ t('jobs.detail.finished') }}</NText></dt>
              <dd>{{ formatDateTime(job.finishedAt) }}</dd>
            </div>
            <div v-if="duration">
              <dt><NText depth="3">{{ t('jobs.column.duration') }}</NText></dt>
              <dd>{{ duration }}</dd>
            </div>
            <div v-for="param in params" :key="param.key">
              <dt><NText depth="3" class="mono">{{ param.key }}</NText></dt>
              <dd class="mono" dir="ltr">{{ param.value }}</dd>
            </div>
          </dl>

          <NProgress
            v-if="job.progress !== null && !final"
            type="line"
            :percentage="Math.round(job.progress * 100)"
            class="job__progress"
          >
            {{ formatPercent(job.progress * 100, 0) }}
          </NProgress>

          <NAlert v-if="job.error" type="error" class="job__error">
            <div>{{ jobErrorText(job.error.code, job.error.params) }}</div>
            <code v-if="errorReason" class="mono reason" dir="ltr">{{ errorReason }}</code>
          </NAlert>
        </NCard>

        <NCard :title="t('jobs.detail.log')" size="small">
          <NText v-if="trimmed" depth="3" tag="p" class="job__trimmed">{{ t('jobs.detail.trimmed') }}</NText>
          <pre
            ref="logBox"
            class="log"
            dir="ltr"
            tabindex="0"
            :aria-label="t('jobs.detail.log')"
            @scroll="onLogScroll"
          >{{ log }}<span v-if="!final" class="log__cursor" aria-hidden="true">▍</span></pre>
          <NText v-if="!log && final" depth="3">{{ t('jobs.detail.noLog') }}</NText>
        </NCard>
      </template>
    </NSpin>
  </div>
</template>

<style scoped>
.job {
  max-inline-size: 1100px;
}

.job__back {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-block-end: 8px;
  color: inherit;
  text-decoration: none;
  opacity: 0.75;
}

.job__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
  margin-block-end: 16px;
}

.job__title {
  margin: 0;
}

.job__cancel {
  margin-inline-start: auto;
}

.job__block {
  margin-block-end: 16px;
}

.facts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 12px 24px;
  margin: 0;
}

.facts dd {
  margin: 2px 0 0;
  overflow-wrap: anywhere;
}

.job__progress {
  margin-block-start: 16px;
}

.job__error {
  margin-block-start: 16px;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
}

.reason {
  display: block;
  margin-block-start: 6px;
  overflow-wrap: anywhere;
}

.job__trimmed {
  margin-block: 0 8px;
  font-size: 13px;
}

.log {
  block-size: min(60vh, 560px);
  margin: 0;
  padding: 10px 12px;
  overflow: auto;
  border-radius: 4px;
  background: rgb(128 128 128 / 12%);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12.5px;
  line-height: 1.5;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.log__cursor {
  opacity: 0.5;
  animation: blink 1s steps(2, start) infinite;
}

@keyframes blink {
  to {
    visibility: hidden;
  }
}
</style>
