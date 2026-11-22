<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2 } from '@lucide/vue'
import { addAddress, deleteAddress, type ChangeResult, type FirewallAddress } from '@/api/firewall'
import DataTable from '@/components/common/DataTable.vue'
import RowActions from '@/components/common/RowActions.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import { confirmAction } from '@/composables/useConfirmDialog'
import type { ChangeInfo } from '@/composables/useFirewallConfirm'

// Trusted and blocked addresses: they apply to every port, SSH included.
const props = defineProps<{
  addresses: readonly FirewallAddress[]
  loading: boolean
  error: unknown
  canManage: boolean
  writable: boolean
}>()

const emit = defineEmits<{ create: []; changed: [result: ChangeResult, info: ChangeInfo]; retry: [] }>()

const { t } = useI18n()
const search = ref('')

const columns = computed<ColumnDef<FirewallAddress>[]>(() => [
  { id: 'address', header: t('common.field.address'), accessorFn: (entry) => entry.address, meta: { class: 'min-w-[220px]' } },
  { id: 'action', header: t('common.field.action'), accessorFn: (entry) => entry.action, meta: { class: 'w-[140px]' } },
  { id: 'note', header: t('common.field.note'), accessorFn: (entry) => entry.note.toLowerCase(), meta: { class: 'min-w-[180px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[70px] text-end' } },
])

function actionLabel(entry: FirewallAddress): string {
  return entry.action === 'allow' ? t('firewall.address.allow') : t('firewall.address.deny')
}

function askDelete(entry: FirewallAddress) {
  void confirmAction({
    title: t('firewall.address.deleteTitle', { address: entry.address }),
    description: entry.action === 'allow' ? t('firewall.address.deleteAllowText') : t('firewall.address.deleteDenyText'),
    confirmLabel: t('firewall.address.delete'),
    destructive: true,
    run: async () => {
      // Step-up protected: the client asks for the password and retries.
      const result = await deleteAddress(entry.id)
      const previous = { address: entry.address, action: entry.action, note: entry.note }
      emit('changed', result, {
        summary: t('firewall.summary.address.delete', { address: entry.address }),
        undo: () => addAddress(previous),
      })
    },
  })
}
</script>

<template>
  <DataTable
    v-model:search="search"
    :data="addresses"
    :columns="columns"
    :row-key="(entry) => entry.id"
    :label="t('firewall.tabs.addresses')"
    :loading="loading"
    :error="error"
    :search-placeholder="t('firewall.address.search')"
    :search-text="(entry) => [entry.address, actionLabel(entry), entry.note].join(' ')"
    :empty-title="t('firewall.address.empty')"
    :empty-text="t('firewall.address.emptyText')"
    :min-width="620"
    @retry="emit('retry')"
  >
    <template #cell-address="{ row }">
      <span class="font-mono text-[12.5px]" dir="ltr">{{ row.address }}</span>
    </template>
    <template #cell-action="{ row }">
      <StatusPill :tone="row.action === 'allow' ? 'success' : 'destructive'">{{ actionLabel(row) }}</StatusPill>
    </template>
    <template #cell-note="{ row }">
      <span class="[overflow-wrap:anywhere]">{{ row.note }}</span>
    </template>
    <template #cell-actions="{ row }">
      <div v-if="canManage" class="flex justify-end">
        <RowActions
          :actions="[{ key: 'delete', label: t('firewall.address.delete'), icon: Trash2, destructive: true, disabled: !props.writable, onSelect: () => askDelete(row) }]"
          :label="t('firewall.address.more', { address: row.address })"
        />
      </div>
    </template>
    <template v-if="canManage && writable" #empty>
      <Button @click="emit('create')">
        <Plus aria-hidden="true" />
        {{ t('firewall.address.add') }}
      </Button>
    </template>
  </DataTable>
</template>
