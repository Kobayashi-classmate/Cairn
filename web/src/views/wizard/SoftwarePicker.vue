<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check, Search, X } from '@lucide/vue'
import type { AcceptableValue } from 'reka-ui'
import { toast } from 'vue-sonner'
import {
  MAX_PROBE_ITEMS,
  isAvailable,
  type SoftwareCatalog,
  type SoftwareEntry,
  type Suite,
  type SuiteItem,
} from '@/api/software'
import Notice from '@/components/common/Notice.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { formatIsoDate } from '@/i18n/format'
import { categoryText, methodText } from '@/software/display'
import {
  alternativesOf,
  applyPreset,
  canAdd,
  defaultLine,
  itemState,
  offerOf,
  presetState,
  type PickedLine,
} from './selection'

// The software step of the first-login wizard: presets as starting points,
// the selection (one line per software, each row changeable and removable)
// and the catalog to add single software from.
const props = defineProps<{
  catalog: SoftwareCatalog
  suites: Suite[]
}>()

/** The selection, in the order the rows were added. */
const picked = defineModel<PickedLine[]>('picked', { required: true })

const { t, te } = useI18n()

/** From this many offered software on, the catalog list gets a search field. */
const SEARCH_FROM = 10
/** Order of the catalog's categories; unknown ones follow. */
const CATEGORY_ORDER = ['web', 'runtime', 'database', 'cache', 'tool']

/** The whole catalog by id: installed lines count even where they can no longer be installed. */
const byId = computed(() => new Map(props.catalog.software.map((e) => [e.id, e])))
const repos = computed(() => new Map(props.catalog.repos.map((repo) => [repo.id, repo])))
/** Software with at least one line that can be installed here. */
const offered = computed(() => props.catalog.software.filter((e) => e.offers.some(isAvailable)))

const full = computed(() => picked.value.length >= MAX_PROBE_ITEMS)
const isPicked = (id: string) => picked.value.some((p) => p.software === id)
const nameOf = (id: string) => byId.value.get(id)?.name ?? id

// ---- Presets ---------------------------------------------------------------

function suiteName(suite: Suite): string {
  const key = `wizard.suite.${suite.id}.name`
  return te(key) ? t(key) : suite.id
}

function suiteDescription(suite: Suite): string {
  const key = `wizard.suite.${suite.id}.description`
  return te(key) ? t(key) : ''
}

/** "Redis / Valkey": the choices offered here, or all of them when none is. */
function presetItemLabel(item: SuiteItem): string {
  const here = item.choices.filter((id) => offered.value.some((e) => e.id === id))
  return (here.length > 0 ? here : item.choices).map(nameOf).join(' / ')
}

function addPreset(suite: Suite) {
  const result = applyPreset(suite, picked.value, byId.value)
  picked.value = result.picked
  if (result.left > 0) toast.warning(t('wizard.selection.notAllAdded', { count: result.left, max: MAX_PROBE_ITEMS }, result.left))
}

// ---- Selection -------------------------------------------------------------

const rows = computed(() =>
  picked.value.flatMap((p) => {
    const entry = byId.value.get(p.software)
    if (!entry) return []
    return [
      {
        ...p,
        entry,
        offer: offerOf(byId.value, p.software, p.line),
        alternatives: alternativesOf(p.software, props.suites, picked.value, byId.value),
      },
    ]
  }),
)

/** Every line that can be installed here; installed ones are shown, not selectable. */
function lineOptions(entry: SoftwareEntry) {
  return entry.offers.filter(isAvailable).map((o) => {
    let label = o.line
    if (o.installed) label = t('wizard.software.installedLine', { line: o.line })
    else if (o.recommended) label = t('wizard.software.recommendedLine', { line: o.line })
    else if (o.endOfLife) label = t('wizard.software.endedLine', { line: o.line, date: o.eol ? formatIsoDate(o.eol) : '' })
    return { value: o.line, label, disabled: o.installed }
  })
}

function setLine(software: string, value: AcceptableValue) {
  if (typeof value !== 'string') return
  picked.value = picked.value.map((p) => (p.software === software ? { software, line: value } : p))
}

/** Puts an alternative (Valkey for Redis) in the row's place, in its default line. */
function switchSoftware(from: string, value: AcceptableValue) {
  if (typeof value !== 'string' || value === from) return
  const entry = byId.value.get(value)
  if (!canAdd(entry) || isPicked(value)) return
  picked.value = picked.value.map((p) => (p.software === from ? { software: value, line: defaultLine(entry) } : p))
}

function remove(software: string) {
  picked.value = picked.value.filter((p) => p.software !== software)
}

// ---- Catalog ---------------------------------------------------------------

const query = ref('')
const searchable = computed(() => offered.value.length >= SEARCH_FROM)

const groups = computed(() => {
  const needle = query.value.trim().toLowerCase()
  // Matches the name, the id or the category ("database").
  const shown = offered.value.filter(
    (e) =>
      !needle ||
      e.name.toLowerCase().includes(needle) ||
      e.id.includes(needle) ||
      categoryText(e.category).toLowerCase().includes(needle),
  )
  const byCategory = new Map<string, SoftwareEntry[]>()
  for (const entry of shown) byCategory.set(entry.category, [...(byCategory.get(entry.category) ?? []), entry])
  const rank = (category: string) => {
    const index = CATEGORY_ORDER.indexOf(category)
    return index < 0 ? CATEGORY_ORDER.length : index
  }
  return [...byCategory.entries()]
    .sort(([a], [b]) => rank(a) - rank(b))
    .map(([category, list]) => ({ category, list }))
})

function toggle(entry: SoftwareEntry, on: boolean) {
  if (!on) {
    remove(entry.id)
  } else if (!isPicked(entry.id) && !full.value && canAdd(entry)) {
    picked.value = [...picked.value, { software: entry.id, line: defaultLine(entry) }]
  }
}
</script>

<template>
  <div class="flex flex-col gap-6">
    <Notice v-if="!catalog.supported" tone="warning">{{ t('software.unsupported') }}</Notice>

    <!-- Presets: starting points that add to the selection. -->
    <section v-if="suites.length > 0" aria-labelledby="wizard-presets">
      <h2 id="wizard-presets" class="m-0 text-section">{{ t('wizard.preset.title') }}</h2>
      <p class="mt-1 mb-3 text-detail text-muted-foreground">{{ t('wizard.preset.intro') }}</p>
      <div class="grid gap-4 sm:grid-cols-2">
        <article
          v-for="suite in suites"
          :key="suite.id"
          class="flex flex-col rounded-lg border border-border bg-card p-4"
        >
          <h3 :id="`preset-${suite.id}`" class="m-0 text-body font-semibold">{{ suiteName(suite) }}</h3>
          <p v-if="suiteDescription(suite)" class="mt-1 mb-0 text-detail text-muted-foreground">
            {{ suiteDescription(suite) }}
          </p>
          <ul class="mt-2 mb-3 flex list-disc flex-col gap-0.5 ps-5 text-detail">
            <li v-for="(item, index) in suite.items" :key="index">
              <span class="inline-flex flex-wrap items-center gap-1.5">
                <span :class="{ 'text-muted-foreground': itemState(item, picked, byId) === 'missing' }">
                  {{ presetItemLabel(item) }}
                </span>
                <span v-if="itemState(item, picked, byId) === 'missing'" class="text-muted-foreground">
                  {{ t('wizard.software.missing') }}
                </span>
                <Badge v-else-if="itemState(item, picked, byId) === 'installed'">{{ t('software.installed') }}</Badge>
                <Badge v-else-if="itemState(item, picked, byId) === 'picked'" variant="secondary">
                  <Check aria-hidden="true" />
                  {{ t('wizard.preset.picked') }}
                </Badge>
              </span>
            </li>
          </ul>
          <div class="mt-auto flex flex-wrap items-center gap-2">
            <span
              v-if="presetState(suite, picked, byId) !== 'open'"
              class="inline-flex items-center gap-1.5 text-detail text-muted-foreground"
            >
              <Check
                v-if="presetState(suite, picked, byId) !== 'unavailable'"
                class="size-4 text-success"
                aria-hidden="true"
              />
              {{ t(`wizard.preset.state.${presetState(suite, picked, byId)}`) }}
            </span>
            <Button
              v-else
              variant="outline"
              size="sm"
              :disabled="full"
              :aria-describedby="`preset-${suite.id}`"
              @click="addPreset(suite)"
            >
              {{ t('wizard.preset.add') }}
            </Button>
          </div>
        </article>
      </div>
    </section>

    <!-- The selection: one line per software. -->
    <section aria-labelledby="wizard-selection">
      <div class="mb-2 flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2 id="wizard-selection" class="m-0 text-section">{{ t('wizard.selection.title') }}</h2>
        <span class="text-detail text-muted-foreground" role="status">
          {{ t('wizard.software.count', { count: picked.length, max: MAX_PROBE_ITEMS }) }}
        </span>
        <Button v-if="picked.length > 0" variant="link" size="sm" class="ms-auto" @click="picked = []">
          {{ t('wizard.selection.clear') }}
        </Button>
      </div>
      <p
        v-if="rows.length === 0"
        class="m-0 rounded-lg border border-dashed border-border px-4 py-5 text-center text-detail text-muted-foreground"
      >
        {{ t('wizard.selection.empty') }}
      </p>
      <!-- Keyed by position: swapping Redis for Valkey or removing a row keeps the keyboard focus in place. -->
      <ul v-else class="m-0 list-none rounded-lg border border-border bg-card p-0">
        <li
          v-for="(row, index) in rows"
          :key="index"
          class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-border px-4 py-2.5 last:border-b-0"
        >
          <div class="min-w-0 flex-1 basis-48">
            <div class="flex flex-wrap items-center gap-2">
              <Select
                v-if="row.alternatives.length > 0"
                :model-value="row.software"
                @update:model-value="(value) => switchSoftware(row.software, value)"
              >
                <SelectTrigger
                  size="sm"
                  class="font-medium"
                  :aria-label="t('wizard.selection.alternative', { names: [row.software, ...row.alternatives].map(nameOf).join(' / ') })"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="id in [row.software, ...row.alternatives]" :key="id" :value="id">
                    {{ nameOf(id) }}
                  </SelectItem>
                </SelectContent>
              </Select>
              <span v-else class="font-medium">{{ row.entry.name }}</span>
              <Badge v-if="row.offer?.endOfLife" variant="warning">{{ t('software.endOfLife.badge') }}</Badge>
            </div>
            <p v-if="row.offer" class="m-0 mt-0.5 text-caption text-muted-foreground">{{ methodText(row.offer, repos) }}</p>
          </div>
          <div class="flex items-center gap-2 max-sm:w-full">
            <Select :model-value="row.line" @update:model-value="(value) => setLine(row.software, value)">
              <SelectTrigger
                size="sm"
                class="min-w-0 flex-1 sm:w-60 sm:flex-none"
                :aria-label="t('wizard.software.lineOf', { name: row.entry.name })"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem
                  v-for="option in lineOptions(row.entry)"
                  :key="option.value"
                  :value="option.value"
                  :disabled="option.disabled"
                >
                  {{ option.label }}
                </SelectItem>
              </SelectContent>
            </Select>
            <Button
              variant="ghost"
              size="icon"
              :aria-label="t('wizard.selection.remove', { name: row.entry.name })"
              @click="remove(row.software)"
            >
              <X aria-hidden="true" />
            </Button>
          </div>
        </li>
      </ul>
      <Notice v-if="full" tone="warning" class="mt-3">{{ t('wizard.selection.full', { max: MAX_PROBE_ITEMS }) }}</Notice>
    </section>

    <!-- The catalog: add single software. -->
    <section aria-labelledby="wizard-catalog">
      <div class="mb-3 flex flex-wrap items-center gap-x-3 gap-y-2">
        <h2 id="wizard-catalog" class="m-0 text-section">{{ t('wizard.catalog.title') }}</h2>
        <label
          v-if="searchable"
          class="ms-auto flex h-9 min-w-0 flex-[1_1_220px] items-center gap-2 rounded-md border border-input bg-card px-2.5 text-muted-foreground focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/50 sm:max-w-72 max-md:h-11"
        >
          <Search class="size-4 shrink-0" aria-hidden="true" />
          <input
            v-model="query"
            type="search"
            :aria-label="t('software.search')"
            :placeholder="t('software.search')"
            class="h-full min-w-0 flex-1 bg-transparent text-base text-foreground outline-none placeholder:text-muted-foreground md:text-sm [&::-webkit-search-cancel-button]:hidden"
          />
          <button
            v-if="query"
            type="button"
            class="inline-flex size-6 shrink-0 items-center justify-center rounded-sm outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 max-md:-me-2.5 max-md:size-11"
            :aria-label="t('common.table.clearSearch')"
            @click="query = ''"
          >
            <X class="size-3.5" aria-hidden="true" />
          </button>
        </label>
      </div>
      <p v-if="groups.length === 0" class="m-0 text-detail text-muted-foreground" role="status">
        {{ query ? t('common.table.noMatches', { query }) : t('wizard.catalog.empty') }}
      </p>
      <div v-for="group in groups" :key="group.category" class="mb-4 last:mb-0">
        <h3 class="m-0 mb-2 text-detail font-semibold text-muted-foreground">{{ categoryText(group.category) }}</h3>
        <ul class="m-0 grid list-none gap-x-6 gap-y-2 p-0 sm:grid-cols-2 lg:grid-cols-3">
          <li v-for="entry in group.list" :key="entry.id" class="flex min-h-8 items-center gap-2">
            <Checkbox
              :id="`software-${entry.id}`"
              :model-value="isPicked(entry.id)"
              :disabled="!isPicked(entry.id) && (full || !canAdd(entry))"
              @update:model-value="(on) => toggle(entry, on === true)"
            />
            <label :for="`software-${entry.id}`" class="min-w-0 truncate">{{ entry.name }}</label>
            <Badge v-if="!isPicked(entry.id) && !canAdd(entry)">{{ t('software.installed') }}</Badge>
          </li>
        </ul>
      </div>
    </section>
  </div>
</template>
