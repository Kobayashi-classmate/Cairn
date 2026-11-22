<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { UserPlus } from '@lucide/vue'
import type { ManagedUser } from '@/api/users'
import PageHeader from '@/components/common/PageHeader.vue'
import { Button } from '@/components/ui/button'
import { useUsersStore } from '@/stores/users'
import UserCreateSheet from './users/UserCreateSheet.vue'
import UserPasswordSheet from './users/UserPasswordSheet.vue'
import UserRoleSheet from './users/UserRoleSheet.vue'
import UserTable from './users/UserTable.vue'

// Panel accounts ("list" skeleton): a table with the account actions in each
// row's menu; adding, changing the role and setting a password use form
// sheets. Every change asks to re-authenticate.
const { t } = useI18n()
const store = useUsersStore()

const showCreate = ref(false)
const showRole = ref(false)
const showPassword = ref(false)
const selected = ref<ManagedUser | null>(null)

onMounted(() => void store.load())

// The account shown in a sheet follows the list (a role just changed).
watch(
  () => store.users,
  (users) => {
    if (selected.value) selected.value = users?.find((u) => u.id === selected.value?.id) ?? selected.value
  },
)

const status = computed(() => {
  const users = store.users
  if (!users) return ''
  const parts = [t('users.status.count', { count: users.length }, users.length)]
  const disabled = users.filter((u) => u.disabled).length
  if (disabled > 0) parts.push(t('users.status.disabledCount', { count: disabled }, disabled))
  const noTotp = users.filter((u) => !u.disabled && !u.totpEnabled).length
  if (noTotp > 0) parts.push(t('users.status.noTotp', { count: noTotp }, noTotp))
  return parts.join(' · ')
})

function openRole(user: ManagedUser) {
  selected.value = user
  showRole.value = true
}

function openPassword(user: ManagedUser) {
  selected.value = user
  showPassword.value = true
}
</script>

<template>
  <div>
    <PageHeader :title="t('users.title')" :status="status" :status-loading="!store.users && !store.loadError">
      <template #actions>
        <Button class="max-md:h-11" @click="showCreate = true">
          <UserPlus aria-hidden="true" />
          {{ t('users.create.title') }}
        </Button>
      </template>
    </PageHeader>

    <UserTable
      :users="store.users ?? []"
      :loading="store.loading && !store.users"
      :error="store.loadError"
      @create="showCreate = true"
      @role="openRole"
      @password="openPassword"
      @retry="store.load()"
    />

    <UserCreateSheet v-model:open="showCreate" />
    <UserRoleSheet v-model:open="showRole" :user="selected" />
    <UserPasswordSheet v-model:open="showPassword" :user="selected" />
  </div>
</template>
