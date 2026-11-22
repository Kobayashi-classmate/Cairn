// SPDX-License-Identifier: AGPL-3.0-only
//
// Account actions of the users table's "more" menu. Every change asks to
// re-authenticate (the API client handles it); the ones that lock someone
// out or remove something say so first, and deleting asks for the name.
import { deleteUser, resetUserTotp, updateUser, type ManagedUser } from '@/api/users'
import { confirmAction } from '@/composables/useConfirmDialog'
import { i18n } from '@/i18n'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useUsersStore } from '@/stores/users'

export function askDisable(user: ManagedUser): Promise<boolean> {
  const { t } = i18n.global
  return confirmAction({
    title: t('users.confirm.disable.title', { username: user.username }),
    description: t('users.confirm.disable.content', { username: user.username }),
    confirmLabel: t('users.action.disable'),
    destructive: true,
    run: async () => {
      useUsersStore().put(await updateUser(user.id, { disabled: true }))
      notifySuccess(t('users.done.disabled', { username: user.username }))
    },
  })
}

/** Enabling locks nobody out: no question first. */
export async function enable(user: ManagedUser): Promise<void> {
  const { t } = i18n.global
  try {
    useUsersStore().put(await updateUser(user.id, { disabled: false }))
    notifySuccess(t('users.done.enabled', { username: user.username }))
  } catch (err) {
    notifyError(err)
  }
}

export function askResetTotp(user: ManagedUser): Promise<boolean> {
  const { t } = i18n.global
  return confirmAction({
    title: t('users.confirm.resetTotp.title', { username: user.username }),
    description: t('users.confirm.resetTotp.content', { username: user.username }),
    confirmLabel: t('users.action.resetTotp'),
    destructive: true,
    run: async () => {
      await resetUserTotp(user.id)
      useUsersStore().put({ ...user, totpEnabled: false })
      notifySuccess(t('users.done.totpReset', { username: user.username }))
    },
  })
}

export function askDelete(user: ManagedUser): Promise<boolean> {
  const { t } = i18n.global
  return confirmAction({
    title: t('users.confirm.delete.title', { username: user.username }),
    description: t('users.confirm.delete.content', { username: user.username }),
    confirmLabel: t('users.confirm.delete.confirm'),
    destructive: true,
    typeToConfirm: user.username,
    run: async () => {
      await deleteUser(user.id)
      useUsersStore().remove(user.id)
      notifySuccess(t('users.done.deleted', { username: user.username }))
    },
  })
}
