<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { ChevronDown, ListFilter, LoaderCircle } from '@lucide/vue'
import { isAbortError } from '@/api/client'
import { JOURNAL_MAX, JOURNAL_TIME, fetchJournal, fetchUnits, type JournalEntry } from '@/api/logs'
import ComboboxField from '@/components/common/ComboboxField.vue'
import FormField from '@/components/common/FormField.vue'
import LogView from '@/components/common/LogView.vue'
import SelectField from '@/components/common/SelectField.vue'
import type { SelectOption } from '@/components/common/types'
import { LogSpans } from '@/components/logs/LogText'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { byteLength } from '@/forms/rules'
import { validationMessage } from '@/i18n/errors'
import { formatLogTime, formatNumber } from '@/i18n/format'
import { useAnsiPalette } from '@/logs/ansiPalette'
import {
  DEFAULT_LIMIT,
  GREP_MAX_BYTES,
  LIMITS,
  PRIORITIES,
  entryText,
  filtersFrom,
  filtersQuery,
  journalQuery,
  priorityTone,
  sourceOf,
  unitName,
  type JournalFilters,
} from '@/logs/journal'
import { TONE_TEXT } from '@/lib/tone'
import { cn } from '@/lib/utils'

// The system journal: filters by unit, level, time and text (kept in the
// address), and following new entries by polling after the last one. At
// most KEEP_MAX entries stay on the page.
const emit = defineEmits<{ status: [text: string] }>()

/** Pause between polls while following. */
const FOLLOW_MS = 2500
/** Entries kept on the page while following; older ones are dropped. */
const KEEP_MAX = 5000

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
/** The colours of messages with ANSI colours. */
const palette = useAnsiPalette()

/** The filters in effect (from the address). */
const applied = computed(() => filtersFrom(route.query))
/** The filters as one value, so other address changes do not count as new filters. */
const appliedKey = computed(() => JSON.stringify(applied.value))
/** What is typed in the fields, applied by Show or Enter. */
const draft = reactive<JournalFilters>({ ...applied.value })

watch(appliedKey, () => Object.assign(draft, applied.value))

/** On phones the filters fold behind a button so the entries start on the first screen. */
const filtersOpen = ref(false)
/** How many filters differ from the defaults, shown on the folded button. */
const activeFilters = computed(() => {
  const f = applied.value
  return [f.unit, f.priority, f.since, f.until, f.grep, f.boot, f.limit !== DEFAULT_LIMIT].filter(Boolean).length
})

const units = ref<string[]>([])
const rows = ref<{ id: number; entry: JournalEntry }[]>([])
const cursor = ref('')
const loading = ref(false)
const loaded = ref(false)
const loadError = ref<unknown>(null)
const follow = ref(false)
const dropped = ref(false)
const wrap = ref(true)
let nextId = 0
let controller: AbortController | null = null
let timer: ReturnType<typeof setTimeout> | undefined
let stopped = false

const sinceError = computed(() => (draft.since && !JOURNAL_TIME.test(draft.since.trim()) ? validationMessage('since', 'journal_time') : null))
const untilError = computed(() => (draft.until && !JOURNAL_TIME.test(draft.until.trim()) ? validationMessage('until', 'journal_time') : null))
// The server counts the search text in bytes: a long Chinese query passes the field's character limit.
const grepError = computed(() => (byteLength(draft.grep) > GREP_MAX_BYTES ? validationMessage('grep', 'max') : null))
const draftInvalid = computed(() => !!sinceError.value || !!untilError.value || !!grepError.value)

const priorityOptions = computed<SelectOption[]>(() => [
  { value: '', label: t('logs.allPriorities') },
  ...PRIORITIES.map((p) => ({ value: String(p), label: t('logs.priorityAtLeast', { level: t(`logs.priority.${p}`) }) })),
])
const limitOptions = computed<SelectOption[]>(() =>
  LIMITS.map((n) => ({ value: String(n), label: t('logs.limitOption', { count: formatNumber(n) }) })),
)

/** Writes filters into the address; the list follows the address. */
function apply(next: Partial<JournalFilters> = {}) {
  Object.assign(draft, next)
  if (draftInvalid.value) return
  const unit = draft.unit.trim()
  const filters: JournalFilters = { ...draft, since: draft.since.trim(), until: draft.until.trim(), unit: unit && unitName(unit) }
  const query = filtersQuery(filters, route.query)
  if (JSON.stringify(filtersFrom(query)) === appliedKey.value) {
    void load()
    return
  }
  void router.replace({ query })
}

function wrapRows(entries: JournalEntry[]) {
  return entries.map((entry) => ({ id: nextId++, entry }))
}

async function load() {
  clearTimeout(timer)
  controller?.abort()
  const request = new AbortController()
  controller = request
  loading.value = true
  try {
    const journal = await fetchJournal({ ...journalQuery(applied.value), limit: applied.value.limit }, request.signal)
    if (controller !== request) return
    rows.value = wrapRows(journal.entries)
    cursor.value = journal.cursor ?? ''
    dropped.value = false
    loadError.value = null
    loaded.value = true
  } catch (err) {
    if (isAbortError(err) || controller !== request) return
    // The old entries belong to other filters: drop them rather than show them under these.
    rows.value = []
    cursor.value = ''
    dropped.value = false
    loaded.value = false
    loadError.value = err
    follow.value = false
  } finally {
    if (controller === request) {
      controller = null
      loading.value = false
    }
  }
  schedule()
}

async function poll() {
  // A load in flight continues the list itself.
  if (controller) return
  if (!follow.value) return schedule()
  const request = new AbortController()
  controller = request
  try {
    // Without a cursor (nothing was found, and the server knew no end of
    // the journal) ask again without the end time rather than idle.
    const query = cursor.value
      ? { ...journalQuery(applied.value), until: undefined, after: cursor.value, limit: JOURNAL_MAX }
      : { ...journalQuery(applied.value), until: undefined, limit: applied.value.limit }
    const journal = await fetchJournal(query, request.signal)
    if (controller !== request) return
    if (journal.entries.length > 0) {
      const next = rows.value.concat(wrapRows(journal.entries))
      if (next.length > KEEP_MAX) dropped.value = true
      rows.value = next.slice(-KEEP_MAX)
    }
    if (journal.cursor) cursor.value = journal.cursor
    loadError.value = null
  } catch (err) {
    if (isAbortError(err) || controller !== request) return
    loadError.value = err
  } finally {
    if (controller === request) controller = null
  }
  schedule()
}

function schedule() {
  clearTimeout(timer)
  if (!stopped && follow.value) timer = setTimeout(() => void poll(), FOLLOW_MS)
}

watch(follow, (on) => {
  if (on) void poll()
  else clearTimeout(timer)
})

watch(appliedKey, () => void load(), { immediate: true })

onMounted(async () => {
  try {
    units.value = await fetchUnits()
  } catch {
    // The unit can still be typed.
  }
})

onBeforeUnmount(() => {
  stopped = true
  clearTimeout(timer)
  controller?.abort()
})

const status = computed(() => {
  if (!loaded.value) return ''
  const parts = [t('logs.status.entries', { count: formatNumber(rows.value.length) }, rows.value.length)]
  if (applied.value.unit) parts.push(applied.value.unit)
  if (follow.value) parts.push(t('logs.status.following'))
  return parts.join(' · ')
})
watch(status, (value) => emit('status', value), { immediate: true })

function levelText(priority: number): string {
  return t(`logs.priorityShort.${priority}`)
}

function copyText(): string {
  return rows.value.map(({ entry }) => entryText(entry, formatLogTime(entry.time / 1000), levelText(entry.priority))).join('\n')
}

function toneClass(priority: number): string {
  const tone = priorityTone(priority)
  return tone ? TONE_TEXT[tone] : ''
}
</script>

<template>
  <div class="flex min-w-0 flex-col gap-3">
    <form class="flex flex-col gap-3 rounded-lg border bg-card px-4 py-3.5" novalidate @submit.prevent="apply()">
      <!-- Phones: a button folds the filters; following stays at hand. -->
      <div class="flex items-center gap-3 md:hidden">
        <Button
          type="button"
          variant="outline"
          :aria-expanded="filtersOpen"
          aria-controls="journal-filters"
          @click="filtersOpen = !filtersOpen"
        >
          <ListFilter aria-hidden="true" />
          {{ t('logs.filters') }}
          <span v-if="activeFilters > 0" class="rounded-full bg-primary-soft px-1.5 text-caption text-primary-soft-foreground tabular-nums">
            {{ activeFilters }}
          </span>
          <ChevronDown :class="cn('transition-transform', filtersOpen && 'rotate-180')" aria-hidden="true" />
        </Button>
        <label class="ms-auto flex h-11 cursor-pointer items-center gap-2 text-detail font-medium">
          <Switch v-model="follow" />
          {{ t('logs.follow') }}
        </label>
      </div>
      <div id="journal-filters" :class="cn('flex flex-col gap-3', !filtersOpen && 'max-md:hidden')">
        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)_minmax(0,0.8fr)_minmax(0,0.8fr)]">
          <FormField id="journal-unit" :label="t('common.field.unit')">
            <template #default="field">
              <ComboboxField
                v-bind="field"
                v-model="draft.unit"
                :options="units"
                :placeholder="t('logs.allUnits')"
                :clear-label="t('logs.clearUnit')"
                mono
                @commit="(unit: string) => apply({ unit })"
              />
            </template>
          </FormField>
          <FormField id="journal-priority" :label="t('logs.minimumLevel')">
            <template #default="field">
              <SelectField v-bind="field" :model-value="draft.priority" :options="priorityOptions" @update:model-value="(priority: string) => apply({ priority })" />
            </template>
          </FormField>
          <FormField id="journal-since" :label="t('common.field.since')" :error="sinceError">
            <template #default="field">
              <Input v-bind="field" v-model="draft.since" :placeholder="t('logs.sincePlaceholder')" spellcheck="false" autocomplete="off" class="font-mono max-md:h-11" dir="ltr" />
            </template>
          </FormField>
          <FormField id="journal-until" :label="t('common.field.until')" :error="untilError">
            <template #default="field">
              <Input v-bind="field" v-model="draft.until" :placeholder="t('logs.untilPlaceholder')" spellcheck="false" autocomplete="off" class="font-mono max-md:h-11" dir="ltr" />
            </template>
          </FormField>
        </div>
        <div class="flex flex-wrap items-end gap-x-4 gap-y-3">
          <FormField id="journal-grep" :label="t('logs.grep')" :error="grepError" class="min-w-0 flex-[1_1_220px]">
            <template #default="field">
              <Input v-bind="field" v-model="draft.grep" maxlength="200" type="search" spellcheck="false" autocomplete="off" class="max-md:h-11" />
            </template>
          </FormField>
          <FormField id="journal-limit" :label="t('common.field.limit')" class="w-36">
            <template #default="field">
              <SelectField v-bind="field" :model-value="String(draft.limit)" :options="limitOptions" @update:model-value="(limit: string) => apply({ limit: Number(limit) })" />
            </template>
          </FormField>
          <label class="flex h-9 cursor-pointer items-center gap-2 text-detail max-md:h-11">
            <Checkbox :model-value="draft.boot" @update:model-value="(boot: boolean | 'indeterminate') => apply({ boot: boot === true })" />
            {{ t('logs.boot') }}
          </label>
          <Button type="submit" class="max-md:h-11" :disabled="draftInvalid">
            <LoaderCircle v-if="loading" class="animate-spin" aria-hidden="true" />
            {{ t('logs.apply') }}
          </Button>
          <label class="ms-auto flex h-9 cursor-pointer items-center gap-2 text-detail font-medium max-md:hidden">
            <Switch v-model="follow" />
            {{ t('logs.follow') }}
          </label>
        </div>
        <p class="m-0 text-xs text-muted-foreground">{{ t('logs.timeHint') }}</p>
      </div>
    </form>

    <LogView
      v-model:wrap="wrap"
      :label="t('logs.tabs.system')"
      :loading="loading && !loaded"
      :error="loadError"
      :empty="rows.length === 0"
      :empty-text="t('logs.empty')"
      :copy-text="copyText"
      box-class="h-[max(360px,calc(100dvh-460px))]"
      @retry="load"
    >
      <template #toolbar>
        <span class="text-xs text-muted-foreground" role="status">
          <template v-if="dropped">{{ t('logs.dropped', { count: formatNumber(KEEP_MAX) }) }}</template>
          <template v-else-if="loaded">{{ t('logs.status.entries', { count: formatNumber(rows.length) }, rows.length) }}</template>
        </span>
      </template>
      <ol class="m-0 list-none p-0 py-1.5" :style="palette">
        <li
          v-for="{ id, entry } in rows"
          :key="id"
          :class="cn('flex items-start gap-x-3 px-3 py-0.5 hover:bg-accent/40 max-md:flex-wrap', entry.priority >= 7 && 'text-muted-foreground')"
        >
          <span class="shrink-0 text-muted-foreground tabular-nums">{{ formatLogTime(entry.time / 1000) }}</span>
          <span :class="cn('w-[5ch] shrink-0 font-semibold', toneClass(entry.priority))">{{ levelText(entry.priority) }}</span>
          <button
            v-if="entry.unit"
            type="button"
            class="-my-0.5 min-h-6 w-[30ch] shrink-0 truncate rounded-sm text-start max-md:-my-3 max-md:py-3 text-muted-foreground outline-none hover:text-foreground hover:underline focus-visible:ring-2 focus-visible:ring-ring/50 max-md:w-auto max-md:max-w-full"
            :title="t('logs.filterUnit', { unit: entry.unit })"
            @click="apply({ unit: entry.unit })"
          >
            {{ sourceOf(entry) }}
          </button>
          <span v-else class="w-[30ch] shrink-0 truncate text-muted-foreground max-md:w-auto max-md:max-w-full">{{ sourceOf(entry) }}</span>
          <LogSpans
            :text="entry.message"
            :tone="priorityTone(entry.priority)"
            :class="cn('min-w-0 flex-1 max-md:basis-full', wrap ? 'whitespace-pre-wrap [overflow-wrap:anywhere]' : 'whitespace-pre')"
          />
        </li>
      </ol>
    </LogView>
  </div>
</template>
