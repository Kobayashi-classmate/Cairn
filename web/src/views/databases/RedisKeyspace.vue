<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { DbEngine, RedisKeyspace } from '@/api/databases'
import DataTable from '@/components/common/DataTable.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { formatNumber } from '@/i18n/format'

// How many keys each database of the system Redis/Valkey instance holds,
// for those who may not browse the keys (their names hold session IDs too).
const props = defineProps<{ engine: DbEngine }>()

const { t } = useI18n()

const rows = computed<RedisKeyspace[]>(() => props.engine.redis?.keyspace ?? [])

const columns = computed<ColumnDef<RedisKeyspace>[]>(() => [
  { id: 'db', header: t('common.field.db'), accessorFn: (k) => k.db, meta: { class: 'w-[120px]' } },
  { id: 'keys', header: t('databases.keyspace.keys'), accessorFn: (k) => k.keys, meta: { class: 'min-w-[140px]' } },
  { id: 'expires', header: t('databases.keyspace.expires'), accessorFn: (k) => k.expires, meta: { class: 'min-w-[140px]' } },
])
</script>

<template>
  <p class="m-0 text-detail text-muted-foreground">{{ t('databases.keyspace.intro') }}</p>
  <DataTable
    :data="rows"
    :columns="columns"
    :row-key="(k) => String(k.db)"
    :label="t('databases.tabs.keys')"
    :empty-title="t('databases.keyspace.empty')"
    :min-width="420"
    :initial-sort="[{ id: 'db', desc: false }]"
  >
    <template #cell-db="{ row }"><span class="font-mono">db{{ row.db }}</span></template>
    <template #cell-keys="{ row }"><span class="tabular-nums">{{ formatNumber(row.keys) }}</span></template>
    <template #cell-expires="{ row }"><span class="tabular-nums">{{ formatNumber(row.expires) }}</span></template>
  </DataTable>
</template>
