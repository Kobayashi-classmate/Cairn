<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NH2,
  NIcon,
  NSpace,
  NTag,
  NText,
  type DataTableColumns,
} from 'naive-ui'
import { RefreshOutline } from '@vicons/ionicons5'
import { isAbortError } from '@/api/client'
import { fetchJobs, type Job } from '@/api/jobs'
import JobStateTag from '@/components/JobStateTag.vue'
import { useNow } from '@/composables/useNow'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime, formatDuration, formatPercent } from '@/i18n/format'
import { isFinalState, jobActorLabel, jobKindLabel, sourceLabel } from '@/jobs/labels'

const PAGE_SIZE = 50
/** How often the list is refreshed while a shown job is still active. */
const REFRESH_MS = 3000

const { t } = useI18n()
const now = useNow(1000)

const jobs = ref<Job[]>([])
const nextBefore = ref<number | null>(null)
const loading = ref(false)
const loadError = ref<unknown>(null)

let controller: AbortController | null = null
let timer: ReturnType<typeof setTimeout> | undefined
let unmounted = false

/** Merges the newest page into the list: updated jobs are replaced, new ones prepended. */
function mergeNewest(page: Job[]) {
  const fresh = new Map(page.map((job) => [job.id, job]))
  const kept = jobs.value.map((job) => fresh.get(job.id) ?? job)
  const known = new Set(jobs.value.map((job) => job.id))
  jobs.value = [...page.filter((job) => !known.has(job.id)), ...kept]
}

async function load(mode: 'reset' | 'more' | 'refresh') {
  // A background refresh never interrupts a request the user asked for.
  if (mode === 'refresh' && controller) return
  controller?.abort()
  const current = new AbortController()
  controller = current
  if (mode !== 'refresh') loading.value = true
  try {
    const page = await fetchJobs(
      { limit: PAGE_SIZE, before: mode === 'more' ? (nextBefore.value ?? undefined) : undefined },
      current.signal,
    )
    if (mode === 'reset') {
      jobs.value = page.jobs
      nextBefore.value = page.nextBefore
    } else if (mode === 'more') {
      jobs.value = [...jobs.value, ...page.jobs]
      nextBefore.value = page.nextBefore
    } else {
      mergeNewest(page.jobs)
    }
    loadError.value = null
  } catch (err) {
    if (isAbortError(err)) return
    if (mode !== 'refresh') loadError.value = err
  } finally {
    if (controller === current) {
      controller = null
      loading.value = false
    }
    scheduleRefresh()
  }
}

/** Polls while any shown job is still queued or running. */
function scheduleRefresh() {
  clearTimeout(timer)
  if (unmounted || !jobs.value.some((job) => !isFinalState(job.state))) return
  timer = setTimeout(() => void load('refresh'), REFRESH_MS)
}

onMounted(() => load('reset'))
onBeforeUnmount(() => {
  unmounted = true
  clearTimeout(timer)
  controller?.abort()
})

function duration(job: Job): string {
  if (job.startedAt === 0) return '-'
  const end = job.finishedAt > 0 ? job.finishedAt : now.value
  return formatDuration(Math.max(0, end - job.startedAt))
}

const columns = computed<DataTableColumns<Job>>(() => [
  {
    key: 'kind',
    title: t('jobs.column.task'),
    minWidth: 220,
    render: (job) =>
      h(RouterLink, { to: { name: 'job', params: { id: job.id } }, class: 'link' }, () =>
        jobKindLabel(job.kind),
      ),
  },
  {
    key: 'state',
    title: t('jobs.column.state'),
    width: 170,
    render: (job) =>
      h(NSpace, { size: 6, align: 'center', wrap: false }, () => [
        h(JobStateTag, { state: job.state }),
        job.state === 'running' && job.progress !== null
          ? h(NText, { depth: 3 }, () => formatPercent(job.progress! * 100, 0))
          : null,
      ]),
  },
  {
    key: 'username',
    title: t('jobs.column.startedBy'),
    width: 180,
    render: (job) =>
      h(NSpace, { size: 6, align: 'center', wrap: false }, () => [
        jobActorLabel(job.username),
        h(NTag, { size: 'small', bordered: false }, () => sourceLabel(job.source)),
      ]),
  },
  {
    key: 'startedAt',
    title: t('jobs.column.started'),
    width: 180,
    render: (job) =>
      job.startedAt > 0 ? formatDateTime(job.startedAt) : h(NText, { depth: 3 }, () => t('jobs.notStarted')),
  },
  { key: 'duration', title: t('jobs.column.duration'), width: 150, render: duration },
])
</script>

<template>
  <div class="jobs">
    <header class="jobs__header">
      <NH2 class="jobs__title">{{ t('jobs.title') }}</NH2>
      <NButton :loading="loading" @click="load('reset')">
        <template #icon>
          <NIcon><RefreshOutline /></NIcon>
        </template>
        {{ t('common.action.refresh') }}
      </NButton>
    </header>
    <NText depth="3" tag="p" class="jobs__intro">{{ t('jobs.intro') }}</NText>

    <NAlert v-if="loadError" type="error" class="jobs__alert">{{ errorMessage(loadError) }}</NAlert>

    <NCard size="small">
      <NDataTable
        :columns="columns"
        :data="jobs"
        :loading="loading"
        :row-key="(job: Job) => job.id"
        :scroll-x="900"
        size="small"
      >
        <template #empty>
          <NText depth="3">{{ t('jobs.empty') }}</NText>
        </template>
      </NDataTable>
      <div v-if="nextBefore !== null" class="jobs__more">
        <NButton :loading="loading" @click="load('more')">{{ t('common.action.loadMore') }}</NButton>
      </div>
    </NCard>
  </div>
</template>

<style scoped>
.jobs {
  max-inline-size: 1200px;
}

.jobs__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 16px;
}

.jobs__title {
  margin: 0;
}

.jobs__intro {
  margin-block: 4px 16px;
}

.jobs__alert {
  margin-block-end: 16px;
}

.jobs__more {
  display: flex;
  justify-content: center;
  margin-block-start: 12px;
}

:deep(.link) {
  color: inherit;
  font-weight: 500;
}
</style>
