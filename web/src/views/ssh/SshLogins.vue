<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { History, RefreshCw } from '@lucide/vue'
import { fetchSshLogins, type LoginResult, type SshLogin } from '@/api/ssh'
import DataTable from '@/components/common/DataTable.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import { notifyError } from '@/lib/notify'
import type { Tone } from '@/lib/tone'

// SSH sign-ins, accepted and refused, newest first; older ones page in with
// "Load older". Shared by the SSH page and the Logs page.
const PAGE = 100

/** A sign-in with a key of its own (the same user can fail twice in one second). */
type Row = SshLogin & { key: string }

const { t, te } = useI18n()
const entries = ref<Row[]>([])
let nextKey = 0

function keyed(list: SshLogin[]): Row[] {
  return list.map((entry) => ({ ...entry, key: String(nextKey++) }))
}
const next = ref('')
const loading = ref(false)
const loadingMore = ref(false)
const loaded = ref(false)
const loadError = ref<unknown>(null)
const search = ref('')

async function load() {
  loading.value = true
  try {
    const page = await fetchSshLogins(PAGE)
    entries.value = keyed(page.entries)
    next.value = page.next
    loadError.value = null
    loaded.value = true
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

async function loadOlder() {
  if (!next.value) return
  loadingMore.value = true
  try {
    const page = await fetchSshLogins(PAGE, next.value)
    entries.value = entries.value.concat(keyed(page.entries))
    next.value = page.next
  } catch (err) {
    notifyError(err)
  } finally {
    loadingMore.value = false
  }
}

onMounted(() => void load())

function label(prefix: string, value: string): string {
  const key = `${prefix}.${value}`
  return value && te(key) ? t(key) : value
}

const RESULT_TONES: Record<LoginResult, Tone> = { accepted: 'success', failed: 'destructive', invalid_user: 'warning' }

const columns = computed<ColumnDef<Row>[]>(() => [
  { id: 'time', header: t('ssh.logins.time'), accessorFn: (entry) => entry.time, meta: { class: 'w-[150px]' } },
  { id: 'user', header: t('common.field.user'), accessorFn: (entry) => entry.user, meta: { class: 'w-[160px]' } },
  { id: 'ip', header: t('ssh.logins.ip'), accessorFn: (entry) => entry.ip, meta: { class: 'min-w-[160px]' } },
  { id: 'method', header: t('ssh.logins.method'), accessorFn: (entry) => entry.method, meta: { class: 'w-[170px]' } },
  { id: 'result', header: t('ssh.logins.result'), accessorFn: (entry) => entry.result, meta: { class: 'w-[140px]' } },
])

function searchText(entry: SshLogin): string {
  return [entry.user, entry.ip, label('ssh.method', entry.method), label('ssh.result', entry.result)].join(' ')
}
</script>

<template>
  <div class="flex min-w-0 flex-col gap-3">
    <DataTable
      v-model:search="search"
      :data="entries"
      :columns="columns"
      :row-key="(entry) => entry.key"
      :label="t('ssh.tabs.logins')"
      :loading="loading && !loaded"
      :error="loadError"
      :search-placeholder="t('ssh.logins.search')"
      :search-text="searchText"
      :empty-title="t('ssh.logins.empty')"
      :min-width="720"
      :initial-sort="[{ id: 'time', desc: true }]"
      @retry="load"
    >
      <template #toolbar>
        <Button variant="ghost" size="sm" class="max-md:h-11" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" aria-hidden="true" />
          {{ t('common.action.refresh') }}
        </Button>
      </template>
      <template #cell-time="{ row }">
        <RelativeTime :value="row.time" class="text-muted-foreground" />
      </template>
      <template #cell-user="{ row }">
        <span class="font-mono text-[12.5px]" dir="ltr">{{ row.user || '—' }}</span>
      </template>
      <template #cell-ip="{ row }">
        <span class="font-mono text-[12.5px]" dir="ltr">{{ row.ip }}</span>
      </template>
      <template #cell-method="{ row }">
        <span :class="row.method ? '' : 'text-muted-foreground'">{{ label('ssh.method', row.method) || '—' }}</span>
      </template>
      <template #cell-result="{ row }">
        <StatusPill :tone="RESULT_TONES[row.result] ?? 'neutral'">{{ label('ssh.result', row.result) }}</StatusPill>
      </template>
    </DataTable>
    <div v-if="next && loaded" class="flex justify-center">
      <Button variant="outline" class="max-md:h-11" :disabled="loadingMore" @click="loadOlder">
        <History aria-hidden="true" />
        {{ t('ssh.logins.older') }}
      </Button>
    </div>
  </div>
</template>
