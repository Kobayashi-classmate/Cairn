// SPDX-License-Identifier: AGPL-3.0-only
//
// Database servers: MySQL/MariaDB, PostgreSQL and Redis/Valkey
// (`databases.read` to view, `databases.manage` to change). Changes run as
// jobs; several routes are step-up protected.
import { API_BASE, ApiError, api, apiErrorFromBody, reportUnauthorized, type ErrorParams } from './client'
import type { Job } from './jobs'

export type EngineKind = 'mysql' | 'postgresql' | 'redis'
export type GrantLevel = 'all' | 'readonly'

export interface DbGrant {
  database: string
  level: GrantLevel
}

export interface DbDatabase {
  name: string
  sizeBytes: number
  owner?: string
  /** Created through the panel. */
  managed: boolean
  /** Recorded by the panel but no longer on the server. */
  missing?: boolean
  createdAt?: number
  /** Backup plans that include this database, with their last runs; absent when none does. */
  backup?: DbBackupSummary
  /** Newest dump of this database in the panel's dump folder (Unix seconds). */
  lastDumpAt?: number
}

export interface DbBackupSummary {
  plans: string[]
  lastOkAt?: number
  /** Only when later than the last successful run. */
  lastFailedAt?: number
}

export interface DbUser {
  name: string
  hosts: string[]
  grants: DbGrant[]
  managed: boolean
  missing?: boolean
  /** The panel knows the password and can show it. */
  hasPassword: boolean
  updatedAt?: number
}

export type RedisPersistence = 'rdb' | 'aof' | 'both' | 'none'
export const REDIS_PERSISTENCE: readonly RedisPersistence[] = ['rdb', 'aof', 'both', 'none']
export const REDIS_POLICIES = [
  'noeviction',
  'allkeys-lru',
  'allkeys-lfu',
  'volatile-lru',
  'volatile-lfu',
  'allkeys-random',
  'volatile-random',
  'volatile-ttl',
] as const

export interface RedisInfo {
  passwordSet: boolean
  /** 0: no limit. */
  maxMemoryMb: number
  maxMemoryPolicy: string
  persistence: RedisPersistence
  keys: number
  usedMemory: number
}

export interface DbEngine {
  /** "mysql", "postgresql-<line>" or "redis". */
  id: string
  kind: EngineKind
  /** mariadb, mysql, postgresql, redis or valkey. */
  software: string
  service?: string
  line: string
  version?: string
  running: boolean
  port?: number
  /** Listening on every address rather than locally only. */
  remote: boolean
  /** PostgreSQL line installed without its main cluster. */
  noCluster?: boolean
  redis?: RedisInfo
  /** Why the engine could not be read. */
  error?: { code: string; params?: ErrorParams }
  databases: DbDatabase[]
  users: DbUser[]
  /** The panel knows the Redis password and can show it. */
  redisPasswordKnown?: boolean
}

export interface DbStatus {
  supported: boolean
  /** "platform": databases are not available on this distribution. */
  reason?: string
  statusAvailable: boolean
  engines: DbEngine[]
  /** Installed engines the panel does not manage on this system yet (catalog ids). */
  unmanaged?: string[]
  /** False when the backups module is off: last backups are then unknown, not missing. */
  backups?: boolean
}

export interface DbCreateRequest {
  name: string
  /** Owner user name; defaults to the database name. */
  user?: string
  /** Do not create an owner user. */
  noUser?: boolean
  /** The new user's password; empty generates one. */
  password?: string
  /** The new user's hosts; default local only. */
  hosts?: string[]
}

export interface DbUserRequest {
  /** Create only. */
  name?: string
  /** Empty generates one on create and keeps the current one on update. */
  password?: string
  hosts: string[]
  grants: DbGrant[]
}

export interface DumpInfo {
  engine: string
  database?: string
  file: string
  sizeBytes: number
  createdAt: number
}

export interface RedisConfigRequest {
  /** Absent keeps it, "" removes it. */
  password?: string
  generatePassword?: boolean
  maxMemoryMb?: number
  maxMemoryPolicy?: string
  persistence?: RedisPersistence
}

export interface RedisKey {
  key: string
  type: string
  /** Seconds; -1 = no expiry. */
  ttl: number
}

export interface RedisValue {
  key: string
  type: string
  ttl: number
  length: number
  /** Strings. */
  value?: string
  /** list/set; zset as member, score pairs; hash as field, value pairs; stream as lines. */
  items?: string[]
  truncated?: boolean
}

const base = (engine: string) => `/databases/${encodeURIComponent(engine)}`
const jobOf = async (p: Promise<{ job: Job }>) => (await p).job

export function fetchDatabases(): Promise<DbStatus> {
  return api.get<DbStatus>('/databases')
}

export function createDatabase(engine: string, body: DbCreateRequest): Promise<Job> {
  return jobOf(api.post<{ job: Job }>(`${base(engine)}/dbs`, body))
}

/** Step-up protected. */
export function dropDatabase(engine: string, name: string, dropOwner: boolean): Promise<Job> {
  const query = dropOwner ? '?dropOwner=true' : ''
  return jobOf(api.delete<{ job: Job }>(`${base(engine)}/dbs/${encodeURIComponent(name)}${query}`))
}

export function createDbUser(engine: string, body: DbUserRequest): Promise<Job> {
  return jobOf(api.post<{ job: Job }>(`${base(engine)}/users`, body))
}

/** Step-up protected. */
export function updateDbUser(engine: string, name: string, body: DbUserRequest): Promise<Job> {
  return jobOf(api.put<{ job: Job }>(`${base(engine)}/users/${encodeURIComponent(name)}`, body))
}

/** Step-up protected. */
export function dropDbUser(engine: string, name: string): Promise<Job> {
  return jobOf(api.delete<{ job: Job }>(`${base(engine)}/users/${encodeURIComponent(name)}`))
}

/** Step-up protected; db.password_unknown when the panel does not know it. */
export async function revealDbPassword(engine: string, name: string): Promise<string> {
  return (await api.get<{ password: string }>(`${base(engine)}/users/${encodeURIComponent(name)}/password`)).password
}

/** Step-up protected. */
export function setRemoteAccess(engine: string, enabled: boolean): Promise<Job> {
  return jobOf(api.put<{ job: Job }>(`${base(engine)}/remote`, { enabled }))
}

export function controlService(engine: string, action: 'start' | 'stop' | 'restart'): Promise<Job> {
  return jobOf(api.post<{ job: Job }>(`${base(engine)}/service`, { action }))
}

/** Creates the main cluster of a PostgreSQL line installed without one. */
export function createCluster(engine: string): Promise<Job> {
  return jobOf(api.post<{ job: Job }>(`${base(engine)}/cluster`))
}

export async function fetchDumps(engine: string): Promise<DumpInfo[]> {
  return (await api.get<{ dumps: DumpInfo[] }>(`${base(engine)}/dumps`)).dumps
}

/** Dumps one database (MySQL/PostgreSQL) or snapshots Redis (no name). */
export function createDump(engine: string, name?: string): Promise<Job> {
  return jobOf(api.post<{ job: Job }>(`${base(engine)}/dumps`, name ? { name } : {}))
}

/** Step-up protected. */
export async function deleteDump(engine: string, file: string): Promise<void> {
  await api.delete<unknown>(`${base(engine)}/dumps/${encodeURIComponent(file)}`)
}

/** Address of a dump for a plain download link (needs a re-authenticated session). */
export function dumpUrl(engine: string, file: string): string {
  return `${API_BASE}${base(engine)}/dumps/${encodeURIComponent(file)}`
}

/** Step-up protected; runs as the database's owner or `user`. */
export function restoreDump(engine: string, database: string, file: string, user?: string): Promise<Job> {
  return jobOf(
    api.post<{ job: Job }>(`${base(engine)}/dbs/${encodeURIComponent(database)}/restore`, user ? { file, user } : { file }),
  )
}

/**
 * Uploads a dump (step-up protected; the session must already be elevated).
 * Uses XMLHttpRequest for upload progress; the file is streamed from disk.
 */
export function uploadDump(
  engine: string,
  file: File,
  onProgress: (fraction: number) => void,
): Promise<DumpInfo> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('PUT', `${API_BASE}${base(engine)}/dumps/${encodeURIComponent(file.name)}`)
    xhr.withCredentials = true
    xhr.setRequestHeader('Content-Type', 'application/octet-stream')
    xhr.setRequestHeader('Accept', 'application/json')
    // CSRF guard, as for every state-changing request.
    xhr.setRequestHeader('X-Cairn-Request', '1')
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) onProgress(event.loaded / event.total)
    }
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        try {
          resolve(JSON.parse(xhr.responseText) as DumpInfo)
        } catch {
          reject(new ApiError(xhr.status, 'client.bad_response'))
        }
        return
      }
      const error = apiErrorFromBody(xhr.status, xhr.responseText)
      if (xhr.status === 401) reportUnauthorized(error)
      reject(error)
    }
    xhr.onerror = () => reject(apiErrorFromBody(0, ''))
    xhr.send(file)
  })
}

/** Step-up protected. */
export function configureRedis(body: RedisConfigRequest): Promise<Job> {
  return jobOf(api.put<{ job: Job }>('/databases/redis/config', body))
}

/** Step-up protected. */
export async function revealRedisPassword(): Promise<string> {
  return (await api.get<{ password: string }>('/databases/redis/password')).password
}

export function scanRedisKeys(
  db: number,
  pattern: string,
  cursor: string,
  count = 200,
): Promise<{ keys: RedisKey[]; cursor: string }> {
  const query = new URLSearchParams({ db: String(db), pattern, cursor, count: String(count) })
  return api.get<{ keys: RedisKey[]; cursor: string }>(`/databases/redis/keys?${query.toString()}`)
}

export function fetchRedisValue(db: number, key: string): Promise<RedisValue> {
  const query = new URLSearchParams({ db: String(db), key })
  return api.get<RedisValue>(`/databases/redis/value?${query.toString()}`)
}

/** Step-up protected. Returns how many keys were deleted. */
export async function deleteRedisKeys(db: number, keys: string[]): Promise<number> {
  return (await api.post<{ deleted: number }>('/databases/redis/delete', { db, keys })).deleted
}

/** Step-up protected; 0 seconds removes the expiry. */
export async function setRedisTtl(db: number, key: string, seconds: number): Promise<void> {
  await api.put<unknown>('/databases/redis/ttl', { db, key, seconds })
}
