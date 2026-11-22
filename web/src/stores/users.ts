// SPDX-License-Identifier: AGPL-3.0-only
//
// The users page's data: the panel accounts, shared by the table, its row
// actions and the form sheets. Changes are quick calls (no jobs), so the
// list is updated from each answer instead of being reloaded.
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { fetchUsers, type ManagedUser } from '@/api/users'
import { onAccountReset } from './accountReset'

export const useUsersStore = defineStore('users', () => {
  const users = ref<ManagedUser[] | null>(null)
  const loadError = ref<unknown>(null)
  const loading = ref(false)

  /** Numbers the loads, so an older answer arriving late is dropped. */
  let seq = 0

  async function load(): Promise<void> {
    const mine = ++seq
    loading.value = true
    try {
      const list = await fetchUsers()
      if (mine !== seq) return
      users.value = list
      loadError.value = null
    } catch (err) {
      if (mine === seq) loadError.value = err
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  /** Puts a changed or new account in the list. */
  function put(user: ManagedUser): void {
    const list = users.value ?? []
    users.value = list.some((u) => u.id === user.id) ? list.map((u) => (u.id === user.id ? user : u)) : [...list, user]
  }

  /** Changes some fields of a listed account, keeping the rest as the list has them now. */
  function patch(id: number, fields: Partial<ManagedUser>): void {
    if (users.value) users.value = users.value.map((u) => (u.id === id ? { ...u, ...fields } : u))
  }

  function remove(id: number): void {
    users.value = (users.value ?? []).filter((u) => u.id !== id)
  }

  /** Forgets everything (sign-out): the next account loads its own. */
  onAccountReset(() => {
    seq++
    users.value = null
    loadError.value = null
    loading.value = false
  })

  return { users, loadError, loading, load, put, patch, remove }
})
