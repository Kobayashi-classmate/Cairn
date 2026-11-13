// SPDX-License-Identifier: AGPL-3.0-only
//
// User management (permission `users.manage`). Every mutation is step-up protected.
import { api } from './client'
import type { Role } from '@/auth/permissions'

export interface ManagedUser {
  id: number
  username: string
  role: Role
  locale: string
  disabled: boolean
  totpEnabled: boolean
  /** Unix seconds. */
  createdAt: number
  /** Unix seconds; 0 when the user never signed in. */
  lastLoginAt: number
}

export interface CreateUserRequest {
  username: string
  password: string
  role: Role
}

export interface UpdateUserRequest {
  role?: Role
  disabled?: boolean
}

export async function fetchUsers(): Promise<ManagedUser[]> {
  return (await api.get<{ users: ManagedUser[] }>('/users')).users
}

export async function createUser(body: CreateUserRequest): Promise<ManagedUser> {
  return (await api.post<{ user: ManagedUser }>('/users', body)).user
}

export async function updateUser(id: number, body: UpdateUserRequest): Promise<ManagedUser> {
  return (await api.patch<{ user: ManagedUser }>(`/users/${id}`, body)).user
}

/** Sets a new password; the user is signed out everywhere. */
export function setUserPassword(id: number, password: string): Promise<void> {
  return api.put<void>(`/users/${id}/password`, { password })
}

/** Turns off the user's two-factor authentication. */
export function resetUserTotp(id: number): Promise<void> {
  return api.delete<void>(`/users/${id}/totp`)
}

export function deleteUser(id: number): Promise<void> {
  return api.delete<void>(`/users/${id}`)
}
