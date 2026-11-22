<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { History, RefreshCw } from '@lucide/vue'
import { isAbortError } from '@/api/client'
import { fetchJobs, type Job } from '@/api/jobs'
import DataTable from '@/components/common/DataTable.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import SelectField from '@/components/common/SelectField.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { useNow } from '@/composables/useNow'
import { errorText } from '@/i18n/errors'
import { formatPercent } from '@/i18n/format'
import { isFinalState, jobActorLabel, jobKindLabel, jobStateLabel, sourceLabel } from '@/jobs/labels'
import JobDrawer from './jobs/JobDrawer.vue'
import { STATE_FILTERS, jobDuration, jobTone, stateFilterLabel } from './jobs/labels'

// Background jobs ("list" skeleton): what ran, how it went, who started it
// and how long it took, newest first, with filters by state and kind kept
// in the address (?state=&kind=). A job's details and its live log open in
// a drawer (?open=job:<id>&section=overview|log), also from the job dock.
const PAGE_SIZE = 50
/** How often the list is refreshed while a shown job is still active. */
const REFRESH_MS = 3000

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const drawer = useDrawerRoute('job')
const now = useNow(1000)

const jobs = ref<Job[]>([])
const nextBefore = ref<number | null>(null)
const loaded = ref(false)
const loading = ref(false)
const loadingMore = ref(false)
const loadError = ref<unknown>(null)
const search = ref('')

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
  if (mode === 'reset') loading.value = true
  if (mode === 'more') loadingMore.value = true
  try {
    const page = await fetchJobs(
      {
        limit: PAGE_SIZE,
        before: mode === 'more' ? (nextBefore.value ?? undefined) : undefined,
        state: stateFilter.value || undefined,
        kind: kindFilter.value || undefined,
      },
      current.signal,
    )
    // Filtered, a job may have left the filter since: the newest page replaces the list.
    if (mode === 'reset' || (mode === 'refresh' && filtering.value)) {
      jobs.value = page.jobs
      nextBefore.value = page.nextBefore
    } else if (mode === 'more') {
      const known = new Set(jobs.value.map((job) => job.id))
      jobs.value = [...jobs.value, ...page.jobs.filter((job) => !known.has(job.id))]
      nextBefore.value = page.nextBefore
    } else {
      mergeNewest(page.jobs)
    }
    loadError.value = null
    loaded.value = true
  } catch (err) {
    if (isAbortError(err)) return
    if (mode !== 'refresh') loadError.value = err
  } finally {
    if (controller === current) {
      controller = null
      loading.value = false
      loadingMore.value = false
    }
    scheduleRefresh()
  }
}

/** Polls while any shown job is still queued or running, and while only active jobs are shown (new ones appear). */
function scheduleRefresh() {
  clearTimeout(timer)
  if (unmounted) return
  if (stateFilter.value !== 'active' && !jobs.value.some((job) => !isFinalState(job.state))) return
  timer = setTimeout(() => void load('refresh'), REFRESH_MS)
}

onMounted(() => void load('reset'))
onBeforeUnmount(() => {
  unmounted = true
  clearTimeout(timer)
  controller?.abort()
})

/** The drawer follows its job: keep the row current, and look for news when it ends. */
function onDrawerUpdate(job: Job) {
  const index = jobs.value.findIndex((entry) => entry.id === job.id)
  if (index < 0) return
  const before = jobs.value[index]
  jobs.value = jobs.value.map((entry) => (entry.id === job.id ? job : entry))
  if (before && !isFinalState(before.state) && isFinalState(job.state)) scheduleRefresh()
}

// Filters, kept in the address.
function queryValue(name: string): string {
  const value = route.query[name]
  return typeof value === 'string' ? value : ''
}

const stateFilter = computed<string>({
  get: () => {
    const value = queryValue('state')
    return (STATE_FILTERS as readonly string[]).includes(value) ? value : ''
  },
  set: (value) => setQuery('state', value),
})
const kindFilter = computed<string>({
  get: () => queryValue('kind'),
  set: (value) => setQuery('kind', value),
})

function setQuery(name: string, value: string) {
  const query = { ...route.query }
  if (value) query[name] = value
  else delete query[name]
  void router.replace({ query })
}

const stateOptions = computed(() => [
  { value: '', label: t('jobs.filter.allStates') },
  ...STATE_FILTERS.map((value) => ({ value, label: stateFilterLabel(value) })),
])

/** Every kind loaded while on the page: a kind filter shows only its own jobs, and the others stay offered. */
const seenKinds = ref<string[]>([])
watch(
  jobs,
  (list) => {
    const added = list.map((job) => job.kind).filter((kind) => !seenKinds.value.includes(kind))
    if (added.length > 0) seenKinds.value = [...new Set([...seenKinds.value, ...added])]
  },
  { immediate: true },
)

/** The kinds seen, by their short name. */
const kindOptions = computed(() => {
  const kinds = new Set(seenKinds.value)
  if (kindFilter.value) kinds.add(kindFilter.value)
  const options = [...kinds].map((kind) => ({ value: kind, label: jobKindLabel(kind) })).sort((a, b) => a.label.localeCompare(b.label))
  return [{ value: '', label: t('jobs.filter.allKinds') }, ...options]
})

const filtering = computed(() => stateFilter.value !== '' || kindFilter.value !== '')
// The server filters: a new filter loads the list again.
watch([stateFilter, kindFilter], () => void load('reset'))

const status = computed(() => {
  if (!loaded.value) return ''
  const running = jobs.value.filter((job) => job.state === 'running').length
  const queued = jobs.value.filter((job) => job.state === 'queued').length
  const parts: string[] = []
  if (running > 0) parts.push(t('jobs.status.running', { count: running }, running))
  if (queued > 0) parts.push(t('jobs.status.queued', { count: queued }, queued))
  if (parts.length === 0) parts.push(t('jobs.status.idle'))
  return parts.join(' · ')
})

const columns = computed<ColumnDef<Job>[]>(() => [
  { id: 'task', header: t('jobs.column.task'), enableSorting: false, meta: { class: 'min-w-[260px]' } },
  { id: 'state', header: t('jobs.column.state'), enableSorting: false, meta: { class: 'w-[160px]' } },
  { id: 'startedBy', header: t('jobs.column.startedBy'), enableSorting: false, meta: { class: 'w-[190px]' } },
  { id: 'started', header: t('jobs.column.started'), enableSorting: false, meta: { class: 'w-[150px]' } },
  { id: 'duration', header: t('jobs.column.duration'), enableSorting: false, meta: { class: 'w-[130px]' } },
])

/** Why a job failed, in one line. */
function failureText(job: Job): string {
  const error = job.error
  return error ? (errorText(error.code, error.params) ?? error.code) : ''
}

function searchText(job: Job): string {
  return [jobKindLabel(job.kind, job.params), jobStateLabel(job.state), jobActorLabel(job.username), sourceLabel(job.source), job.id].join(' ')
}
</script>

<template>
  <div>
    <PageHeader :title="t('jobs.title')" :status="status" :status-loading="!loaded && !loadError">
      <template #actions>
        <Button variant="outline" class="max-md:h-11" :disabled="loading" @click="load('reset')">
          <RefreshCw :class="loading ? 'animate-spin' : ''" aria-hidden="true" />
          {{ t('common.action.refresh') }}
        </Button>
      </template>
    </PageHeader>

    <div class="flex flex-col gap-3">
      <p class="m-0 text-detail text-muted-foreground">{{ t('jobs.intro') }}</p>

      <DataTable
        v-model:search="search"
        :data="jobs"
        :columns="columns"
        :row-key="(job) => job.id"
        :label="t('jobs.title')"
        :loading="loading && !loaded"
        :error="loadError"
        :search-placeholder="t('jobs.search')"
        :search-text="searchText"
        :empty-title="filtering ? t('jobs.filter.none') : t('jobs.empty')"
        :min-width="900"
        row-clickable
        @row-click="(job: Job) => drawer.open(job.id)"
        @retry="load('reset')"
      >
        <template #toolbar>
          <SelectField v-model="stateFilter" :options="stateOptions" :aria-label="t('jobs.filter.state')" class="w-44 max-md:h-11" />
          <SelectField v-model="kindFilter" :options="kindOptions" :aria-label="t('jobs.filter.kind')" class="w-56 max-md:h-11" />
        </template>
        <template #cell-task="{ row }">
          <span class="flex min-w-0 flex-col">
            <button
              type="button"
              class="w-fit max-w-full rounded-sm text-start font-semibold text-foreground underline decoration-border underline-offset-[3px] tap-y outline-none [overflow-wrap:anywhere] hover:decoration-current focus-visible:ring-2 focus-visible:ring-ring/50"
              @click="drawer.open(row.id)"
            >
              {{ jobKindLabel(row.kind, row.params) }}
            </button>
            <span v-if="row.state === 'failed' && row.error" class="truncate text-xs text-destructive" :title="failureText(row)">
              {{ failureText(row) }}
            </span>
          </span>
        </template>
        <template #cell-state="{ row }">
          <span class="inline-flex items-center gap-2">
            <StatusPill :tone="jobTone(row.state)" dot>{{ jobStateLabel(row.state) }}</StatusPill>
            <span v-if="row.state === 'running' && row.progress !== null" class="text-xs text-muted-foreground tabular-nums">
              {{ formatPercent(row.progress * 100, 0) }}
            </span>
          </span>
        </template>
        <template #cell-startedBy="{ row }">
          <span class="inline-flex min-w-0 flex-wrap items-center gap-1.5">
            <span class="truncate">{{ jobActorLabel(row.username) }}</span>
            <StatusPill>{{ sourceLabel(row.source) }}</StatusPill>
          </span>
        </template>
        <template #cell-started="{ row }">
          <RelativeTime v-if="row.startedAt > 0" :value="row.startedAt" class="text-muted-foreground" />
          <span v-else class="text-muted-foreground">{{ t('jobs.notStarted') }}</span>
        </template>
        <template #cell-duration="{ row }">
          <span class="tabular-nums" :class="jobDuration(row, now) ? '' : 'text-muted-foreground'">{{ jobDuration(row, now) ?? '—' }}</span>
        </template>
      </DataTable>

      <div v-if="nextBefore !== null && loaded" class="flex justify-center">
        <Button variant="outline" class="max-md:h-11" :disabled="loadingMore" @click="load('more')">
          <History aria-hidden="true" />
          {{ t('jobs.older') }}
        </Button>
      </div>
    </div>

    <JobDrawer @update="onDrawerUpdate" />
  </div>
</template>
