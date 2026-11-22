// SPDX-License-Identifier: AGPL-3.0-only
//
// Backups (`backups.read` to view, `backups.manage` to change): storage
// (S3-compatible buckets) and plans. Storage changes, downloads, deleting
// backups and restoring are step-up protected; runs and restores are jobs.
import { api, type ErrorParams } from './client'
import type { Job } from './jobs'

export interface S3Storage {
  /** host[:port], without a scheme. */
  endpoint: string
  region?: string
  bucket: string
  prefix?: string
  accessKey: string
  /** Addresses the bucket in the path (MinIO and some others). */
  pathStyle?: boolean
  /** Plain HTTP (a local MinIO, say). */
  insecure?: boolean
}

export interface StorageView extends S3Storage {
  id: string
  name: string
  /** The panel has the secret key (it never sends it back). */
  secretSet: boolean
  createdAt: number
  updatedAt: number
}

export interface StorageRequest extends S3Storage {
  name: string
  /** Empty keeps the saved one when changing a storage. */
  secretKey: string
}

export type TargetKind = 'site' | 'database' | 'directory'

export interface BackupTarget {
  kind: TargetKind
  site?: string
  engine?: string
  database?: string
  root?: string
  path?: string
}

/** Keep the newest `last` backups, plus the newest of each of the last days, weeks and months. */
export interface Retention {
  last: number
  daily: number
  weekly: number
  monthly: number
}

export const DEFAULT_RETENTION: Retention = { last: 1, daily: 7, weekly: 4, monthly: 6 }

export interface ApiErrorBody {
  code: string
  params?: ErrorParams
}

export interface BackupRun {
  started: number
  finished?: number
  ok: boolean
  error?: ApiErrorBody
  /** Backup files made. */
  files?: string[]
  size: number
  /** Old backups retention removed. */
  deleted?: number
}

export interface BackupPlan {
  id: string
  name: string
  targets: BackupTarget[]
  /** Bucket that gets a copy of every backup; absent: local only. */
  storageId?: string
  keepLocal: boolean
  retention: Retention
  /** age recipients the backups are encrypted for. */
  recipients: string[]
  lastRun?: BackupRun
  /** Scheduled tasks running this plan. */
  tasks: string[]
  createdAt: number
  updatedAt: number
}

export interface PlanRequest {
  name: string
  targets: BackupTarget[]
  storageId?: string
  keepLocal: boolean
  retention: Retention
  recipients: string[]
}

export interface BackupFile {
  name: string
  /** Target key: "site-<code>", "db-<engine>-<database>", "dir-…". */
  target: string
  size: number
  created: number
  local: boolean
  remote: boolean
  encrypted: boolean
}

export interface BackupFiles {
  files: BackupFile[]
  /** Why the bucket could not be listed. */
  remoteError?: ApiErrorBody
}

export interface RestoreRequest {
  name: string
  /** age private key of an encrypted backup; used for this request only. */
  identity?: string
  /** Where site and directory backups are extracted (file manager root and path). */
  dest: { root: string; path: string }
  overwrite: boolean
}

/** An age X25519 recipient ("age1…"). */
export const AGE_RECIPIENT = /^age1[02-9ac-hj-np-z]{58}$/
/** An age X25519 private key ("AGE-SECRET-KEY-1…"). */
export const AGE_IDENTITY = /^AGE-SECRET-KEY-1[02-9AC-HJ-NP-Z]{58}$/
/** Bucket names the server accepts. */
export const BUCKET_NAME = /^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/

async function jobOf(request: Promise<{ job: Job }>): Promise<Job> {
  return (await request).job
}

function storagePath(id: string): string {
  return `/backups/storages/${encodeURIComponent(id)}`
}

function planPath(id: string): string {
  return `/backups/plans/${encodeURIComponent(id)}`
}

export async function fetchStorages(signal?: AbortSignal): Promise<StorageView[]> {
  return (await api.get<{ storages: StorageView[] }>('/backups/storages', { signal })).storages
}

/** Step-up protected. */
export function createStorage(body: StorageRequest): Promise<StorageView> {
  return api.post<StorageView>('/backups/storages', body)
}

/** Step-up protected; an empty secret key keeps the saved one. */
export function updateStorage(id: string, body: StorageRequest): Promise<StorageView> {
  return api.put<StorageView>(storagePath(id), body)
}

/** Step-up protected; fails with backup.storage_in_use while a plan uses it. */
export function deleteStorage(id: string): Promise<void> {
  return api.delete<void>(storagePath(id))
}

/** Settings to check before saving them. */
export interface StorageTestRequest extends S3Storage {
  secretKey: string
  /** A saved storage whose secret key is used when `secretKey` is empty. */
  id?: string
}

/** Checks settings before saving them; fails with backup.storage_failed. */
export function testNewStorage(body: StorageTestRequest): Promise<void> {
  return api.post<void>('/backups/storages/test', body)
}

export function testStorage(id: string): Promise<void> {
  return api.post<void>(`${storagePath(id)}/test`)
}

export async function fetchPlans(signal?: AbortSignal): Promise<BackupPlan[]> {
  return (await api.get<{ plans: BackupPlan[] }>('/backups/plans', { signal })).plans
}

export function createPlan(body: PlanRequest): Promise<BackupPlan> {
  return api.post<BackupPlan>('/backups/plans', body)
}

export function updatePlan(id: string, body: PlanRequest): Promise<BackupPlan> {
  return api.put<BackupPlan>(planPath(id), body)
}

/** Step-up protected; `files` deletes the plan's backups too. */
export function deletePlan(id: string, files: boolean): Promise<void> {
  return api.delete<void>(`${planPath(id)}${files ? '?files=true' : ''}`)
}

export function runPlan(id: string): Promise<Job> {
  return jobOf(api.post(`${planPath(id)}/run`))
}

export async function fetchPlanRuns(id: string, signal?: AbortSignal): Promise<BackupRun[]> {
  return (await api.get<{ runs: BackupRun[] }>(`${planPath(id)}/runs`, { signal })).runs
}

export function fetchBackupFiles(id: string, signal?: AbortSignal): Promise<BackupFiles> {
  return api.get<BackupFiles>(`${planPath(id)}/files`, { signal })
}

/**
 * Where a backup downloads from (step-up protected): the panel's own copy,
 * or a short-lived link to the bucket's. Asking first reports errors (gone,
 * not allowed, the bucket unreachable) on the page instead of in place of it.
 */
export async function backupFileLink(id: string, name: string): Promise<string> {
  return (await api.get<{ url: string }>(`${planPath(id)}/files/${encodeURIComponent(name)}?link=1`)).url
}

/** Step-up protected; `remote` deletes the bucket's copies too. */
export function deleteBackupFiles(id: string, names: string[], remote: boolean): Promise<Job> {
  return jobOf(api.post(`${planPath(id)}/delete`, { names, remote }))
}

/** Step-up protected. */
export function restoreBackup(id: string, body: RestoreRequest): Promise<Job> {
  return jobOf(api.post(`${planPath(id)}/restore`, body))
}

/** Database backups ("db-…") become a dump of the databases page instead of files. */
export function isDatabaseBackup(name: string): boolean {
  return name.startsWith('db-')
}
