<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { LoaderCircle, Search, Trash2 } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import { deleteRedisKeys, fetchRedisValue, scanRedisKeys, setRedisTtl, type RedisKey, type RedisValue } from '@/api/databases'
import Card from '@/components/common/Card.vue'
import DataTable from '@/components/common/DataTable.vue'
import Notice from '@/components/common/Notice.vue'
import SelectField from '@/components/common/SelectField.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { keepOpenOnCompanion, keepOpenOnEscape, trackOverlay } from '@/components/common/overlayGuard'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { confirmAction } from '@/composables/useConfirmDialog'
import { errorMessage } from '@/i18n/errors'
import { formatDuration, formatNumber } from '@/i18n/format'
import { notifyError, notifySuccess } from '@/lib/notify'

// Browses the keys of the system Redis/Valkey instance: pick a database
// number and a pattern, scan, open a key to see its value and change its
// expiry, select keys to delete them (step-up protected).
defineProps<{ canManage: boolean }>()

const { t } = useI18n()

const db = ref('0')
const pattern = ref('*')
const keys = ref<RedisKey[]>([])
/** "0" once the scan reached the end; null before the first scan. */
const cursor = ref<string | null>(null)
const scanning = ref(false)
const scanError = ref<unknown>(null)
const selection = ref<string[]>([])

const value = ref<RedisValue | null>(null)
const ttl = ref('0')
const savingTtl = ref(false)
const ttlError = ref<unknown>(null)
const opening = ref<string | null>(null)

const showValue = computed(() => value.value !== null)
trackOverlay(() => showValue.value)

const dbOptions = Array.from({ length: 16 }, (_, index) => ({ value: String(index), label: `db${index}` }))

/** The database and pattern the listed keys come from; "Load more" continues that scan. */
const scanned = ref<{ db: number; pattern: string } | null>(null)
/** Numbers the scans, so an answer for an earlier scan is dropped. */
let seq = 0

async function scan(reset: boolean) {
  const target = reset || !scanned.value ? { db: Number(db.value), pattern: pattern.value.trim() || '*' } : scanned.value
  const mine = ++seq
  scanning.value = true
  scanError.value = null
  if (reset) {
    scanned.value = target
    keys.value = []
    cursor.value = null
    selection.value = []
  }
  try {
    const page = await scanRedisKeys(target.db, target.pattern, reset ? '0' : (cursor.value ?? '0'))
    if (mine !== seq) return
    keys.value = reset ? page.keys : [...keys.value, ...page.keys.filter((k) => !keys.value.some((known) => known.key === k.key))]
    cursor.value = page.cursor
  } catch (err) {
    if (mine === seq) scanError.value = err
  } finally {
    if (mine === seq) scanning.value = false
  }
}

function onDb(next: string) {
  db.value = next
  void scan(true)
}

async function open(key: RedisKey) {
  opening.value = key.key
  ttlError.value = null
  try {
    value.value = await fetchRedisValue(scanned.value?.db ?? Number(db.value), key.key)
    ttl.value = String(value.value.ttl > 0 ? value.value.ttl : 0)
  } catch (err) {
    notifyError(err)
  } finally {
    opening.value = null
  }
}

async function saveTtl() {
  const current = value.value
  const seconds = Number(ttl.value)
  if (!current || !Number.isInteger(seconds) || seconds < 0) return
  savingTtl.value = true
  ttlError.value = null
  try {
    // Step-up protected; 0 removes the expiry.
    const number = scanned.value?.db ?? Number(db.value)
    await setRedisTtl(number, current.key, seconds)
    notifySuccess(t('databases.keys.ttlSaved'))
    value.value = await fetchRedisValue(number, current.key)
    const listed = keys.value.find((k) => k.key === current.key)
    if (listed && value.value) listed.ttl = value.value.ttl
  } catch (err) {
    if (!isStepUpRequired(err)) ttlError.value = err
  } finally {
    savingTtl.value = false
  }
}

async function removeSelected(list: string[], clear: () => void) {
  if (list.length === 0 || !scanned.value) return
  const number = scanned.value.db
  const ok = await confirmAction({
    title: t('databases.keys.deleteTitle', { count: list.length }, list.length),
    description: t('databases.keys.deleteContent', { db: `db${number}` }),
    confirmLabel: t('databases.keys.delete'),
    destructive: true,
    run: async () => {
      const deleted = await deleteRedisKeys(number, list)
      notifySuccess(t('databases.keys.deleted', { count: deleted }, deleted))
      keys.value = keys.value.filter((k) => !list.includes(k.key))
    },
  })
  if (ok) clear()
}

function ttlText(seconds: number): string {
  return seconds < 0 ? t('databases.keys.noExpiry') : formatDuration(seconds)
}

/** zset and hash items come as pairs; everything else one per row. */
const pairs = computed(() => value.value?.type === 'zset' || value.value?.type === 'hash')
const rows = computed(() => {
  const items = value.value?.items ?? []
  if (!pairs.value) return items.map((item) => [item])
  const out: string[][] = []
  for (let i = 0; i < items.length; i += 2) out.push([items[i] ?? '', items[i + 1] ?? ''])
  return out
})

const columns = computed<ColumnDef<RedisKey>[]>(() => [
  { id: 'key', header: t('databases.keys.key'), accessorFn: (k) => k.key, meta: { class: 'min-w-[260px]' } },
  { id: 'type', header: t('common.field.type'), accessorFn: (k) => k.type, meta: { class: 'w-[110px]' } },
  { id: 'ttl', header: t('databases.keys.ttl'), accessorFn: (k) => (k.ttl < 0 ? Number.MAX_SAFE_INTEGER : k.ttl), meta: { class: 'w-[160px]' } },
])

function closeValue(open: boolean) {
  if (!open) value.value = null
}
</script>

<template>
  <Card :level="3" flush>
    <form class="flex flex-wrap items-end gap-2 border-b px-4 py-3 md:px-[18px]" @submit.prevent="scan(true)">
      <SelectField
        :model-value="db"
        :options="dbOptions"
        :aria-label="t('common.field.db')"
        :disabled="scanning"
        class="w-28 font-mono max-md:h-11"
        @update:model-value="onDb"
      />
      <Input
        v-model="pattern"
        :aria-label="t('databases.keys.pattern')"
        :placeholder="t('databases.keys.pattern')"
        autocomplete="off"
        spellcheck="false"
        autocapitalize="none"
        class="min-w-40 flex-[1_1_220px] font-mono sm:max-w-80 max-md:h-11"
      />
      <Button type="submit" class="max-md:h-11" :disabled="scanning">
        <LoaderCircle v-if="scanning" class="animate-spin" aria-hidden="true" />
        <Search v-else aria-hidden="true" />
        {{ t('databases.keys.scan') }}
      </Button>
    </form>

    <Notice v-if="scanError" tone="destructive" class="m-4">{{ errorMessage(scanError) }}</Notice>
    <p v-if="cursor === null && !scanning" class="m-0 px-4 py-6 text-center text-detail text-muted-foreground md:px-[18px]">{{ t('databases.keys.intro') }}</p>
    <DataTable
      v-else
      v-model:selection="selection"
      :data="keys"
      :columns="columns"
      :row-key="(k) => k.key"
      :label="t('databases.tabs.keys')"
      :loading="scanning && keys.length === 0"
      :selectable="canManage"
      :empty-title="t('databases.keys.empty')"
      :min-width="560"
      flat
    >
      <template #cell-key="{ row }">
        <button
          type="button"
          class="inline-flex max-w-full items-center gap-1.5 rounded-sm text-start font-mono text-[12.5px] underline decoration-border underline-offset-[3px] outline-none hover:decoration-current focus-visible:ring-2 focus-visible:ring-ring/50"
          dir="ltr"
          :disabled="opening !== null"
          @click="open(row)"
        >
          <span class="truncate">{{ row.key }}</span>
          <LoaderCircle v-if="opening === row.key" class="size-3.5 shrink-0 animate-spin" aria-hidden="true" />
        </button>
      </template>
      <template #cell-type="{ row }"><StatusPill class="font-mono">{{ row.type }}</StatusPill></template>
      <template #cell-ttl="{ row }">
        <span :class="row.ttl < 0 ? 'text-muted-foreground' : ''">{{ ttlText(row.ttl) }}</span>
      </template>
      <template #bulk="{ keys: picked, clear }">
        <Button size="sm" variant="outline" class="text-destructive hover:text-destructive" @click="removeSelected([...picked], clear)">
          <Trash2 aria-hidden="true" />
          {{ t('databases.keys.deleteSelected', { count: picked.length }) }}
        </Button>
      </template>
    </DataTable>
    <div v-if="cursor && cursor !== '0'" class="flex justify-center border-t px-4 py-3">
      <Button variant="outline" size="sm" :disabled="scanning" @click="scan(false)">
        <LoaderCircle v-if="scanning" class="animate-spin" aria-hidden="true" />
        {{ t('common.action.loadMore') }}
      </Button>
    </div>
  </Card>

  <Dialog :open="showValue" @update:open="closeValue">
    <DialogContent class="sm:max-w-[720px]" @interact-outside="keepOpenOnCompanion" @escape-key-down="keepOpenOnEscape">
      <template v-if="value">
        <div class="flex min-w-0 flex-col gap-1.5 pe-6">
          <DialogTitle class="m-0 font-mono text-base font-semibold [overflow-wrap:anywhere]" dir="ltr">{{ value.key }}</DialogTitle>
          <DialogDescription class="m-0 flex flex-wrap items-center gap-x-3 gap-y-1 text-detail text-muted-foreground">
            <StatusPill class="font-mono">{{ value.type }}</StatusPill>
            <span>{{ t('databases.keys.length', { count: formatNumber(value.length) }) }}</span>
            <span>{{ ttlText(value.ttl) }}</span>
          </DialogDescription>
        </div>
        <pre
          v-if="value.value !== undefined && !value.items"
          class="m-0 max-h-[50vh] overflow-auto rounded-md bg-muted px-3 py-2.5 font-mono text-[12.5px] whitespace-pre-wrap [overflow-wrap:anywhere]"
          dir="ltr"
        >{{ value.value }}</pre>
        <div v-else class="max-h-[50vh] overflow-auto rounded-md border">
          <table class="w-full border-collapse font-mono text-[12.5px]" dir="ltr">
            <tbody>
              <tr v-for="(row, index) in rows" :key="index" class="border-t first:border-t-0">
                <td v-for="(cell, cellIndex) in row" :key="cellIndex" class="px-3 py-1.5 align-top [overflow-wrap:anywhere]">{{ cell }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-if="value.truncated" class="m-0 text-xs text-muted-foreground">{{ t('databases.keys.truncated') }}</p>
        <form v-if="canManage" class="flex flex-wrap items-end gap-2" novalidate @submit.prevent="saveTtl">
          <label class="flex flex-col gap-1.5 text-detail font-medium">
            {{ t('databases.keys.ttlSeconds') }}
            <Input v-model="ttl" type="number" min="0" step="1" inputmode="numeric" class="w-40 tabular-nums" />
          </label>
          <Button type="submit" variant="outline" class="max-md:h-11" :disabled="savingTtl">
            <LoaderCircle v-if="savingTtl" class="animate-spin" aria-hidden="true" />
            {{ t('databases.keys.setTtl') }}
          </Button>
          <span class="basis-full text-xs text-muted-foreground">{{ t('databases.keys.ttlHint') }}</span>
        </form>
        <Notice v-if="ttlError" tone="destructive">{{ errorMessage(ttlError) }}</Notice>
      </template>
    </DialogContent>
  </Dialog>
</template>
