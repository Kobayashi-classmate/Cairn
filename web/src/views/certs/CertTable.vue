<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { MonitorCog, Plus, RefreshCw, Settings2, Trash2 } from '@lucide/vue'
import type { CertView } from '@/api/certs'
import DataTable from '@/components/common/DataTable.vue'
import RowActions from '@/components/common/RowActions.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { caLabel, certDisplayState } from '@/certs/labels'
import { useClock } from '@/composables/useClock'
import { useNavRoutes } from '@/composables/useNavRoutes'
import { errorText } from '@/i18n/errors'
import { expiryStatus } from '@/sites/display'
import { CERT_STATE_TONE, askDeleteCert, askPanelCert, canServePanel, renew, renewing } from './actions'

// The certificates: name (opens the drawer), domains, source, state,
// expiry, auto-renew and what uses it.
const props = defineProps<{
  certs: readonly CertView[]
  loading: boolean
  error: unknown
  canManage: boolean
}>()

const emit = defineEmits<{ open: [id: string]; issue: []; retry: [] }>()

const { t } = useI18n()
const now = useClock()
const navRoutes = useNavRoutes()
const search = ref('')

const columns = computed<ColumnDef<CertView>[]>(() => [
  { id: 'name', header: t('common.field.name'), accessorFn: (c) => c.name, meta: { class: 'min-w-[180px]' } },
  { id: 'domains', header: t('common.field.domains'), accessorFn: (c) => c.domains[0] ?? '', meta: { class: 'min-w-[200px]' } },
  { id: 'source', header: t('certs.column.source'), accessorFn: (c) => sourceLabel(c), meta: { class: 'w-[150px]' } },
  { id: 'state', header: t('certs.column.state'), accessorFn: (c) => certDisplayState(c, now.value), meta: { class: 'w-[140px]' } },
  { id: 'expires', header: t('certs.column.expires'), accessorFn: (c) => c.notAfter ?? 0, meta: { class: 'w-[140px]' } },
  { id: 'autoRenew', header: t('certs.column.autoRenew'), enableSorting: false, meta: { class: 'w-[110px]' } },
  { id: 'usedBy', header: t('certs.column.usedBy'), enableSorting: false, meta: { class: 'min-w-[150px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[64px] text-end' } },
])

function sourceLabel(cert: CertView): string {
  return cert.source === 'upload' ? t('certs.source.upload') : caLabel(cert.ca ?? '')
}

function searchText(cert: CertView): string {
  return [cert.name, ...cert.domains, sourceLabel(cert), ...cert.sites].join(' ')
}

function failure(cert: CertView): string | undefined {
  return cert.errorCode ? (errorText(cert.errorCode, cert.errorParams ?? {}) ?? cert.errorCode) : undefined
}

function actions(cert: CertView): RowAction[] {
  const list: RowAction[] = [
    { key: 'open', label: t('certs.action.settings'), icon: Settings2, onSelect: () => emit('open', cert.id) },
  ]
  if (!props.canManage) return list
  if (cert.source === 'acme') {
    list.push({
      key: 'renew',
      label: t('certs.detail.renew'),
      icon: RefreshCw,
      disabled: cert.state === 'issuing' || renewing(cert.id),
      onSelect: () => void renew(cert),
    })
  }
  if (!cert.panel && canServePanel(cert, now.value)) {
    list.push({ key: 'panel', label: t('certs.action.usePanel'), icon: MonitorCog, onSelect: () => void askPanelCert(cert) })
  }
  list.push({
    key: 'delete',
    label: t('certs.action.delete'),
    icon: Trash2,
    destructive: true,
    disabled: cert.sites.length > 0 || cert.panel,
    onSelect: () => void askDeleteCert(cert),
  })
  return list
}
</script>

<template>
  <DataTable
    v-model:search="search"
    :data="certs"
    :columns="columns"
    :row-key="(cert) => cert.id"
    :label="t('certs.tabs.certs')"
    :loading="loading"
    :error="error"
    :search-placeholder="t('certs.list.search')"
    :search-text="searchText"
    :empty-title="t('certs.empty.title')"
    :empty-text="t('certs.empty.text')"
    :min-width="1020"
    :initial-sort="[{ id: 'expires', desc: false }]"
    @retry="emit('retry')"
  >
    <template #cell-name="{ row }">
      <div class="flex min-w-0 flex-col">
        <button
          type="button"
          class="self-start truncate rounded-sm text-start font-semibold text-foreground underline decoration-border underline-offset-[3px] tap-y outline-none hover:decoration-current focus-visible:ring-2 focus-visible:ring-ring/50"
          @click="emit('open', row.id)"
        >
          {{ row.name }}
        </button>
      </div>
    </template>
    <template #cell-domains="{ row }">
      <div class="flex min-w-0 flex-col font-mono text-[12.5px]" dir="ltr">
        <span class="truncate">{{ row.domains[0] }}</span>
        <span v-if="row.domains.length > 1" class="font-sans text-xs text-muted-foreground">
          {{ t('sites.list.moreDomains', { count: row.domains.length - 1 }) }}
        </span>
      </div>
    </template>
    <template #cell-source="{ row }">
      <div class="flex flex-col">
        <span>{{ sourceLabel(row) }}</span>
        <span v-if="row.challenge" class="text-xs text-muted-foreground">
          {{ row.challenge === 'dns-01' ? 'DNS-01' : 'HTTP-01' }}
        </span>
      </div>
    </template>
    <template #cell-state="{ row }">
      <StatusDot :tone="CERT_STATE_TONE[certDisplayState(row, now)]" :title="failure(row)">
        {{ t(`certs.state.${certDisplayState(row, now)}`) }}
      </StatusDot>
    </template>
    <template #cell-expires="{ row }">
      <StatusPill v-if="row.notAfter && row.state !== 'issuing'" :tone="expiryStatus(row.notAfter, now).tone">
        {{ expiryStatus(row.notAfter, now).text }}
      </StatusPill>
      <span v-else class="text-muted-foreground">—</span>
    </template>
    <template #cell-autoRenew="{ row }">
      <span v-if="row.source !== 'acme'" class="text-muted-foreground">—</span>
      <span v-else :class="row.autoRenew ? '' : 'text-muted-foreground'">
        {{ row.autoRenew ? t('common.status.on') : t('common.status.off') }}
      </span>
    </template>
    <template #cell-usedBy="{ row }">
      <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
        <template v-for="code in row.sites" :key="code">
          <RouterLink
            v-if="navRoutes.has('sites')"
            :to="{ name: 'sites', query: { open: `site:${code}` } }"
            class="font-mono text-[12.5px] text-link underline-offset-2 hover:underline"
          >
            {{ code }}
          </RouterLink>
          <span v-else class="font-mono text-[12.5px]">{{ code }}</span>
        </template>
        <StatusPill v-if="row.panel" tone="progress">{{ t('certs.panelTag') }}</StatusPill>
        <span v-if="row.sites.length === 0 && !row.panel" class="text-muted-foreground">—</span>
      </div>
    </template>
    <template #cell-actions="{ row }">
      <div class="flex justify-end">
        <RowActions :actions="actions(row)" :label="t('certs.list.more', { name: row.name })" />
      </div>
    </template>
    <template v-if="canManage" #empty>
      <Button @click="emit('issue')">
        <Plus aria-hidden="true" />
        {{ t('certs.issue.title') }}
      </Button>
    </template>
  </DataTable>
</template>
