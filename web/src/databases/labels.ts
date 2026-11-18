// SPDX-License-Identifier: AGPL-3.0-only
import type { DbEngine } from '@/api/databases'

/** Product names of the database servers (not translated). */
const PRODUCT_NAMES: Record<string, string> = {
  mariadb: 'MariaDB',
  mysql: 'MySQL',
  postgresql: 'PostgreSQL',
  redis: 'Redis',
  valkey: 'Valkey',
}

/** Product name of a database server's catalog id ("postgresql" gives "PostgreSQL"). */
export function productName(id: string): string {
  return PRODUCT_NAMES[id] ?? id
}

/** "MariaDB 11.8", "PostgreSQL 17", "Redis 8.0". */
export function engineLabel(engine: Pick<DbEngine, 'software' | 'line'>): string {
  const name = PRODUCT_NAMES[engine.software] ?? engine.software
  return engine.line ? `${name} ${engine.line}` : name
}

/**
 * Name for an engine ID as jobs and audit entries carry it ("mysql",
 * "postgresql-17", "redis"). The ID alone cannot tell MySQL from MariaDB or
 * Redis from Valkey, so both are named.
 */
export function engineIdLabel(id: string): string {
  if (id === 'mysql') return 'MySQL/MariaDB'
  if (id === 'redis') return 'Redis/Valkey'
  const pg = /^postgresql-(\d+)$/.exec(id)
  return pg ? `PostgreSQL ${pg[1]}` : id
}
