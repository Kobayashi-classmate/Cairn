// SPDX-License-Identifier: AGPL-3.0-only
//
// TLS certificates (`certs.read` to view, `certs.manage` to change): issued
// through ACME or uploaded, used by sites and by the panel itself.
import { api, type ErrorParams } from './client'
import type { Job } from './jobs'

export type CertSource = 'acme' | 'upload'
export type CertState = 'issuing' | 'valid' | 'failed'
export type Challenge = 'http-01' | 'dns-01'
export type KeyType = 'ec256' | 'ec384' | 'rsa2048' | 'rsa4096'
export const KEY_TYPES: readonly KeyType[] = ['ec256', 'ec384', 'rsa2048', 'rsa4096']

export interface ChainCert {
  subject: string
  issuer: string
  /** Unix seconds. */
  notAfter: number
}

export interface CertView {
  id: string
  name: string
  source: CertSource
  domains: string[]
  /** ACME CA name, for example "letsencrypt". */
  ca?: string
  challenge?: Challenge
  dnsAccount?: string
  keyType?: KeyType
  autoRenew: boolean
  /** A failed renewal keeps "valid" and sets errorCode. */
  state: CertState
  errorCode?: string
  errorParams?: ErrorParams
  /** Unix seconds. */
  notBefore?: number
  notAfter?: number
  issuer?: string
  serial?: string
  fingerprint?: string
  lastJob?: string
  createdAt: number
  updatedAt: number
  /** Codes of the sites serving this certificate. */
  sites: string[]
  /** The panel itself serves it. */
  panel: boolean
  /** Only from fetchCert. */
  chain?: ChainCert[]
}

export interface CertList {
  certs: CertView[]
  /** Id of the panel's certificate; "" for the self-signed one. */
  panel: string
  statusAvailable: boolean
}

export interface IssueRequest {
  name?: string
  domains: string[]
  /** Defaults to the settings' default CA. */
  ca?: string
  challenge: Challenge
  dnsAccount?: string
  keyType?: KeyType
  autoRenew?: boolean
}

export interface UploadRequest {
  name?: string
  /** Certificate chain, leaf first. */
  certPem: string
  keyPem: string
}

export interface AcmeCa {
  name: string
  directory: string
  /** The CA needs External Account Binding (ZeroSSL, Google). */
  needsEab: boolean
  eabKid?: string
  /** The EAB HMAC key is stored; it is never returned. */
  eabHmacSet: boolean
  /** "custom" only: the root that signs the CA's own HTTPS. */
  rootPem?: string
  /** Usable for issuing (EAB or directory configured where needed). */
  ready: boolean
}

export interface AcmeSettings {
  email: string
  defaultCa: string
  cas: AcmeCa[]
}

/**
 * Per-CA changes. `eabHmac`: empty keeps the stored key, "-" removes it.
 * `directory` and `rootPem` are for "custom" only.
 */
export interface AcmeCaRequest {
  eabKid?: string
  eabHmac?: string
  directory?: string
  rootPem?: string
}

export interface AcmeSettingsRequest {
  email: string
  defaultCa: string
  cas?: Record<string, AcmeCaRequest>
}

export function fetchCerts(): Promise<CertList> {
  return api.get<CertList>('/certs')
}

export function fetchCert(id: string): Promise<CertView> {
  return api.get<CertView>(`/certs/${encodeURIComponent(id)}`)
}

export async function issueCert(body: IssueRequest): Promise<Job> {
  return (await api.post<{ job: Job }>('/certs', body)).job
}

export async function uploadCert(body: UploadRequest): Promise<Job> {
  return (await api.post<{ job: Job }>('/certs/upload', body)).job
}

export function updateCert(id: string, body: { name: string; autoRenew: boolean }): Promise<CertView> {
  return api.put<CertView>(`/certs/${encodeURIComponent(id)}`, body)
}

/** ACME certificates only; uploads answer cert.not_acme. */
export async function renewCert(id: string): Promise<Job> {
  return (await api.post<{ job: Job }>(`/certs/${encodeURIComponent(id)}/renew`)).job
}

/** Step-up protected; refused while sites or the panel use the certificate. */
export async function deleteCert(id: string): Promise<Job> {
  return (await api.delete<{ job: Job }>(`/certs/${encodeURIComponent(id)}`)).job
}

export function fetchAcmeSettings(): Promise<AcmeSettings> {
  return api.get<AcmeSettings>('/certs/settings')
}

/** Step-up protected. */
export async function saveAcmeSettings(body: AcmeSettingsRequest): Promise<void> {
  await api.put<unknown>('/certs/settings', body)
}

/** Serves a valid certificate for the panel itself; "" returns to the self-signed one (step-up). */
export async function setPanelCert(id: string): Promise<Job> {
  return (await api.put<{ job: Job }>('/certs/panel', { id })).job
}
