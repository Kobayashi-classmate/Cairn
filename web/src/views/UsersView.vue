<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NDropdown,
  NH2,
  NIcon,
  NTag,
  useMessage,
  type DataTableColumns,
  type DropdownOption,
} from 'naive-ui'
import { AddOutline, EllipsisHorizontal } from '@vicons/ionicons5'
import {
  deleteUser,
  fetchUsers,
  resetUserTotp,
  updateUser,
  type ManagedUser,
} from '@/api/users'
import { roleLabel } from '@/auth/permissions'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'
import ChangeRoleModal from './users/ChangeRoleModal.vue'
import CreateUserModal from './users/CreateUserModal.vue'
import ResetPasswordModal from './users/ResetPasswordModal.vue'

const { t } = useI18n()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()
const session = useSessionStore()

const users = ref<ManagedUser[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)
const busyId = ref<number | null>(null)

const showCreate = ref(false)
const showRole = ref(false)
const showPassword = ref(false)
const selected = ref<ManagedUser | null>(null)

async function load() {
  loading.value = true
  try {
    users.value = await fetchUsers()
    loadError.value = null
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

onMounted(load)

function replace(user: ManagedUser) {
  users.value = users.value.map((u) => (u.id === user.id ? user : u))
}

function onCreated(user: ManagedUser) {
  users.value = [...users.value, user]
  message.success(t('users.create.done', { username: user.username }))
}

function onRoleUpdated(user: ManagedUser) {
  replace(user)
  message.success(t('users.role.done', { username: user.username }))
}

function onPasswordSet(user: ManagedUser) {
  message.success(t('users.password.done', { username: user.username }))
}

/** Runs a row action with a busy indicator; errors are shown as toasts. */
async function runOn(user: ManagedUser, action: () => Promise<void>) {
  busyId.value = user.id
  try {
    await action()
  } catch (err) {
    showError(err)
  } finally {
    busyId.value = null
  }
}

async function toggleDisabled(user: ManagedUser) {
  const disabling = !user.disabled
  if (disabling) {
    const ok = await confirm({
      title: t('users.confirm.disable.title', { username: user.username }),
      content: t('users.confirm.disable.content', { username: user.username }),
      confirmText: t('users.action.disable'),
      danger: true,
    })
    if (!ok) return
  }
  await runOn(user, async () => {
    replace(await updateUser(user.id, { disabled: disabling }))
    message.success(
      t(disabling ? 'users.done.disabled' : 'users.done.enabled', { username: user.username }),
    )
  })
}

async function resetTotp(user: ManagedUser) {
  const ok = await confirm({
    title: t('users.confirm.resetTotp.title', { username: user.username }),
    content: t('users.confirm.resetTotp.content', { username: user.username }),
    confirmText: t('users.action.resetTotp'),
    danger: true,
  })
  if (!ok) return
  await runOn(user, async () => {
    await resetUserTotp(user.id)
    replace({ ...user, totpEnabled: false })
    message.success(t('users.done.totpReset', { username: user.username }))
  })
}

async function remove(user: ManagedUser) {
  const ok = await confirm({
    title: t('users.confirm.delete.title', { username: user.username }),
    content: t('users.confirm.delete.content', { username: user.username }),
    confirmText: t('users.action.delete'),
    danger: true,
  })
  if (!ok) return
  await runOn(user, async () => {
    await deleteUser(user.id)
    users.value = users.value.filter((u) => u.id !== user.id)
    message.success(t('users.done.deleted', { username: user.username }))
  })
}

function actionOptions(user: ManagedUser): DropdownOption[] {
  // The server refuses these on your own account; say so up front.
  const self = user.id === session.user?.id
  return [
    { key: 'role', label: t('users.action.changeRole'), disabled: self },
    {
      key: 'toggle',
      label: user.disabled ? t('users.action.enable') : t('users.action.disable'),
      disabled: self,
    },
    { key: 'password', label: t('users.action.resetPassword') },
    { key: 'totp', label: t('users.action.resetTotp'), disabled: !user.totpEnabled },
    { type: 'divider', key: 'divider' },
    { key: 'delete', label: t('users.action.delete'), disabled: self },
  ]
}

function onAction(key: string | number, user: ManagedUser) {
  selected.value = user
  switch (key) {
    case 'role':
      showRole.value = true
      break
    case 'password':
      showPassword.value = true
      break
    case 'toggle':
      void toggleDisabled(user)
      break
    case 'totp':
      void resetTotp(user)
      break
    case 'delete':
      void remove(user)
      break
  }
}

function tag(type: 'success' | 'warning' | 'default' | 'info', text: string) {
  return h(NTag, { type, size: 'small', bordered: false }, () => text)
}

const columns = computed<DataTableColumns<ManagedUser>>(() => [
  {
    key: 'username',
    title: t('common.field.username'),
    minWidth: 160,
    render: (user) =>
      h('span', { class: 'name' }, [
        user.username,
        user.id === session.user?.id ? tag('info', t('users.you')) : null,
      ]),
  },
  { key: 'role', title: t('common.field.role'), width: 140, render: (user) => roleLabel(user.role) },
  {
    key: 'disabled',
    title: t('users.column.status'),
    width: 120,
    render: (user) =>
      user.disabled ? tag('warning', t('users.status.disabled')) : tag('success', t('users.status.active')),
  },
  {
    key: 'totpEnabled',
    title: t('users.column.totp'),
    width: 140,
    render: (user) =>
      user.totpEnabled ? tag('success', t('common.status.on')) : tag('default', t('common.status.off')),
  },
  {
    key: 'createdAt',
    title: t('users.column.created'),
    width: 180,
    render: (user) => formatDateTime(user.createdAt),
  },
  {
    key: 'lastLoginAt',
    title: t('users.column.lastLogin'),
    width: 180,
    render: (user) => (user.lastLoginAt > 0 ? formatDateTime(user.lastLoginAt) : t('users.never')),
  },
  {
    key: 'actions',
    title: '',
    width: 64,
    render: (user) =>
      h(
        NDropdown,
        {
          trigger: 'click',
          options: actionOptions(user),
          onSelect: (key: string | number) => onAction(key, user),
        },
        () =>
          h(
            NButton,
            {
              size: 'small',
              quaternary: true,
              loading: busyId.value === user.id,
              'aria-label': t('users.column.actions'),
            },
            { icon: () => h(NIcon, null, () => h(EllipsisHorizontal)) },
          ),
      ),
  },
])
</script>

<template>
  <div class="users">
    <header class="users__header">
      <NH2 class="users__title">{{ t('users.title') }}</NH2>
      <NButton type="primary" @click="showCreate = true">
        <template #icon>
          <NIcon><AddOutline /></NIcon>
        </template>
        {{ t('users.create.title') }}
      </NButton>
    </header>

    <NAlert v-if="loadError" type="error" class="users__alert">{{ errorMessage(loadError) }}</NAlert>

    <NCard size="small">
      <NDataTable
        :columns="columns"
        :data="users"
        :loading="loading"
        :row-key="(user: ManagedUser) => user.id"
        :scroll-x="960"
        size="small"
      />
    </NCard>

    <CreateUserModal v-model:show="showCreate" @created="onCreated" />
    <ChangeRoleModal v-model:show="showRole" :user="selected" @updated="onRoleUpdated" />
    <ResetPasswordModal v-model:show="showPassword" :user="selected" @done="onPasswordSet" />
  </div>
</template>

<style scoped>
.users {
  max-inline-size: 1200px;
}

.users__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 16px;
  margin-block-end: 16px;
}

.users__title {
  margin: 0;
}

.users__alert {
  margin-block-end: 16px;
}

:deep(.name) {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
</style>
