// SPDX-License-Identifier: AGPL-3.0-only
//
// The signed-in user's own account. Calls marked "step-up" may first open the
// re-authentication dialog (handled by the API client).
import { api } from './client'

export interface AccountSession {
  id: string
  /** Unix seconds. */
  createdAt: number
  /** Unix seconds. */
  lastSeenAt: number
  /** Client IP address. */
  remote: string
  userAgent: string
  /** The session making this request. */
  current: boolean
}

export interface TotpEnrollment {
  /** Base32 secret for manual entry. */
  secret: string
  /** otpauth:// URI, shown as a QR code. */
  uri: string
}

export interface RecoveryCodes {
  recoveryCodes: string[]
}

/** Changes the password; the server signs out every other session. */
export function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  return api.put<void>('/me/password', { currentPassword, newPassword })
}

export async function fetchSessions(): Promise<AccountSession[]> {
  return (await api.get<{ sessions: AccountSession[] }>('/me/sessions')).sessions
}

/** Ends one session; ending the current one signs the user out. */
export function revokeSession(id: string): Promise<void> {
  return api.delete<void>(`/me/sessions/${encodeURIComponent(id)}`)
}

/** Ends every session except the current one; returns how many were ended. */
export async function revokeOtherSessions(): Promise<number> {
  return (await api.delete<{ revoked: number }>('/me/sessions')).revoked
}

/** Starts two-factor enrollment (step-up). The pending secret expires after 10 minutes. */
export function beginTotp(): Promise<TotpEnrollment> {
  return api.post<TotpEnrollment>('/me/totp/begin')
}

/** Finishes enrollment with a code from the authenticator app. */
export function confirmTotp(code: string): Promise<RecoveryCodes> {
  return api.post<RecoveryCodes>('/me/totp/confirm', { code })
}

/** Turns two-factor authentication off (step-up). */
export function disableTotp(): Promise<void> {
  return api.delete<void>('/me/totp')
}

/** Replaces all recovery codes (step-up). */
export function regenerateRecoveryCodes(): Promise<RecoveryCodes> {
  return api.post<RecoveryCodes>('/me/recovery-codes')
}
