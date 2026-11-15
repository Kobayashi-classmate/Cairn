// SPDX-License-Identifier: AGPL-3.0-only
//
// Four-layer (TCP/UDP) proxies through nginx's stream module
// (`sites.read` to view, `sites.manage` to change).
import { api } from './client'
import type { Job } from './jobs'

export type StreamProtocol = 'tcp' | 'udp'

export interface StreamRequest {
  /** Defaults to "<protocol>-<port>"; fixed once created. */
  code?: string
  protocol: StreamProtocol
  /** 1-65535 except 80, 443 and the app range 20000-29999. */
  port: number
  /** host:port or [v6]:port, up to 16. */
  upstreams: string[]
  /** "hash" keeps a client on one upstream; absent means round robin. */
  balance?: 'hash'
}

export interface StreamView {
  code: string
  protocol: StreamProtocol
  port: number
  upstreams: string[]
  balance?: 'hash'
  /** A stopped stream does not listen. */
  enabled: boolean
  state: 'applying' | 'active' | 'failed'
  errorCode?: string
  lastJob?: string
  createdAt: number
  updatedAt: number
  drift: boolean
}

export async function fetchStreams(): Promise<StreamView[]> {
  return (await api.get<{ streams: StreamView[] }>('/streams')).streams
}

export async function createStream(body: StreamRequest): Promise<Job> {
  return (await api.post<{ job: Job }>('/streams', body)).job
}

export async function updateStream(code: string, body: StreamRequest): Promise<Job> {
  return (await api.put<{ job: Job }>(`/streams/${encodeURIComponent(code)}`, body)).job
}

export async function startStream(code: string): Promise<Job> {
  return (await api.post<{ job: Job }>(`/streams/${encodeURIComponent(code)}/start`)).job
}

export async function stopStream(code: string): Promise<Job> {
  return (await api.post<{ job: Job }>(`/streams/${encodeURIComponent(code)}/stop`)).job
}

/** Step-up protected. */
export async function deleteStream(code: string): Promise<Job> {
  return (await api.delete<{ job: Job }>(`/streams/${encodeURIComponent(code)}`)).job
}
