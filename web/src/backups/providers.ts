// SPDX-License-Identifier: AGPL-3.0-only
//
// Common S3-compatible services, as hints for the storage form. Product
// names are not translated; the examples show the shape of the endpoint.

export interface StorageProvider {
  id: string
  /** Product name. */
  name: string
  /** Endpoint example (host[:port], no scheme). */
  endpoint: string
  /** Region example; empty when the service ignores it. */
  region: string
  /** The service wants the bucket in the path. */
  pathStyle: boolean
}

export const STORAGE_PROVIDERS: readonly StorageProvider[] = [
  { id: 'aws', name: 'Amazon S3', endpoint: 's3.us-east-1.amazonaws.com', region: 'us-east-1', pathStyle: false },
  { id: 'r2', name: 'Cloudflare R2', endpoint: 'ACCOUNT_ID.r2.cloudflarestorage.com', region: 'auto', pathStyle: false },
  { id: 'oss', name: 'Aliyun OSS', endpoint: 'oss-cn-hangzhou.aliyuncs.com', region: 'cn-hangzhou', pathStyle: false },
  { id: 'cos', name: 'Tencent COS', endpoint: 'cos.ap-guangzhou.myqcloud.com', region: 'ap-guangzhou', pathStyle: false },
  { id: 'obs', name: 'Huawei OBS', endpoint: 'obs.cn-north-4.myhuaweicloud.com', region: 'cn-north-4', pathStyle: false },
  { id: 'minio', name: 'MinIO', endpoint: 'minio.example.com:9000', region: 'us-east-1', pathStyle: true },
  { id: 'other', name: '', endpoint: 's3.example.com', region: '', pathStyle: false },
]

/** The provider an endpoint looks like, for showing the right hints when editing. */
export function providerOf(endpoint: string): string {
  const host = endpoint.toLowerCase()
  if (host.endsWith('.amazonaws.com')) return 'aws'
  if (host.endsWith('.r2.cloudflarestorage.com')) return 'r2'
  if (host.endsWith('.aliyuncs.com')) return 'oss'
  if (host.endsWith('.myqcloud.com')) return 'cos'
  if (host.endsWith('.myhuaweicloud.com')) return 'obs'
  return 'other'
}

/** host[:port] without a scheme or path. */
export function isEndpoint(value: string): boolean {
  return /^(\[[0-9a-fA-F:.]+\]|[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?)(:\d{1,5})?$/.test(value.trim())
}
