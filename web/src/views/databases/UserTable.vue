<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { KeyRound, Pencil, Plus, Trash2 } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import { revealDbPassword, type DbEngine, type DbUser } from '@/api/databases'
import DataTable from '@/components/common/DataTable.vue'
import RowActions from '@/components/common/RowActions.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { notifyError } from '@/lib/notify'
import { useSessionStore } from '@/stores/session'
import { askDropUser } from './actions'
import PasswordDialog from './PasswordDialog.vue'

// The users of a MySQL/MariaDB or PostgreSQL server: where they may connect
// from and which databases they can use. Passwords the panel knows can be
// shown again after re-authenticating.
const props = defineProps<{ engine: DbEngine; canManage: boolean }>()
const emit = defineEmits<{ create: []; edit: [user: DbUser] }>()

const { t } = useI18n()
const session = useSessionStore()
/** A user the panel did not create is changed and deleted by administrators only. */
const canAdmin = computed(() => session.can('databases.admin'))
const search = ref('')
const revealed = ref<string | null>(null)
const revealedUser = ref('')
const revealing = ref<string | null>(null)

const columns = computed<ColumnDef<DbUser>[]>(() => [
  { id: 'name', header: t('common.field.name'), accessorFn: (user) => user.name, meta: { class: 'min-w-[180px]' } },
  { id: 'hosts', header: t('common.field.hosts'), enableSorting: false, meta: { class: 'min-w-[160px]' } },
  { id: 'grants', header: t('common.field.grants'), enableSorting: false, meta: { class: 'min-w-[200px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[180px] text-end' } },
])

async function reveal(user: DbUser) {
  revealing.value = user.name
  try {
    // Step-up protected: the client asks for the password and retries.
    revealed.value = await revealDbPassword(props.engine.id, user.name)
    revealedUser.value = user.name
  } catch (err) {
    if (!isStepUpRequired(err)) notifyError(err)
  } finally {
    revealing.value = null
  }
}

function actions(user: DbUser): RowAction[] {
  if (!props.canManage || user.missing || (!user.managed && !canAdmin.value)) return []
  // A user the panel did not create: its privileges are not the panel's to
  // edit (it does not know them), only its password.
  const edit: RowAction = user.managed
    ? { key: 'edit', label: t('common.action.edit'), icon: Pencil, onSelect: () => emit('edit', user) }
    : { key: 'password', label: t('databases.user.setPassword'), icon: KeyRound, onSelect: () => emit('edit', user) }
  return [
    edit,
    { key: 'delete', label: t('databases.user.drop'), icon: Trash2, destructive: true, onSelect: () => void askDropUser(props.engine, user) },
  ]
}

function hostText(host: string): string {
  return host === '%' ? t('databases.anyHost') : host
}
</script>

<template>
  <p v-if="engine.kind === 'postgresql'" class="m-0 text-detail text-muted-foreground">{{ t('databases.hints.pgHosts') }}</p>
  <DataTable
    v-model:search="search"
    :data="engine.users"
    :columns="columns"
    :row-key="(user) => user.name"
    :label="t('databases.tabs.users')"
    :search-placeholder="t('databases.user.search')"
    :search-text="(user) => [user.name, ...user.hosts, ...user.grants.map((g) => g.database)].join(' ')"
    :empty-title="t('databases.user.empty')"
    :min-width="780"
    :initial-sort="[{ id: 'name', desc: false }]"
  >
    <template v-if="canManage" #toolbar>
      <Button class="max-md:h-11" :disabled="!engine.running" @click="emit('create')">
        <Plus aria-hidden="true" />
        {{ t('databases.user.createTitle') }}
      </Button>
    </template>

    <template #cell-name="{ row }">
      <span class="flex flex-wrap items-center gap-1.5">
        <span class="font-mono text-[13px] font-medium" dir="ltr">{{ row.name }}</span>
        <StatusPill v-if="row.missing" tone="destructive">{{ t('databases.missing') }}</StatusPill>
        <StatusPill v-if="!row.managed">{{ t('databases.unmanagedTag') }}</StatusPill>
      </span>
    </template>
    <template #cell-hosts="{ row }">
      <span class="flex flex-wrap gap-1">
        <StatusPill v-for="host in row.hosts.length ? row.hosts : ['localhost']" :key="host" class="font-mono">{{ hostText(host) }}</StatusPill>
      </span>
    </template>
    <template #cell-grants="{ row }">
      <span v-if="row.grants.length > 0" class="flex flex-wrap gap-1">
        <StatusPill v-for="grant in row.grants" :key="grant.database" :tone="grant.level === 'all' ? 'progress' : 'neutral'">
          {{ t('databases.grant.tag', { database: grant.database, level: t(`databases.grant.${grant.level}`) }) }}
        </StatusPill>
      </span>
      <span v-else class="text-muted-foreground">—</span>
    </template>
    <template #cell-actions="{ row }">
      <div v-if="canManage && !row.missing" class="flex justify-end gap-2">
        <Button
          v-if="row.hasPassword"
          variant="outline"
          size="sm"
          class="max-md:h-11"
          :disabled="revealing !== null"
          @click="reveal(row)"
        >
          <KeyRound aria-hidden="true" />
          {{ t('databases.password.show') }}
        </Button>
        <RowActions v-if="actions(row).length > 0" :actions="actions(row)" :label="t('databases.user.more', { name: row.name })" />
      </div>
    </template>
  </DataTable>

  <PasswordDialog v-model:password="revealed" :title="t('databases.password.title', { name: revealedUser })" />
</template>
