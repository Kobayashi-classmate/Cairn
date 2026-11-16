// SPDX-License-Identifier: AGPL-3.0-only
//
// Scheduled tasks (`cron.read` to view, `cron.manage` to change, `cron.root`
// for tasks that run as root). Each task is a systemd timer; changes run as
// jobs. Root tasks are step-up protected.
import { api } from './client'
import type { Job } from './jobs'
import type { TermUsers } from './terminal'

export const TASK_TYPES = ['shell', 'url', 'backup'] as const
export type TaskType = (typeof TASK_TYPES)[number]
export type TaskState = 'applying' | 'active' | 'failed'
export type RunResult = 'success' | 'failed' | 'timeout' | 'running'

export interface CronTask {
  id: string
  type: TaskType
  /** systemd OnCalendar expression. */
  schedule: string
  /** Who runs a shell task. */
  user?: string
  script?: string
  url?: string
  /** Backup plan a backup task runs. */
  plan?: string
  /** Minutes before a run is stopped (0: one hour). */
  timeoutMin?: number
  enabled: boolean
  name: string
  state: TaskState
  /** Why applying the task failed. */
  errorCode?: string
  lastJob?: string
  /** Unix seconds; absent when not known. */
  nextRun?: number
  lastRun?: number
  running: boolean
  /** Result of the last run. */
  result?: RunResult
  createdAt: number
  updatedAt: number
}

export interface CronList {
  /** Tasks need systemd; without it none can be created. */
  systemd: boolean
  /** False when the helper could not be asked about the timers. */
  statusAvailable: boolean
  tasks: CronTask[]
}

export interface TaskRequest {
  name: string
  type: TaskType
  schedule: string
  user?: string
  script?: string
  url?: string
  plan?: string
  timeoutMin?: number
  enabled: boolean
}

export interface ScheduleInfo {
  /** The expression as systemd reads it. */
  normalized: string
  /** The next runs, Unix seconds. */
  next: number[]
}

export interface CronRun {
  /** Empty while systemd has not named the run yet. */
  invocation?: string
  started: number
  finished?: number
  result: RunResult
  exitCode?: number
  /** Started from the panel, not by the timer. */
  manual?: boolean
}

export interface CronLog {
  lines: string[]
  truncated?: boolean
}

/** Largest script (bytes). */
export const SCRIPT_MAX = 64 << 10
/** Longest timeout (minutes). */
export const TIMEOUT_MAX = 24 * 60
export const TIMEOUT_DEFAULT = 60

async function jobOf(request: Promise<{ job: Job }>): Promise<Job> {
  return (await request).job
}

function path(id: string): string {
  return `/cron/${encodeURIComponent(id)}`
}

/** Who a script task can run as; root only for holders of `cron.root`. */
export function fetchCronUsers(signal?: AbortSignal): Promise<TermUsers> {
  return api.get<TermUsers>('/cron/users', { signal })
}

export function fetchTasks(signal?: AbortSignal): Promise<CronList> {
  return api.get<CronList>('/cron', { signal })
}

/** How systemd reads a schedule; fails with cron.bad_schedule. */
export function checkSchedule(expr: string, signal?: AbortSignal): Promise<ScheduleInfo> {
  return api.get<ScheduleInfo>(`/cron/schedule?expr=${encodeURIComponent(expr)}`, { signal })
}

/** Root tasks are step-up protected. */
export function createTask(body: TaskRequest): Promise<Job> {
  return jobOf(api.post('/cron', body))
}

export function updateTask(id: string, body: TaskRequest): Promise<Job> {
  return jobOf(api.put(path(id), body))
}

export function deleteTask(id: string): Promise<Job> {
  return jobOf(api.delete(path(id)))
}

/** Starts a run; the run itself shows up in the task's history. */
export function runTask(id: string): Promise<Job> {
  return jobOf(api.post(`${path(id)}/run`))
}

export async function fetchRuns(id: string, signal?: AbortSignal): Promise<CronRun[]> {
  return (await api.get<{ runs: CronRun[] }>(`${path(id)}/runs`, { signal })).runs
}

export function fetchRunLog(id: string, invocation: string, signal?: AbortSignal): Promise<CronLog> {
  return api.get<CronLog>(`${path(id)}/runs/${encodeURIComponent(invocation)}/log`, { signal })
}
