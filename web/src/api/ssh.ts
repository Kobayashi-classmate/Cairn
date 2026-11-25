// SPDX-License-Identifier: AGPL-3.0-only
//
// SSH server settings, authorized keys and the login log (`ssh.read` to
// view, `ssh.manage` to change; changes are step-up protected). Settings
// and port changes run as jobs. Key pairs can also be made on the server:
// it authorizes the public key and hands the private key out once.
import { api } from './client'
import type { FirewallBackend } from './firewall'
import type { Job } from './jobs'

export type PermitRootLogin = 'yes' | 'prohibit-password' | 'no'
export const PERMIT_ROOT_LOGIN: readonly PermitRootLogin[] = ['prohibit-password', 'no', 'yes']

export interface SshUser {
  name: string
  uid: number
  /** Number of authorized keys. */
  keys: number
  root: boolean
}

/** A port move in progress: both ports listen until the old one closes. */
export interface SshPortChange {
  oldPorts: number[]
  newPort: number
  /** Unix seconds the old port closes. */
  closeAt: number
  /**
   * Unix seconds a connection from another host first reached the new port;
   * absent while none has. Only then does the server close the old port on
   * its own.
   */
  reachedAt?: number
  /**
   * Why the server kept the old port when the time ran out: nobody had
   * connected to the new port from outside, or sshd did not answer on it.
   * The move then waits to be finished or cancelled by hand.
   */
  held?: 'unreached' | 'not_listening'
}

export interface SshView {
  installed: boolean
  service: 'ssh' | 'sshd'
  socketActivated: boolean
  ports: number[]
  listenAddresses: string[]
  passwordAuthentication: boolean
  permitRootLogin: PermitRootLogin
  pubkeyAuthentication: boolean
  pending: SshPortChange | null
  users: SshUser[]
  /** False: root's keys are read-only (root access is off in the settings). */
  rootKeys: boolean
  firewall: FirewallBackend
}

export interface SshKey {
  type: string
  fingerprint: string
  comment: string
  /** authorized_keys options, as written ("" for none). */
  options: string
}

export type LoginMethod = 'publickey' | 'password' | 'keyboard-interactive' | ''
export type LoginResult = 'accepted' | 'failed' | 'invalid_user'

export interface SshLogin {
  time: number
  user: string
  ip: string
  method: LoginMethod
  result: LoginResult
}

export interface SshLoginPage {
  entries: SshLogin[]
  /** Pass as `before` for older entries; "" on the last page. */
  next: string
}

/** Minutes before the old port closes after a port move. */
export const CLOSE_AFTER_MIN = 5
export const CLOSE_AFTER_MAX = 240
export const CLOSE_AFTER_DEFAULT = 30

function jobOf(request: Promise<{ job: Job }>): Promise<Job> {
  return request.then((body) => body.job)
}

export function fetchSsh(signal?: AbortSignal): Promise<SshView> {
  return api.get<SshView>('/ssh', { signal })
}

export function updateSshSettings(body: { passwordAuthentication: boolean; permitRootLogin: PermitRootLogin }): Promise<Job> {
  return jobOf(api.put('/ssh/settings', body))
}

/** Starts listening on `port` too; the old port closes after `closeAfterMinutes`. */
export function changeSshPort(port: number, closeAfterMinutes: number): Promise<Job> {
  return jobOf(api.post('/ssh/port', { port, closeAfterMinutes }))
}

/** Closes the old port now. */
export function finishSshPort(): Promise<Job> {
  return jobOf(api.post('/ssh/port/finish'))
}

/** Keeps the old port and drops the new one. */
export function cancelSshPort(): Promise<Job> {
  return jobOf(api.post('/ssh/port/cancel'))
}

export async function fetchSshKeys(user: string, signal?: AbortSignal): Promise<SshKey[]> {
  return (await api.get<{ keys: SshKey[] }>(`/ssh/keys?user=${encodeURIComponent(user)}`, { signal })).keys
}

export async function addSshKey(user: string, key: string): Promise<SshKey> {
  return (await api.post<{ key: SshKey }>('/ssh/keys', { user, key })).key
}

/** Key types the server generates; RSA keys have 4096 bits. */
export type SshKeyType = 'ed25519' | 'rsa'
export const SSH_KEY_TYPES: readonly SshKeyType[] = ['ed25519', 'rsa']
/** Limits of a key pair request, in UTF-8 bytes as the server counts them. */
export const KEY_COMMENT_MAX = 255
export const KEY_PASSPHRASE_MAX = 1024

export interface GenerateSshKeyRequest {
  user: string
  type: SshKeyType
  /** Ends the public key line; empty: "<user>@<host name>". */
  comment?: string
  /** Encrypts the private key; empty leaves it unencrypted. */
  passphrase?: string
}

/** A new key pair. The server keeps only the public key; this is the only copy of the private one. */
export interface GeneratedSshKey {
  /** The authorized key, as listed. */
  key: SshKey
  /** The line added to authorized_keys. */
  publicKey: string
  /** The private key in OpenSSH format, encrypted when a passphrase was given. */
  privateKey: string
  /** Where ssh looks for it by default: id_ed25519 or id_rsa. */
  fileName: string
}

/** Makes a key pair on the server and authorizes its public key for the user (step-up protected). */
export function generateSshKey(body: GenerateSshKeyRequest): Promise<GeneratedSshKey> {
  return api.post<GeneratedSshKey>('/ssh/keys/generate', body)
}

export function deleteSshKey(user: string, fingerprint: string): Promise<void> {
  const query = new URLSearchParams({ user, fingerprint })
  return api.delete<void>(`/ssh/keys?${query.toString()}`)
}

export function fetchSshLogins(limit: number, before?: string, signal?: AbortSignal): Promise<SshLoginPage> {
  const query = new URLSearchParams({ limit: String(limit) })
  if (before) query.set('before', before)
  return api.get<SshLoginPage>(`/ssh/logins?${query.toString()}`, { signal })
}
