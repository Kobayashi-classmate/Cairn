// SPDX-License-Identifier: AGPL-3.0-only
//
// The signed-in user's API tokens. Creating one is step-up protected.
import { api } from './client'
import type { Permission } from '@/auth/permissions'

export interface ApiToken {
  id: number
  name: string
  /** Public start of the token, for example "cairn_Ab12Cd34", to tell tokens apart. */
  prefix: string
  permissions: Permission[]
  /** IP addresses or CIDR ranges; empty means any address. */
  allowedIps: string[]
  /** Unix seconds; 0 means the token never expires. */
  expiresAt: number
  /** Unix seconds; 0 when never used. */
  lastUsedAt: number
  /** Unix seconds. */
  createdAt: number
}

export interface CreateTokenRequest {
  name: string
  permissions: Permission[]
  /** 0 means the token never expires. */
  expiresInDays: number
  allowedIps: string[]
}

export interface CreatedToken {
  /** The secret token; the server returns it only in this response. */
  token: string
  info: ApiToken
}

export async function fetchTokens(): Promise<ApiToken[]> {
  return (await api.get<{ tokens: ApiToken[] }>('/me/tokens')).tokens
}

export function createToken(body: CreateTokenRequest): Promise<CreatedToken> {
  return api.post<CreatedToken>('/me/tokens', body)
}

export function revokeToken(id: number): Promise<void> {
  return api.delete<void>(`/me/tokens/${id}`)
}
