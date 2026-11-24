<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Download, ExternalLink, Settings2 } from '@lucide/vue'
import { isAvailable, isEndOfLifeInstalled, type Offer, type SoftwareEntry } from '@/api/software'
import DataTable from '@/components/common/DataTable.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import type { Tone } from '@/lib/tone'
import { cn } from '@/lib/utils'
import { safeHttpsUrl } from '@/markdown/parse'
import {
  LINE_STATE_TONE,
  categoryIcon,
  categoryText,
  installableOffers,
  installedOffers,
  installedVersion,
  lineState,
  lineStateText,
  sourceText,
  type LineState,
} from '@/software/display'
import { useSoftwareStore } from '@/stores/software'

// The software table: one row per product with its installed lines (each
// opens the settings drawer), the recommended line, where it comes from,
// its state and Install / Settings.
defineProps<{
  software: readonly SoftwareEntry[]
  loading: boolean
  error: unknown
  canManage: boolean
  emptyTitle: string
  emptyText?: string
}>()

const emit = defineEmits<{ open: [software: string, line: string]; install: [software: string]; retry: [] }>()

const { t } = useI18n()
const store = useSoftwareStore()
const search = ref('')

const columns = computed<ColumnDef<SoftwareEntry>[]>(() => [
  { id: 'name', header: t('software.column.name'), accessorFn: (entry) => entry.name.toLowerCase(), meta: { class: 'min-w-[220px]' } },
  { id: 'installed', header: t('software.column.installed'), enableSorting: false, meta: { class: 'w-[210px]' } },
  { id: 'recommended', header: t('software.column.recommended'), enableSorting: false, meta: { class: 'w-[130px]' } },
  { id: 'source', header: t('software.column.source'), enableSorting: false, meta: { class: 'w-[170px]' } },
  { id: 'status', header: t('software.column.status'), enableSorting: false, meta: { class: 'w-[150px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[200px] text-end' } },
])

function searchText(entry: SoftwareEntry): string {
  return [entry.id, entry.name, categoryText(entry.category), ...entry.offers.map((o) => o.line)].join(' ')
}

function recommended(entry: SoftwareEntry): Offer | null {
  return entry.offers.find((o) => o.recommended && isAvailable(o)) ?? null
}

/** The line the source column describes: the installed one, else the recommended one. */
function sourceOffer(entry: SoftwareEntry): Offer | null {
  return installedOffers(entry)[0] ?? recommended(entry) ?? entry.offers.find(isAvailable) ?? null
}

/** Worst first, so a failed line shows even when another one runs. */
const STATE_ORDER: readonly LineState[] = ['failed', 'partial', 'stopped', 'running', 'installed']

interface StatusCell {
  tone: Tone
  text: string
}

function status(entry: SoftwareEntry): StatusCell {
  const job = store.runningFor(entry.id)
  if (job) return { tone: 'progress', text: t(`software.progress.${job.kind.replace('software.', '')}`) }
  const installed = installedOffers(entry)
  if (installed.length > 0) {
    const states = installed.map(lineState)
    const worst = STATE_ORDER.find((state) => states.includes(state)) ?? 'installed'
    return { tone: LINE_STATE_TONE[worst], text: lineStateText(worst) }
  }
  if (!entry.offers.some(isAvailable)) return { tone: 'neutral', text: t('software.notAvailable') }
  return { tone: 'neutral', text: t('software.notInstalled') }
}

function homepage(entry: SoftwareEntry): string | null {
  return safeHttpsUrl(entry.homepage)
}

function versionTitle(offer: Offer): string | undefined {
  return installedVersion(offer) || undefined
}

/** The pill's name for screen readers; an ended line says so, as its badge does. */
function openLineLabel(entry: SoftwareEntry, offer: Offer): string {
  const named = { name: entry.name, line: offer.line, state: lineStateText(lineState(offer)) }
  return isEndOfLifeInstalled(offer) ? t('software.endOfLife.openLine', named) : t('software.openLine', named)
}
</script>

<template>
  <DataTable
    v-model:search="search"
    :data="software"
    :columns="columns"
    :row-key="(entry) => entry.id"
    :label="t('software.title')"
    :loading="loading"
    :error="error"
    :search-placeholder="t('software.search')"
    :search-text="searchText"
    :empty-title="emptyTitle"
    :empty-text="emptyText"
    :min-width="980"
    :initial-sort="[{ id: 'name', desc: false }]"
    @retry="emit('retry')"
  >
    <template #toolbar>
      <span class="text-detail text-muted-foreground tabular-nums">
        {{ t('software.count', { count: software.length }, software.length) }}
      </span>
    </template>

    <template #cell-name="{ row }">
      <div class="flex min-w-0 items-center gap-3">
        <span
          class="inline-flex size-8 shrink-0 items-center justify-center rounded-md bg-primary-soft text-primary-soft-foreground [&_svg]:size-4"
          aria-hidden="true"
        >
          <component :is="categoryIcon(row.category)" />
        </span>
        <div class="flex min-w-0 flex-col">
          <span class="flex min-w-0 items-center gap-1.5">
            <button
              v-if="installedOffers(row).length > 0"
              type="button"
              class="truncate rounded-sm text-start font-semibold text-foreground underline decoration-border underline-offset-[3px] tap-y outline-none hover:decoration-current focus-visible:ring-2 focus-visible:ring-ring/50"
              @click="emit('open', row.id, installedOffers(row)[0]!.line)"
            >
              {{ row.name }}
            </button>
            <span v-else class="truncate font-semibold">{{ row.name }}</span>
            <a
              v-if="homepage(row)"
              :href="homepage(row) ?? undefined"
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground outline-none tap-area hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
              :aria-label="t('software.homepageOf', { name: row.name })"
              :title="t('software.homepageOf', { name: row.name })"
            >
              <ExternalLink class="size-3.5" aria-hidden="true" />
            </a>
          </span>
          <span class="truncate text-xs text-muted-foreground">{{ categoryText(row.category) }}</span>
        </div>
      </div>
    </template>

    <template #cell-installed="{ row }">
      <div v-if="installedOffers(row).length > 0" class="flex flex-wrap gap-1.5">
        <button
          v-for="offer in installedOffers(row)"
          :key="offer.line"
          type="button"
          class="inline-flex items-center gap-1 rounded-full outline-none tap-area focus-visible:ring-2 focus-visible:ring-ring/50"
          :title="versionTitle(offer)"
          :aria-label="openLineLabel(row, offer)"
          @click="emit('open', row.id, offer.line)"
        >
          <StatusPill :tone="LINE_STATE_TONE[lineState(offer)]" dot class="font-mono hover:underline">{{ offer.line }}</StatusPill>
          <StatusPill v-if="isEndOfLifeInstalled(offer)" tone="warning">{{ t('software.endOfLife.badge') }}</StatusPill>
        </button>
      </div>
      <span v-else class="text-muted-foreground">—</span>
    </template>

    <template #cell-recommended="{ row }">
      <span v-if="recommended(row)" class="inline-flex items-center gap-1.5">
        <span class="font-mono text-[12.5px]" dir="ltr">{{ recommended(row)!.line }}</span>
        <StatusPill v-if="recommended(row)!.lts">{{ t('software.lts') }}</StatusPill>
      </span>
      <span v-else class="text-muted-foreground">—</span>
    </template>

    <template #cell-source="{ row }">
      <span v-if="sourceOffer(row) && sourceText(sourceOffer(row)!, store.repos)" class="text-detail">
        {{ sourceText(sourceOffer(row)!, store.repos) }}
      </span>
      <span v-else class="text-muted-foreground">—</span>
    </template>

    <template #cell-status="{ row }">
      <StatusDot :tone="status(row).tone" :class="cn(status(row).tone === 'neutral' && 'text-muted-foreground')">
        {{ status(row).text }}
      </StatusDot>
    </template>

    <template #cell-actions="{ row }">
      <div class="flex justify-end gap-2">
        <Button
          v-if="canManage && installableOffers(row).length > 0"
          variant="outline"
          size="sm"
          class="max-md:h-11"
          :disabled="store.runningFor(row.id) !== null"
          :aria-label="t('software.installNamed', { name: row.name })"
          @click="emit('install', row.id)"
        >
          <Download aria-hidden="true" />
          {{ t('software.action.install') }}
        </Button>
        <Button
          v-if="installedOffers(row).length > 0"
          variant="outline"
          size="sm"
          class="max-md:h-11"
          :aria-label="t('software.settingsOf', { name: row.name })"
          @click="emit('open', row.id, installedOffers(row)[0]!.line)"
        >
          <Settings2 aria-hidden="true" />
          {{ t('software.action.settings') }}
        </Button>
      </div>
    </template>
  </DataTable>
</template>
