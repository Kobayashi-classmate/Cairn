<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Pause, Pencil, Play, Plus, Trash2 } from '@lucide/vue'
import type { StreamView } from '@/api/streams'
import DataTable from '@/components/common/DataTable.vue'
import RowActions from '@/components/common/RowActions.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { errorText } from '@/i18n/errors'
import type { Tone } from '@/lib/tone'
import { askDeleteStream, toggleStream } from './actions'

// The four-layer (TCP/UDP) proxies of the sites page.
const props = defineProps<{
  streams: readonly StreamView[]
  loading: boolean
  error: unknown
  canManage: boolean
}>()

const emit = defineEmits<{ edit: [stream: StreamView]; create: []; retry: [] }>()

const { t } = useI18n()
const search = ref('')

type StreamState = 'active' | 'stopped' | 'applying' | 'failed'
const TONE: Record<StreamState, Tone> = { active: 'success', stopped: 'neutral', applying: 'progress', failed: 'destructive' }

function stateOf(stream: StreamView): StreamState {
  if (stream.state === 'applying' || stream.state === 'failed') return stream.state
  return stream.enabled ? 'active' : 'stopped'
}

const columns = computed<ColumnDef<StreamView>[]>(() => [
  { id: 'port', header: t('common.field.port'), accessorFn: (s) => s.port, meta: { class: 'w-[120px]' } },
  { id: 'protocol', header: t('common.field.protocol'), accessorFn: (s) => s.protocol, meta: { class: 'w-[100px]' } },
  { id: 'upstreams', header: t('common.field.upstreams'), enableSorting: false, meta: { class: 'min-w-[220px]' } },
  { id: 'balance', header: t('common.field.balance'), enableSorting: false, meta: { class: 'w-[150px]' } },
  { id: 'state', header: t('sites.list.state'), accessorFn: (s) => stateOf(s), meta: { class: 'w-[130px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[64px] text-end' } },
])

function searchText(stream: StreamView): string {
  return [stream.code, stream.protocol, String(stream.port), ...stream.upstreams].join(' ')
}

function actions(stream: StreamView): RowAction[] {
  if (!props.canManage) return []
  const busy = stream.state === 'applying'
  return [
    { key: 'edit', label: t('common.action.edit'), icon: Pencil, disabled: busy, onSelect: () => emit('edit', stream) },
    {
      key: 'toggle',
      label: stream.enabled ? t('sites.action.stop') : t('sites.action.start'),
      icon: stream.enabled ? Pause : Play,
      disabled: busy,
      onSelect: () => void toggleStream(stream),
    },
    {
      key: 'delete',
      label: t('common.action.delete'),
      icon: Trash2,
      destructive: true,
      disabled: busy,
      onSelect: () => void askDeleteStream(stream),
    },
  ]
}
</script>

<template>
  <DataTable
    v-model:search="search"
    :data="streams"
    :columns="columns"
    :row-key="(stream) => stream.code"
    :label="t('sites.pageTabs.stream')"
    :loading="loading"
    :error="error"
    :search-placeholder="t('streams.search')"
    :search-text="searchText"
    :empty-title="t('streams.empty')"
    :empty-text="t('streams.intro')"
    :min-width="760"
    :initial-sort="[{ id: 'port', desc: false }]"
    @retry="emit('retry')"
  >
    <template #cell-port="{ row }">
      <div class="flex flex-col">
        <span class="font-mono font-semibold">{{ row.port }}</span>
        <span class="font-mono text-xs text-muted-foreground">{{ row.code }}</span>
      </div>
    </template>
    <template #cell-protocol="{ row }">
      <span class="text-muted-foreground">{{ row.protocol.toUpperCase() }}</span>
    </template>
    <template #cell-upstreams="{ row }">
      <div class="flex flex-col font-mono text-[12.5px]" dir="ltr">
        <span v-for="upstream in row.upstreams.slice(0, 3)" :key="upstream" class="truncate">{{ upstream }}</span>
        <span v-if="row.upstreams.length > 3" class="text-xs text-muted-foreground">
          {{ t('sites.list.moreDomains', { count: row.upstreams.length - 3 }) }}
        </span>
      </div>
    </template>
    <template #cell-balance="{ row }">
      {{ row.balance === 'hash' ? t('streams.balance.hash') : t('streams.balance.roundRobin') }}
    </template>
    <template #cell-state="{ row }">
      <StatusDot :tone="TONE[stateOf(row)]" :title="row.errorCode ? (errorText(row.errorCode) ?? row.errorCode) : undefined">
        {{ t(`sites.state.${stateOf(row)}`) }}
      </StatusDot>
    </template>
    <template #cell-actions="{ row }">
      <div class="flex justify-end">
        <RowActions :actions="actions(row)" :label="t('streams.more', { code: row.code })" />
      </div>
    </template>
    <template v-if="canManage" #empty>
      <Button @click="emit('create')">
        <Plus aria-hidden="true" />
        {{ t('streams.dialog.createTitle') }}
      </Button>
    </template>
  </DataTable>
</template>
