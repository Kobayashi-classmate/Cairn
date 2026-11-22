<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter, type RouteLocationRaw } from 'vue-router'
import { FilterX, LoaderCircle, Search, X } from '@lucide/vue'
import { isAbortError, isApiError } from '@/api/client'
import { fetchAudit, fetchAuditEntry, type AuditEntry } from '@/api/audit'
import { fetchDnsProviders } from '@/api/dns'
import DataTable from '@/components/common/DataTable.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import type { SegmentedOption } from '@/components/common/segmented'
import SelectField from '@/components/common/SelectField.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { SelectOption } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { certsAvailable } from '@/certs/useCertChoices'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { OPEN_QUERY, SECTION_QUERY } from '@/composables/usePageTab'
import { formatNumber } from '@/i18n/format'
import AuditEntrySheet from './audit/AuditEntrySheet.vue'
import { AUDIT_CATEGORIES, actionDetails, describeAction, resultText, sourceText } from './audit/describe'
import { filtersFrom, filtersQuery, rangeStart, type AuditFilters, type RangeFilter, type ResultFilter } from './audit/filters'

// The audit trail ("list" skeleton): who did what, when, from where and how
// it went, newest first. Filters live in the address and the server applies
// them. "Load more" fetches older pages. A row opens its details in a
// sheet (`?open=entry:<id>`).
const PAGE_SIZE = 50
const FILTER_DELAY_MS = 400

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const drawer = useDrawerRoute('entry')

const entries = ref<AuditEntry[]>([])
const nextBefore = ref<number | null>(null)
const loading = ref(false)
const loaded = ref(false)
const loadError = ref<unknown>(null)

/** The filters in effect (from the address). */
const filters = computed(() => filtersFrom(route.query))
/** What the server filters on; other address changes do not reload. */
const serverKey = computed(() => Object.values(filters.value).join('\u0000'))

/** The user filter as typed; applied after a short pause. */
const userDraft = ref(filters.value.user)
watch(
  () => filters.value.user,
  (user) => {
    if (user !== userDraft.value.trim()) userDraft.value = user
  },
)

let controller: AbortController | null = null
/** The start of the time range when the list was (re)loaded; older pages keep it. */
let since: number | undefined

/** Loads the newest page (reset) or the next older one. */
async function load(reset: boolean) {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  if (reset) since = rangeStart(filters.value.range, Math.floor(Date.now() / 1000)) || undefined
  try {
    const page = await fetchAudit(
      {
        limit: PAGE_SIZE,
        before: reset ? undefined : (nextBefore.value ?? undefined),
        user: filters.value.user || undefined,
        action: filters.value.category ? `${filters.value.category}.` : undefined,
        result: filters.value.result || undefined,
        since,
      },
      current.signal,
    )
    entries.value = reset ? page.entries : [...entries.value, ...page.entries]
    nextBefore.value = page.nextBefore
    loadError.value = null
    loaded.value = true
  } catch (err) {
    if (isAbortError(err)) return
    loadError.value = err
  } finally {
    if (controller === current) {
      controller = null
      loading.value = false
    }
  }
}

onMounted(() => void load(true))
watch(serverKey, () => void load(true))
onBeforeUnmount(() => controller?.abort())

/** DNS provider product names by id; entries show the id until (or unless) they are known. */
const providerNames = ref<Record<string, string>>({})
onMounted(async () => {
  if (!certsAvailable()) return
  try {
    providerNames.value = Object.fromEntries((await fetchDnsProviders()).map((p) => [p.id, p.name]))
  } catch {
    // Provider ids remain readable on their own.
  }
})

function setFilters(patch: Partial<AuditFilters>) {
  const next = { ...filters.value, ...patch }
  void router.replace({ query: filtersQuery(next, route.query) })
}

let filterTimer: ReturnType<typeof setTimeout> | undefined
watch(userDraft, (value) => {
  clearTimeout(filterTimer)
  filterTimer = setTimeout(() => {
    if (value.trim() !== filters.value.user) setFilters({ user: value.trim() })
  }, FILTER_DELAY_MS)
})
onBeforeUnmount(() => clearTimeout(filterTimer))

const rows = computed(() => entries.value)
const canLoadMore = computed(() => nextBefore.value !== null)

const anyFilter = computed(() => Object.values(filters.value).some((value) => value !== ''))

function clearFilters() {
  userDraft.value = ''
  setFilters({ user: '', category: '', result: '', range: '' })
}

const categoryOptions = computed<SelectOption[]>(() => [
  { value: '', label: t('audit.filter.category') },
  ...AUDIT_CATEGORIES.map((c) => ({ value: c, label: t(`audit.category.${c}`) })),
])
const resultOptions = computed<SegmentedOption<ResultFilter | 'all'>[]>(() => [
  { value: 'all', label: t('audit.filter.anyResult') },
  { value: 'ok', label: t('audit.result.ok') },
  { value: 'failed', label: t('audit.result.failed') },
])
const rangeOptions = computed<SelectOption[]>(() => [
  { value: '', label: t('audit.filter.anyTime') },
  { value: '24h', label: t('audit.filter.range.24h') },
  { value: '7d', label: t('audit.filter.range.7d') },
  { value: '30d', label: t('audit.filter.range.30d') },
])

const status = computed(() => {
  if (!loaded.value) return ''
  const shown = rows.value.length
  return canLoadMore.value
    ? t('audit.status.newest', { count: formatNumber(shown) }, shown)
    : t('audit.status.all', { count: formatNumber(shown) }, shown)
})

const columns = computed<ColumnDef<AuditEntry>[]>(() => [
  { id: 'time', header: t('audit.column.time'), enableSorting: false, meta: { class: 'w-[140px]' } },
  { id: 'user', header: t('audit.column.user'), enableSorting: false, meta: { class: 'w-[140px]' } },
  { id: 'action', header: t('audit.column.action'), enableSorting: false, meta: { class: 'min-w-[300px]' } },
  { id: 'result', header: t('audit.column.result'), enableSorting: false, meta: { class: 'min-w-[170px] max-w-[260px]' } },
  { id: 'source', header: t('audit.column.source'), enableSorting: false, meta: { class: 'w-[110px]' } },
  { id: 'remote', header: t('audit.column.remote'), enableSorting: false, meta: { class: 'w-[150px]' } },
])

// The entry of the open sheet: from the loaded pages, or fetched on its own
// (a link or a reload). The newest entry below id+1 is the entry itself.
const fetchedEntry = ref<AuditEntry | null>(null)
const entryError = ref<unknown>(null)
const entryLoading = ref(false)
const openId = computed(() => {
  const value = drawer.id.value
  return value !== null && /^\d+$/.test(value) ? Number(value) : null
})
const openEntry = computed(
  () => entries.value.find((entry) => entry.id === openId.value) ?? (fetchedEntry.value?.id === openId.value ? fetchedEntry.value : null),
)

async function fetchEntry() {
  const id = openId.value
  if (id === null || entries.value.some((entry) => entry.id === id)) return
  entryLoading.value = true
  entryError.value = null
  try {
    fetchedEntry.value = await fetchAuditEntry(id)
  } catch (err) {
    if (isApiError(err) && err.status === 404) drawer.close()
    else entryError.value = err
  } finally {
    entryLoading.value = false
  }
}

watch([openId, loaded], () => {
  // An address that names no entry closes the sheet.
  if (drawer.id.value !== null && openId.value === null) drawer.close()
  else if (loaded.value) void fetchEntry()
})

/** The log filtered by a user or category, with no sheet open. */
function filterLocation(filter: { user?: string; category?: string }): RouteLocationRaw {
  const query = filtersQuery({ ...filters.value, ...filter }, route.query)
  delete query[OPEN_QUERY]
  delete query[SECTION_QUERY]
  return { query }
}

function openRow(entry: AuditEntry) {
  drawer.open(String(entry.id))
}
</script>

<template>
  <div>
    <PageHeader :title="t('audit.title')" :status="status" :status-loading="!loaded && !loadError" />

    <div class="flex min-w-0 flex-col overflow-hidden rounded-lg border bg-card">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b px-4 py-3">
        <label
          class="flex h-9 min-w-0 flex-[1_1_200px] items-center gap-2 rounded-md border border-input bg-card px-2.5 text-muted-foreground focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/50 sm:max-w-64 max-md:h-11"
        >
          <Search class="size-4 shrink-0" aria-hidden="true" />
          <input
            v-model="userDraft"
            type="search"
            :aria-label="t('audit.filter.user')"
            :placeholder="t('audit.filter.user')"
            autocapitalize="none"
            spellcheck="false"
            class="h-full min-w-0 flex-1 bg-transparent text-base text-foreground outline-none placeholder:text-muted-foreground md:text-sm [&::-webkit-search-cancel-button]:hidden"
            @keydown.enter.prevent="setFilters({ user: userDraft.trim() })"
          />
          <button
            v-if="userDraft"
            type="button"
            class="inline-flex size-6 shrink-0 items-center justify-center rounded-sm outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
            :aria-label="t('audit.filter.clearUser')"
            @click="userDraft = ''"
          >
            <X class="size-3.5" aria-hidden="true" />
          </button>
        </label>
        <SelectField
          :model-value="filters.category"
          :options="categoryOptions"
          :aria-label="t('audit.filter.categoryLabel')"
          class="w-full sm:w-52 max-md:h-11"
          @update:model-value="(value: string) => setFilters({ category: value })"
        />
        <SelectField
          :model-value="filters.range"
          :options="rangeOptions"
          :aria-label="t('audit.filter.rangeLabel')"
          class="w-full sm:w-44 max-md:h-11"
          @update:model-value="(value: string) => setFilters({ range: value as RangeFilter })"
        />
        <SegmentedControl
          :model-value="filters.result || 'all'"
          :options="resultOptions"
          :label="t('audit.filter.resultLabel')"
          class="w-full sm:w-auto"
          @update:model-value="(value) => setFilters({ result: value === 'all' ? '' : value })"
        />
        <Button v-if="anyFilter" variant="ghost" size="sm" class="max-md:h-11" @click="clearFilters">
          <FilterX aria-hidden="true" />
          {{ t('audit.filter.clear') }}
        </Button>
      </div>

      <DataTable
        flat
        row-clickable
        :data="rows"
        :columns="columns"
        :row-key="(entry) => String(entry.id)"
        :label="t('audit.title')"
        :loading="loading && !loaded"
        :error="loadError"
        :empty-title="anyFilter ? t('audit.empty.filtered') : t('audit.empty.title')"
        :empty-text="anyFilter && canLoadMore ? t('audit.empty.loadMore') : undefined"
        :min-width="1060"
        :skeleton-rows="8"
        @retry="load(entries.length === 0)"
        @row-click="openRow"
      >
        <template #cell-time="{ row }"><RelativeTime :value="row.ts" /></template>
        <template #cell-user="{ row }">
          <span v-if="row.username" class="truncate" dir="ltr">{{ row.username }}</span>
          <span v-else class="text-muted-foreground">{{ t('audit.noUser') }}</span>
        </template>
        <template #cell-action="{ row }">
          <span class="flex flex-wrap items-center gap-1.5">
            <button
              type="button"
              class="rounded-sm text-start underline decoration-border underline-offset-[3px] outline-none [overflow-wrap:anywhere] hover:decoration-current focus-visible:ring-2 focus-visible:ring-ring/50"
              :title="row.action"
              @click="openRow(row)"
            >
              {{ describeAction(row, providerNames) }}
            </button>
            <StatusPill v-for="text in actionDetails(row)" :key="text">{{ text }}</StatusPill>
          </span>
        </template>
        <template #cell-result="{ row }">
          <StatusDot :tone="row.result === 'ok' ? 'success' : 'destructive'" :title="row.result === 'ok' ? undefined : row.result">
            {{ resultText(row) }}
          </StatusDot>
        </template>
        <template #cell-source="{ row }">{{ sourceText(row.source) }}</template>
        <template #cell-remote="{ row }"><span class="font-mono text-[12.5px]" dir="ltr">{{ row.remote }}</span></template>
        <template v-if="anyFilter" #empty>
          <Button variant="outline" size="sm" @click="clearFilters">{{ t('audit.filter.clear') }}</Button>
        </template>
      </DataTable>

      <div v-if="canLoadMore && loaded" class="flex justify-center border-t px-4 py-3">
        <Button variant="outline" class="max-md:h-11" :disabled="loading" @click="load(false)">
          <LoaderCircle v-if="loading" class="animate-spin" aria-hidden="true" />
          {{ t('audit.loadOlder') }}
        </Button>
      </div>
    </div>

    <AuditEntrySheet
      :open="drawer.id.value !== null"
      :entry="openEntry"
      :loading="entryLoading"
      :error="entryError"
      :provider-names="providerNames"
      :filter-location="filterLocation"
      @close="drawer.close()"
      @retry="fetchEntry"
    />
  </div>
</template>
