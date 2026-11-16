<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The system journal: filter by unit, priority, time and text, and follow
// new entries as they come. Site access and error logs stay on the sites'
// own pages; this page links to them.
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { NAlert, NButton, NCard, NCheckbox, NH2, NInput, NSelect, NSpin, NSwitch, NText } from 'naive-ui'
import { isAbortError } from '@/api/client'
import { JOURNAL_MAX, JOURNAL_TIME, fetchJournal, fetchUnits, type JournalEntry, type JournalQuery } from '@/api/logs'
import { fetchSites, type SiteView } from '@/api/sites'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { formatLogTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'

/** Pause between polls while following. */
const FOLLOW_MS = 2500
/** Entries kept on the page while following; older ones are dropped. */
const KEEP_MAX = 5000
const PRIORITIES = [1, 2, 3, 4, 5, 6, 7] as const
const LIMITS = [100, 200, 500, JOURNAL_MAX] as const

const { t } = useI18n()
const session = useSessionStore()

const units = ref<string[]>([])
const filters = reactive({
  unit: null as string | null,
  priority: null as number | null,
  since: '',
  until: '',
  grep: '',
  boot: false,
  limit: 200,
})
const entries = ref<JournalEntry[]>([])
const cursor = ref('')
const loading = ref(false)
const loadError = ref<unknown>(null)
const follow = ref(false)
const dropped = ref(false)
const list = ref<HTMLElement | null>(null)
const sites = ref<SiteView[]>([])
let controller: AbortController | null = null
let timer: ReturnType<typeof setTimeout> | undefined

const sinceError = computed(() => (filters.since && !JOURNAL_TIME.test(filters.since) ? validationMessage('since', 'journal_time') : ''))
const untilError = computed(() => (filters.until && !JOURNAL_TIME.test(filters.until) ? validationMessage('until', 'journal_time') : ''))

const unitOptions = computed(() => units.value.map((unit) => ({ value: unit, label: unit })))
const priorityOptions = computed(() => PRIORITIES.map((p) => ({ value: p, label: t('logs.priorityAtLeast', { level: t(`logs.priority.${p}`) }) })))
const limitOptions = computed(() => LIMITS.map((n) => ({ value: n, label: t('logs.limitOption', { count: n }) })))

function query(): JournalQuery {
  return {
    unit: filters.unit ?? undefined,
    priority: filters.priority ?? undefined,
    since: filters.since.trim() || undefined,
    until: filters.until.trim() || undefined,
    grep: filters.grep || undefined,
    boot: filters.boot || undefined,
  }
}

/** Whether the list is scrolled to (nearly) the newest entry. */
function atBottom(): boolean {
  const el = list.value
  return !el || el.scrollHeight - el.scrollTop - el.clientHeight < 40
}

async function scrollToBottom() {
  await nextTick()
  if (list.value) list.value.scrollTop = list.value.scrollHeight
}

async function load() {
  if (sinceError.value || untilError.value) return
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  try {
    const journal = await fetchJournal({ ...query(), limit: filters.limit }, current.signal)
    entries.value = journal.entries
    cursor.value = journal.cursor ?? ''
    dropped.value = false
    loadError.value = null
    await scrollToBottom()
  } catch (err) {
    if (isAbortError(err)) return
    loadError.value = err
    follow.value = false
  } finally {
    if (controller === current) loading.value = false
  }
  schedule()
}

async function poll() {
  if (!follow.value || !cursor.value) return schedule()
  const current = new AbortController()
  controller = current
  try {
    const journal = await fetchJournal({ ...query(), until: undefined, after: cursor.value, limit: JOURNAL_MAX }, current.signal)
    if (journal.entries.length > 0) {
      const stick = atBottom()
      const next = entries.value.concat(journal.entries)
      if (next.length > KEEP_MAX) dropped.value = true
      entries.value = next.slice(-KEEP_MAX)
      if (stick) await scrollToBottom()
    }
    if (journal.cursor) cursor.value = journal.cursor
    loadError.value = null
  } catch (err) {
    if (isAbortError(err)) return
    loadError.value = err
  }
  schedule()
}

function schedule() {
  clearTimeout(timer)
  if (follow.value) timer = setTimeout(() => void poll(), FOLLOW_MS)
}

watch(follow, (on) => {
  if (on) void poll()
  else clearTimeout(timer)
})

// Picking from a list applies right away; text fields apply on Enter or the button.
watch(() => [filters.unit, filters.priority, filters.boot, filters.limit], () => void load())

onMounted(async () => {
  void load()
  try {
    units.value = await fetchUnits()
  } catch {
    // The unit filter can still be typed.
  }
  if (session.can('sites.read')) {
    try {
      sites.value = (await fetchSites()).sites
    } catch {
      // Without the sites list there are no shortcuts to their logs.
    }
  }
})

onBeforeUnmount(() => {
  clearTimeout(timer)
  controller?.abort()
})

function source(entry: JournalEntry): string {
  const name = entry.identifier || entry.unit || ''
  return entry.pid ? `${name}[${entry.pid}]` : name
}

function level(priority: number): string {
  if (priority <= 3) return 'error'
  if (priority === 4) return 'warning'
  if (priority === 5) return 'notice'
  if (priority >= 7) return 'debug'
  return 'info'
}
</script>

<template>
  <div class="logs">
    <NH2 class="logs__title">{{ t('logs.title') }}</NH2>
    <NText depth="3" tag="p" class="intro">{{ t('logs.intro') }}</NText>

    <form class="filters" @submit.prevent="load">
      <NSelect
        v-model:value="filters.unit"
        :options="unitOptions"
        filterable
        tag
        clearable
        :placeholder="t('logs.allUnits')"
        class="filters__unit"
      />
      <NSelect v-model:value="filters.priority" :options="priorityOptions" clearable :placeholder="t('logs.allPriorities')" class="filters__priority" />
      <div class="filters__time">
        <NInput
          v-model:value="filters.since"
          :placeholder="t('logs.since')"
          :status="sinceError ? 'error' : undefined"
          :input-props="{ 'aria-label': t('logs.since'), spellcheck: false }"
          @keydown.enter.prevent="load"
        />
        <NInput
          v-model:value="filters.until"
          :placeholder="t('logs.until')"
          :status="untilError ? 'error' : undefined"
          :input-props="{ 'aria-label': t('logs.until'), spellcheck: false }"
          @keydown.enter.prevent="load"
        />
      </div>
      <NInput
        v-model:value="filters.grep"
        :placeholder="t('logs.grep')"
        :maxlength="200"
        clearable
        class="filters__grep"
        :input-props="{ 'aria-label': t('logs.grep'), spellcheck: false }"
        @keydown.enter.prevent="load"
      />
      <NSelect v-model:value="filters.limit" :options="limitOptions" class="filters__limit" />
      <NCheckbox v-model:checked="filters.boot">{{ t('logs.boot') }}</NCheckbox>
      <NButton type="primary" attr-type="submit" :loading="loading">{{ t('logs.apply') }}</NButton>
      <label class="follow">
        <NSwitch v-model:value="follow" size="small" />
        <span>{{ t('logs.follow') }}</span>
      </label>
    </form>
    <NText depth="3" tag="p" class="hint">{{ t('logs.timeHint') }}</NText>
    <NAlert v-if="sinceError || untilError" type="error" class="block">{{ sinceError || untilError }}</NAlert>

    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NText v-if="dropped" depth="3" tag="p" class="hint">{{ t('logs.dropped', { count: KEEP_MAX }) }}</NText>

    <NSpin :show="loading && entries.length === 0">
      <div ref="list" class="journal" role="log" :aria-label="t('logs.title')" dir="ltr">
        <div v-if="!loading && entries.length === 0 && !loadError" class="journal__empty">{{ t('logs.empty') }}</div>
        <div v-for="(entry, index) in entries" :key="`${entry.time}-${index}`" class="line" :class="`line--${level(entry.priority)}`">
          <span class="line__time">{{ formatLogTime(entry.time / 1000) }}</span>
          <span class="line__level">{{ t(`logs.priorityShort.${entry.priority}`) }}</span>
          <span class="line__source">{{ source(entry) }}</span>
          <span class="line__message">{{ entry.message }}</span>
        </div>
      </div>
    </NSpin>

    <NCard v-if="sites.length > 0" size="small" :title="t('logs.sites.title')" class="sites">
      <NText depth="3" tag="p" class="hint">{{ t('logs.sites.intro') }}</NText>
      <div class="sites__list">
        <RouterLink v-for="site in sites" :key="site.code" :to="{ name: 'site', params: { code: site.code }, query: { tab: 'logs' } }" class="sites__link">
          {{ site.domains[0] ?? site.code }}
        </RouterLink>
      </div>
    </NCard>
  </div>
</template>

<style scoped>
.logs {
  max-inline-size: 1400px;
}

.logs__title {
  margin: 0;
}

.intro {
  margin-block: 4px 12px;
  font-size: 13px;
}

.filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 10px;
}

.filters__unit {
  inline-size: 260px;
}

.filters__priority {
  inline-size: 190px;
}

.filters__time {
  display: flex;
  gap: 6px;
  inline-size: 320px;
}

.filters__grep {
  inline-size: 220px;
}

.filters__limit {
  inline-size: 130px;
}

.follow {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}

.hint {
  margin-block: 6px;
  font-size: 12px;
}

.block {
  margin-block: 8px;
}

.journal {
  block-size: max(360px, calc(100vh - 340px));
  overflow: auto;
  padding: 8px 10px;
  border-radius: 4px;
  background: #1e1e1e;
  color: #d4d4d4;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
}

.journal__empty {
  padding: 24px;
  text-align: center;
  opacity: 0.7;
}

.line {
  display: flex;
  gap: 10px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.line__time {
  flex: none;
  color: #8a8a8a;
}

.line__level {
  flex: none;
  inline-size: 5ch;
}

.line__source {
  flex: none;
  max-inline-size: 28ch;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: #9cdcfe;
}

.line__message {
  flex: 1;
  min-inline-size: 0;
}

.line--error .line__level,
.line--error .line__message {
  color: #f48771;
}

.line--warning .line__level,
.line--warning .line__message {
  color: #dcdcaa;
}

.line--notice .line__level {
  color: #4fc1ff;
}

.line--debug {
  opacity: 0.6;
}

.sites {
  margin-block-start: 16px;
}

.sites__list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 16px;
}

.sites__link {
  overflow-wrap: anywhere;
}
</style>
