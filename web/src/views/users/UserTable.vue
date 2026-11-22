<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { Ban, KeyRound, Plus, ShieldOff, Trash2, UserCheck, UserCog } from '@lucide/vue'
import type { ManagedUser } from '@/api/users'
import DataTable from '@/components/common/DataTable.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import RowActions from '@/components/common/RowActions.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { roleLabel } from '@/auth/permissions'
import type { Tone } from '@/lib/tone'
import { useSessionStore } from '@/stores/session'
import { askDelete, askDisable, askResetTotp, enable } from './actions'

// The panel accounts: name, role, two-step verification, state, last
// sign-in and creation, with the account actions in a "more" menu. Your
// own account is changed in your settings, not here.
defineProps<{
  users: readonly ManagedUser[]
  loading: boolean
  error: unknown
}>()

const emit = defineEmits<{ create: []; role: [user: ManagedUser]; password: [user: ManagedUser]; retry: [] }>()

const { t } = useI18n()
const session = useSessionStore()
const search = ref('')

const ROLE_TONE: Record<string, Tone> = { admin: 'progress', operator: 'neutral', auditor: 'neutral' }

function isSelf(user: ManagedUser): boolean {
  return user.id === session.user?.id
}

const columns = computed<ColumnDef<ManagedUser>[]>(() => [
  { id: 'username', header: t('common.field.username'), accessorFn: (u) => u.username, meta: { class: 'min-w-[200px]' } },
  { id: 'role', header: t('common.field.role'), accessorFn: (u) => roleLabel(u.role), meta: { class: 'w-[130px]' } },
  { id: 'totp', header: t('users.column.totp'), accessorFn: (u) => Number(u.totpEnabled), meta: { class: 'w-[130px]' } },
  { id: 'state', header: t('users.column.status'), accessorFn: (u) => Number(u.disabled), meta: { class: 'w-[110px]' } },
  { id: 'lastLogin', header: t('users.column.lastLogin'), accessorFn: (u) => u.lastLoginAt, meta: { class: 'w-[150px]' } },
  { id: 'created', header: t('users.column.created'), accessorFn: (u) => u.createdAt, meta: { class: 'w-[150px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[150px] text-end' } },
])

function searchText(user: ManagedUser): string {
  return [user.username, roleLabel(user.role)].join(' ')
}

function actions(user: ManagedUser): RowAction[] {
  const list: RowAction[] = [
    { key: 'role', label: t('users.action.changeRole'), icon: UserCog, onSelect: () => emit('role', user) },
    { key: 'password', label: t('users.action.resetPassword'), icon: KeyRound, onSelect: () => emit('password', user) },
  ]
  if (user.totpEnabled) {
    list.push({ key: 'totp', label: t('users.action.resetTotp'), icon: ShieldOff, onSelect: () => void askResetTotp(user) })
  }
  list.push(
    user.disabled
      ? { key: 'enable', label: t('users.action.enable'), icon: UserCheck, onSelect: () => void enable(user) }
      : { key: 'disable', label: t('users.action.disable'), icon: Ban, destructive: true, onSelect: () => void askDisable(user) },
    { key: 'delete', label: t('users.action.delete'), icon: Trash2, destructive: true, onSelect: () => void askDelete(user) },
  )
  return list
}
</script>

<template>
  <DataTable
    v-model:search="search"
    :data="users"
    :columns="columns"
    :row-key="(u) => String(u.id)"
    :label="t('users.title')"
    :loading="loading"
    :error="error"
    :search-placeholder="t('users.search')"
    :search-text="searchText"
    :min-width="900"
    :initial-sort="[{ id: 'username', desc: false }]"
    @retry="emit('retry')"
  >
    <template #cell-username="{ row }">
      <span class="flex min-w-0 flex-wrap items-center gap-2">
        <span class="truncate font-semibold" dir="ltr">{{ row.username }}</span>
        <StatusPill v-if="isSelf(row)" tone="progress">{{ t('users.you') }}</StatusPill>
      </span>
    </template>
    <template #cell-role="{ row }">
      <StatusPill :tone="ROLE_TONE[row.role] ?? 'neutral'">{{ roleLabel(row.role) }}</StatusPill>
    </template>
    <template #cell-totp="{ row }">
      <StatusDot :tone="row.totpEnabled ? 'success' : 'warning'">
        {{ row.totpEnabled ? t('users.totp.on') : t('users.totp.off') }}
      </StatusDot>
    </template>
    <template #cell-state="{ row }">
      <StatusPill :tone="row.disabled ? 'neutral' : 'success'">
        {{ row.disabled ? t('users.status.disabled') : t('users.status.active') }}
      </StatusPill>
    </template>
    <template #cell-lastLogin="{ row }">
      <RelativeTime v-if="row.lastLoginAt > 0" :value="row.lastLoginAt" />
      <span v-else class="text-muted-foreground">{{ t('users.never') }}</span>
    </template>
    <template #cell-created="{ row }"><RelativeTime :value="row.createdAt" /></template>
    <template #cell-actions="{ row }">
      <div class="flex justify-end">
        <Button v-if="isSelf(row)" as-child variant="ghost" size="sm" class="max-md:h-11">
          <RouterLink :to="{ name: 'settings-account' }">{{ t('users.ownSettings') }}</RouterLink>
        </Button>
        <RowActions v-else :actions="actions(row)" :label="t('users.action.more', { username: row.username })" />
      </div>
    </template>
    <template #empty>
      <Button @click="emit('create')">
        <Plus aria-hidden="true" />
        {{ t('users.create.title') }}
      </Button>
    </template>
  </DataTable>
</template>
