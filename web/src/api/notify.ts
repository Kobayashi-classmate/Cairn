// SPDX-License-Identifier: AGPL-3.0-only
//
// Notification channels and delivery history (permission `notify.manage`).
// Creating, changing and deleting channels is step-up protected.
import { api } from './client'

export const CHANNEL_TYPES = ['email', 'webhook'] as const
export type ChannelType = (typeof CHANNEL_TYPES)[number]

export const LEVELS = ['info', 'warning', 'critical'] as const
export type Level = (typeof LEVELS)[number]

/** Event categories a channel can subscribe to, sent as prefixes with the trailing dot. */
export const EVENT_CATEGORIES = ['security.', 'users.', 'system.', 'cloud.', 'cert.', 'app.', 'cron.', 'backup.', 'ssh.', 'firewall.'] as const

export const EMAIL_SECURITY = ['starttls', 'tls', 'none'] as const
export type EmailSecurity = (typeof EMAIL_SECURITY)[number]

/** Email settings as returned by the server; the password is never sent back. */
export interface EmailConfig {
  host: string
  port: number
  security: EmailSecurity
  username: string
  hasPassword: boolean
  from: string
  to: string[]
}

/** Webhook settings as returned by the server; the secret is never sent back. */
export interface WebhookConfig {
  url: string
  hasSecret: boolean
}

interface ChannelBase {
  id: number
  name: string
  /** Language of the messages sent on this channel. */
  locale: string
  enabled: boolean
  events: string[]
  minLevel: Level
  /** Unix seconds. */
  createdAt: number
}

export type Channel =
  | (ChannelBase & { type: 'email'; config: EmailConfig })
  | (ChannelBase & { type: 'webhook'; config: WebhookConfig })

/**
 * Email settings to save. `password`: omitted keeps the stored one, `""`
 * removes it.
 */
export interface EmailConfigInput {
  host: string
  port: number
  security: EmailSecurity
  username: string
  password?: string
  from: string
  to: string[]
}

/** Webhook settings to save. `secret`: omitted keeps the stored one, `""` removes it. */
export interface WebhookConfigInput {
  url: string
  secret?: string
}

export interface ChannelInput {
  type: ChannelType
  name: string
  locale: string
  enabled: boolean
  events: string[]
  minLevel: Level
  config: EmailConfigInput | WebhookConfigInput
}

export interface Delivery {
  id: number
  channelId: number
  channelName: string
  event: string
  level: Level
  status: 'pending' | 'sent' | 'failed'
  attempts: number
  lastError: string
  /** Unix seconds. */
  createdAt: number
  /** Unix seconds; 0 when not sent. */
  sentAt: number
}

export async function fetchChannels(): Promise<Channel[]> {
  return (await api.get<{ channels: Channel[] }>('/notify/channels')).channels
}

export async function createChannel(body: ChannelInput): Promise<Channel> {
  return (await api.post<{ channel: Channel }>('/notify/channels', body)).channel
}

/** Changes any subset of a channel's fields. */
export async function updateChannel(id: number, body: Partial<ChannelInput>): Promise<Channel> {
  return (await api.patch<{ channel: Channel }>(`/notify/channels/${id}`, body)).channel
}

export function deleteChannel(id: number): Promise<void> {
  return api.delete<void>(`/notify/channels/${id}`)
}

/**
 * Sends a test message. Fails with `notify.delivery_failed` (HTTP 502) whose
 * `reason` parameter is a short technical description in English.
 */
export function testChannel(id: number): Promise<void> {
  return api.post<void>(`/notify/channels/${id}/test`)
}

export async function fetchDeliveries(limit = 50): Promise<Delivery[]> {
  return (await api.get<{ deliveries: Delivery[] }>(`/notify/deliveries?limit=${limit}`)).deliveries
}
