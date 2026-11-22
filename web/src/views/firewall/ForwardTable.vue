<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Pencil, Plus, Trash2 } from '@lucide/vue'
import { addForward, deleteForward, type ChangeResult, type FirewallForward } from '@/api/firewall'
import DataTable from '@/components/common/DataTable.vue'
import RowActions from '@/components/common/RowActions.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import { confirmAction } from '@/composables/useConfirmDialog'
import type { ChangeInfo } from '@/composables/useFirewallConfirm'
import { describeForward, forwardRequest, forwardTarget, protocolLabel } from './labels'

// Port forwards: the port people connect to and where the connection goes
// on, on this host or another one.
const props = defineProps<{
  forwards: readonly FirewallForward[]
  loading: boolean
  error: unknown
  canManage: boolean
  writable: boolean
  /** Another forward may be added (below the server's limit). */
  canAdd: boolean
}>()

const emit = defineEmits<{
  create: []
  edit: [forward: FirewallForward]
  changed: [result: ChangeResult, info: ChangeInfo]
  retry: []
}>()

const { t } = useI18n()
const search = ref('')

const columns = computed<ColumnDef<FirewallForward>[]>(() => [
  { id: 'protocol', header: t('common.field.protocol'), accessorFn: (forward) => forward.protocol, meta: { class: 'w-[130px]' } },
  { id: 'port', header: t('firewall.forward.port'), accessorFn: (forward) => forward.port, meta: { class: 'w-[140px]' } },
  { id: 'target', header: t('firewall.forward.target'), accessorFn: (forward) => forward.toAddress ?? '', meta: { class: 'min-w-[220px]' } },
  { id: 'note', header: t('common.field.note'), accessorFn: (forward) => (forward.note ?? '').toLowerCase(), meta: { class: 'min-w-[160px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[100px] text-end' } },
])

function searchText(forward: FirewallForward): string {
  return [protocolLabel(forward.protocol), String(forward.port), forwardTarget(forward), forward.note ?? ''].join(' ')
}

function askDelete(forward: FirewallForward) {
  void confirmAction({
    title: t('firewall.forward.deleteTitle', { port: forward.port }),
    description: t('firewall.forward.deleteText', { forward: describeForward(forward) }),
    confirmLabel: t('firewall.forward.delete'),
    destructive: true,
    run: async () => {
      // Step-up protected: the client asks for the password and retries.
      const result = await deleteForward(forward.id)
      const previous = forwardRequest(forward)
      emit('changed', result, {
        summary: t('firewall.summary.forwardDelete', { forward: describeForward(forward) }),
        undo: () => addForward(previous),
      })
    },
  })
}
</script>

<template>
  <DataTable
    v-model:search="search"
    :data="forwards"
    :columns="columns"
    :row-key="(forward) => forward.id"
    :label="t('firewall.tabs.forwards')"
    :loading="loading"
    :error="error"
    :search-placeholder="t('firewall.forward.search')"
    :search-text="searchText"
    :empty-title="t('firewall.forward.empty')"
    :empty-text="t('firewall.forward.emptyText')"
    :min-width="720"
    :initial-sort="[{ id: 'port', desc: false }]"
    @retry="emit('retry')"
  >
    <template #cell-protocol="{ row }">{{ protocolLabel(row.protocol) }}</template>
    <template #cell-port="{ row }">
      <span class="font-mono text-[12.5px]">{{ row.port }}</span>
    </template>
    <template #cell-target="{ row }">
      <span v-if="row.toAddress" class="font-mono text-[12.5px]" dir="ltr">{{ row.toAddress }}:{{ row.toPort }}</span>
      <span v-else class="inline-flex flex-wrap items-baseline gap-x-1.5">
        <span>{{ t('firewall.forward.thisHost') }}</span>
        <span class="font-mono text-[12.5px]">:{{ row.toPort }}</span>
      </span>
    </template>
    <template #cell-note="{ row }">
      <span class="[overflow-wrap:anywhere]">{{ row.note }}</span>
    </template>
    <template #cell-actions="{ row }">
      <div v-if="canManage" class="flex justify-end gap-2">
        <Button
          variant="outline"
          size="icon"
          class="max-md:size-11"
          :disabled="!writable"
          :aria-label="t('firewall.forward.edit', { port: row.port })"
          :title="t('firewall.forward.edit', { port: row.port })"
          @click="emit('edit', row)"
        >
          <Pencil aria-hidden="true" />
        </Button>
        <RowActions
          :actions="[{ key: 'delete', label: t('firewall.forward.delete'), icon: Trash2, destructive: true, disabled: !props.writable, onSelect: () => askDelete(row) }]"
          :label="t('firewall.forward.more', { port: row.port })"
        />
      </div>
    </template>
    <template v-if="canManage && writable && canAdd" #empty>
      <Button @click="emit('create')">
        <Plus aria-hidden="true" />
        {{ t('firewall.forward.add') }}
      </Button>
    </template>
  </DataTable>
</template>
