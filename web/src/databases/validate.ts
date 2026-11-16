// SPDX-License-Identifier: AGPL-3.0-only
//
// Client-side mirrors of the database name, password and host rules; the
// server checks again (and also refuses reserved names).
import type { EngineKind } from '@/api/databases'

const MYSQL_DB = /^[A-Za-z0-9_]{1,64}$/
const MYSQL_USER = /^[A-Za-z0-9_]{1,32}$/
const PG_NAME = /^[a-z_][a-z0-9_]{0,62}$/
const PASSWORD = /^[A-Za-z0-9!#%*+,.:=?@^_~-]{8,128}$/
const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/

export function isDbName(kind: EngineKind, name: string): boolean {
  return kind === 'postgresql' ? PG_NAME.test(name) : MYSQL_DB.test(name)
}

export function isDbUser(kind: EngineKind, name: string): boolean {
  return kind === 'postgresql' ? PG_NAME.test(name) : MYSQL_USER.test(name)
}

/** 8 to 128 characters from letters, digits and !#%*+,.:=?@^_~- */
export function isDbPassword(password: string): boolean {
  return PASSWORD.test(password)
}

/** localhost, % (anywhere), an IPv4 address or an IPv4 CIDR range. */
export function isDbHost(host: string): boolean {
  if (host === 'localhost' || host === '%') return true
  const [address = '', prefix, extra] = host.split('/')
  if (extra !== undefined || !IPV4.test(address)) return false
  return prefix === undefined || (/^\d{1,2}$/.test(prefix) && Number(prefix) <= 32)
}

/** Dump files the server accepts as uploads: .sql and .sql.gz, and .dump for PostgreSQL. */
export function isUploadableDump(kind: EngineKind, fileName: string): boolean {
  if (!/^[A-Za-z0-9_.-]{1,160}\.(sql|sql\.gz|dump)$/.test(fileName)) return false
  return kind === 'postgresql' || !fileName.endsWith('.dump')
}
