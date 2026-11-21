<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ExternalLink, Pause, Play, Plus, Settings2, Trash2 } from '@lucide/vue'
import type { SiteView } from '@/api/sites'
import DataTable from '@/components/common/DataTable.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import RowActions from '@/components/common/RowActions.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useClock } from '@/composables/useClock'
import { errorText } from '@/i18n/errors'
import { TONE_TEXT } from '@/lib/tone'
import { siteDisplayState } from '@/sites/labels'
import {
  SITE_STATE_TONE,
  backupStatus,
  httpsStatus,
  siteDetail,
  siteStateText,
  siteTypeText,
  siteUrl,
} from '@/sites/display'
import { askDeleteSite, toggleSite } from './actions'

// The sites list: domain (opens the settings drawer), type, state, HTTPS,
// last backup and a "more" menu.
const props = defineProps<{
  sites: readonly SiteView[]
  loading: boolean
  error: unknown
  /** The list reports backups (the backups module is on). */
  backupsKnown: boolean
  canManage: boolean
  /** Creating is possible (nginx installed, permission). */
  canCreate: boolean
  emptyTitle: string
  emptyText?: string
}>()

const emit = defineEmits<{ open: [code: string, section?: string]; create: []; retry: [] }>()

const { t } = useI18n()
const now = useClock()
const search = ref('')

const columns = computed<ColumnDef<SiteView>[]>(() => [
  {
    id: 'domain',
    header: t('sites.list.domain'),
    accessorFn: (site) => site.domains[0] ?? site.code,
    meta: { class: 'min-w-[220px]' },
  },
  { id: 'type', header: t('common.field.type'), accessorFn: (site) => siteTypeText(site), meta: { class: 'w-[170px]' } },
  { id: 'state', header: t('sites.list.state'), accessorFn: (site) => siteDisplayState(site), meta: { class: 'w-[130px]' } },
  { id: 'https', header: t('sites.list.https'), enableSorting: false, meta: { class: 'w-[170px]' } },
  {
    id: 'backup',
    header: t('sites.list.backup'),
    accessorFn: (site) => site.backup?.lastOkAt ?? 0,
    enableSorting: props.backupsKnown,
    meta: { class: 'w-[150px]' },
  },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[64px] text-end' } },
])

function searchText(site: SiteView): string {
  return [site.code, ...site.domains, siteTypeText(site), siteDetail(site)].join(' ')
}

function actions(site: SiteView): RowAction[] {
  const url = siteUrl(site, site.domains[0])
  const list: RowAction[] = []
  if (url) list.push({ key: 'visit', label: t('sites.action.visit'), icon: ExternalLink, href: url })
  list.push({ key: 'settings', label: t('sites.action.settings'), icon: Settings2, onSelect: () => emit('open', site.code) })
  if (props.canManage) {
    list.push({
      key: 'toggle',
      label: site.enabled ? t('sites.action.stop') : t('sites.action.start'),
      icon: site.enabled ? Pause : Play,
      disabled: site.state === 'applying',
      onSelect: () => void toggleSite(site),
    })
    list.push({
      key: 'delete',
      label: t('sites.action.delete'),
      icon: Trash2,
      destructive: true,
      disabled: site.state === 'applying',
      onSelect: () => void askDeleteSite(site),
    })
  }
  return list
}

/** The backup column of every row, with the text colour of its tone. */
const backups = computed(() => {
  const cells = new Map<string, { text: string; at?: number; class: string }>()
  for (const site of props.sites) {
    const status = backupStatus(site.backup, props.backupsKnown)
    if (status) cells.set(site.code, { ...status, class: status.tone === 'neutral' ? 'text-foreground' : TONE_TEXT[status.tone] })
  }
  return cells
})

function failure(site: SiteView): string {
  return site.errorCode ? (errorText(site.errorCode) ?? site.errorCode) : ''
}
</script>

<template>
  <DataTable
    v-model:search="search"
    :data="sites"
    :columns="columns"
    :row-key="(site) => site.code"
    :label="t('sites.title')"
    :loading="loading"
    :error="error"
    :search-placeholder="t('sites.list.search')"
    :search-text="searchText"
    :empty-title="emptyTitle"
    :empty-text="emptyText"
    :min-width="900"
    @retry="emit('retry')"
  >
    <template #toolbar>
      <span class="text-detail text-muted-foreground max-sm:hidden">{{ t('sites.list.hint') }}</span>
    </template>

    <template #cell-domain="{ row }">
      <div class="flex min-w-0 flex-col">
        <button
          type="button"
          class="self-start truncate rounded-sm text-start font-semibold text-foreground underline decoration-border underline-offset-[3px] outline-none hover:decoration-current focus-visible:ring-2 focus-visible:ring-ring/50"
          dir="ltr"
          @click="emit('open', row.code)"
        >
          {{ row.domains[0] ?? row.code }}
        </button>
        <span class="truncate text-xs text-muted-foreground" dir="ltr" :title="siteDetail(row)">{{ siteDetail(row) }}</span>
      </div>
    </template>

    <template #cell-type="{ row }">
      <span class="whitespace-nowrap">{{ siteTypeText(row) }}</span>
    </template>

    <template #cell-state="{ row }">
      <div class="flex flex-col gap-0.5">
        <Tooltip v-if="siteDisplayState(row) === 'failed' && failure(row)">
          <TooltipTrigger as-child>
            <button type="button" class="self-start rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
              <StatusDot :tone="SITE_STATE_TONE[siteDisplayState(row)]">{{ siteStateText(row) }}</StatusDot>
            </button>
          </TooltipTrigger>
          <TooltipContent>{{ failure(row) }}</TooltipContent>
        </Tooltip>
        <StatusDot v-else :tone="SITE_STATE_TONE[siteDisplayState(row)]">{{ siteStateText(row) }}</StatusDot>
        <span v-if="row.drift && siteDisplayState(row) !== 'failed'" class="text-xs text-warning">{{ t('sites.drift.tag') }}</span>
      </div>
    </template>

    <template #cell-https="{ row }">
      <StatusPill :tone="httpsStatus(row, now).tone">{{ httpsStatus(row, now).text }}</StatusPill>
    </template>

    <template #cell-backup="{ row }">
      <span v-if="!backups.get(row.code)" class="text-muted-foreground">—</span>
      <span v-else :class="['flex flex-col', backups.get(row.code)!.class]">
        <span v-if="backups.get(row.code)!.text" class="whitespace-nowrap">{{ backups.get(row.code)!.text }}</span>
        <RelativeTime
          v-if="backups.get(row.code)!.at"
          :value="backups.get(row.code)!.at!"
          :class="backups.get(row.code)!.text ? 'text-xs' : ''"
        />
      </span>
    </template>

    <template #cell-actions="{ row }">
      <div class="flex justify-end">
        <RowActions :actions="actions(row)" :label="t('sites.list.more', { site: row.domains[0] ?? row.code })" />
      </div>
    </template>

    <template v-if="canCreate" #empty>
      <Button @click="emit('create')">
        <Plus aria-hidden="true" />
        {{ t('sites.create.title') }}
      </Button>
    </template>
  </DataTable>
</template>
