<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
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
import { cancelJob } from '@/api/jobs'
import JobStateTag from '@/components/JobStateTag.vue'
import { useErrorToast } from '@/composables/useErrorToast'
import { useJobFollow } from '@/composables/useJobFollow'
import { useNow } from '@/composables/useNow'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatDateTime, formatDuration, formatPercent } from '@/i18n/format'
import { formatJobParam, isFinalState, jobActorLabel, jobKindLabel, sourceLabel } from '@/jobs/labels'

/** Distance from the bottom (px) that still counts as "following" the log. */
const FOLLOW_SLACK_PX = 24

const { t } = useI18n()
const route = useRoute()
const message = useMessage()
const showError = useErrorToast()
const now = useNow(1000)

const { job, log, trimmed, loadError, reconnecting, start } = useJobFollow()
const canceling = ref(false)

const logBox = ref<HTMLElement | null>(null)
/** Whether new output scrolls into view; off once the user scrolls up. */
const follow = ref(true)

const final = computed(() => (job.value ? isFinalState(job.value.state) : false))

watch(log, () => {
  if (!follow.value) return
  void nextTick(() => {
    if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
  })
})

function onLogScroll() {
  const box = logBox.value
  if (!box) return
  follow.value = box.scrollHeight - box.scrollTop - box.clientHeight <= FOLLOW_SLACK_PX
}

watch(
  () => route.params.id,
  (id) => {
    if (typeof id === 'string' && id) {
      follow.value = true
      start(id)
    }
  },
  { immediate: true },
)

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
    value: formatJobParam(value),
  })),
)

/** Technical details of a failure (`reason`, program `output`), shown as is. */
const errorDetails = computed(() => {
  const params = job.value?.error?.params ?? {}
  return ['reason', 'output', 'detail']
    .map((key) => params[key])
    .filter((value): value is string => typeof value === 'string' && value !== '')
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
          <NH2 class="job__title">{{ jobKindLabel(job.kind, job.params) }}</NH2>
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
            <code v-for="(detail, index) in errorDetails" :key="index" class="mono reason" dir="ltr">{{
              detail
            }}</code>
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
  max-block-size: 240px;
  margin-block-start: 6px;
  overflow: auto;
  white-space: pre-wrap;
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
