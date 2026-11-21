<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { FlaskConical, Pencil, Plus, Trash2 } from '@lucide/vue'
import { deleteDnsAccount, type DnsAccount } from '@/api/dns'
import DataTable from '@/components/common/DataTable.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import RowActions from '@/components/common/RowActions.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { confirmAction } from '@/composables/useConfirmDialog'
import { notifySuccess } from '@/lib/notify'
import { useCertsStore } from '@/stores/certs'
import { useSessionStore } from '@/stores/session'
import DnsAccountSheet from './DnsAccountSheet.vue'
import DnsTestDialog from './DnsTestDialog.vue'

// Accounts at DNS providers, used for DNS-01 validation.
const { t } = useI18n()
const store = useCertsStore()
const session = useSessionStore()

const search = ref('')
const showSheet = ref(false)
const editing = ref<DnsAccount | null>(null)
const testing = ref<DnsAccount | null>(null)

const canManage = computed(() => session.can('certs.manage'))

onMounted(() => {
  if (!store.accounts) void store.loadAccounts()
})

/** Opens the form for a new account (also from the issue sheet and the page header). */
function openCreate() {
  editing.value = null
  showSheet.value = true
}

defineExpose({ openCreate })

function providerName(id: string): string {
  return store.providers.find((p) => p.id === id)?.name ?? id
}

const columns = computed<ColumnDef<DnsAccount>[]>(() => [
  { id: 'name', header: t('common.field.name'), accessorFn: (a) => a.name, meta: { class: 'min-w-[180px]' } },
  { id: 'provider', header: t('dns.provider'), accessorFn: (a) => providerName(a.provider), meta: { class: 'min-w-[160px]' } },
  { id: 'usedBy', header: t('certs.column.usedBy'), accessorFn: (a) => a.usedBy.length, meta: { class: 'w-[150px]' } },
  { id: 'updated', header: t('sites.overview.updated'), accessorFn: (a) => a.updatedAt, meta: { class: 'w-[160px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[64px] text-end' } },
])

function remove(account: DnsAccount) {
  void confirmAction({
    title: t('dns.delete.title', { name: account.name }),
    description: t('dns.delete.content'),
    confirmLabel: t('dns.delete.confirm'),
    destructive: true,
    run: async () => {
      // Step-up protected: the client asks for the password and retries.
      await deleteDnsAccount(account.id)
      notifySuccess(t('dns.delete.done', { name: account.name }))
      void store.loadAccounts()
    },
  })
}

function actions(account: DnsAccount): RowAction[] {
  if (!canManage.value) return []
  return [
    { key: 'test', label: t('dns.test.button'), icon: FlaskConical, onSelect: () => (testing.value = account) },
    {
      key: 'edit',
      label: t('common.action.edit'),
      icon: Pencil,
      onSelect: () => {
        editing.value = account
        showSheet.value = true
      },
    },
    {
      key: 'delete',
      label: t('common.action.delete'),
      icon: Trash2,
      destructive: true,
      disabled: account.usedBy.length > 0,
      onSelect: () => remove(account),
    },
  ]
}
</script>

<template>
  <div class="flex flex-col gap-4">
    <p class="m-0 text-muted-foreground">{{ t('dns.intro') }}</p>
    <DataTable
      v-model:search="search"
      :data="store.accounts ?? []"
      :columns="columns"
      :row-key="(account) => account.id"
      :label="t('certs.tabs.dns')"
      :loading="store.accountsLoading || !store.accounts"
      :error="store.accountsError"
      :search-placeholder="t('dns.search')"
      :search-text="(a) => `${a.name} ${providerName(a.provider)}`"
      :empty-title="t('dns.empty')"
      :empty-text="t('dns.emptyText')"
      :min-width="680"
      @retry="store.loadAccounts()"
    >
      <template #cell-name="{ row }">
        <span class="font-medium">{{ row.name }}</span>
      </template>
      <template #cell-provider="{ row }">{{ providerName(row.provider) }}</template>
      <template #cell-usedBy="{ row }">
        <span :class="row.usedBy.length === 0 ? 'text-muted-foreground' : ''">
          {{ t('dns.usedBy', { count: row.usedBy.length }, row.usedBy.length) }}
        </span>
      </template>
      <template #cell-updated="{ row }">
        <RelativeTime :value="row.updatedAt" class="text-muted-foreground" />
      </template>
      <template #cell-actions="{ row }">
        <div class="flex justify-end">
          <RowActions :actions="actions(row)" :label="t('dns.more', { name: row.name })" />
        </div>
      </template>
      <template v-if="canManage" #empty>
        <Button @click="openCreate">
          <Plus aria-hidden="true" />
          {{ t('dns.dialog.createTitle') }}
        </Button>
      </template>
    </DataTable>

    <DnsAccountSheet v-model:open="showSheet" :providers="store.providers" :account="editing" @saved="store.loadAccounts()" />
    <DnsTestDialog :account="testing" @close="testing = null" />
  </div>
</template>
