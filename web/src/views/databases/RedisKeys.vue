<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NDataTable,
  NEmpty,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NTag,
  NText,
  useMessage,
  type DataTableColumns,
  type DataTableRowKey,
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import {
  deleteRedisKeys,
  fetchRedisValue,
  scanRedisKeys,
  setRedisTtl,
  type RedisKey,
  type RedisValue,
} from '@/api/databases'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { errorMessage } from '@/i18n/errors'
import { formatDuration, formatNumber } from '@/i18n/format'

const props = defineProps<{ canManage: boolean }>()

const { t } = useI18n()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()

const db = ref(0)
const pattern = ref('*')
const keys = ref<RedisKey[]>([])
/** "0" once the scan reached the end. */
const cursor = ref<string | null>(null)
const scanning = ref(false)
const scanError = ref<unknown>(null)
const checked = ref<DataTableRowKey[]>([])

const value = ref<RedisValue | null>(null)
const valueError = ref<unknown>(null)
const ttlSeconds = ref<number | null>(null)
const savingTtl = ref(false)

const dbOptions = Array.from({ length: 16 }, (_, index) => ({ value: index, label: `db${index}` }))

async function scan(reset: boolean) {
  scanning.value = true
  scanError.value = null
  try {
    const page = await scanRedisKeys(db.value, pattern.value.trim() || '*', reset ? '0' : (cursor.value ?? '0'))
    keys.value = reset ? page.keys : [...keys.value, ...page.keys]
    cursor.value = page.cursor
    if (reset) checked.value = []
  } catch (err) {
    scanError.value = err
  } finally {
    scanning.value = false
  }
}

async function open(key: RedisKey) {
  valueError.value = null
  try {
    value.value = await fetchRedisValue(db.value, key.key)
    ttlSeconds.value = value.value.ttl > 0 ? value.value.ttl : 0
  } catch (err) {
    valueError.value = err
    showError(err)
  }
}

async function saveTtl() {
  const current = value.value
  if (!current || ttlSeconds.value === null) return
  savingTtl.value = true
  try {
    // Step-up protected; 0 removes the expiry.
    await setRedisTtl(db.value, current.key, ttlSeconds.value)
    message.success(t('settings.panel.saved'))
    value.value = await fetchRedisValue(db.value, current.key)
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  } finally {
    savingTtl.value = false
  }
}

async function removeChecked() {
  const list = checked.value.map(String)
  if (list.length === 0) return
  const ok = await confirm({
    title: t('databases.keys.deleteTitle', { count: list.length }, list.length),
    content: t('databases.keys.deleteContent'),
    confirmText: t('common.action.delete'),
    danger: true,
  })
  if (!ok) return
  try {
    const deleted = await deleteRedisKeys(db.value, list)
    message.success(t('databases.keys.deleted', { count: deleted }, deleted))
    keys.value = keys.value.filter((k) => !list.includes(k.key))
    checked.value = []
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  }
}

function ttlText(ttl: number): string {
  return ttl < 0 ? t('databases.keys.noExpiry') : formatDuration(ttl)
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

const columns = computed<DataTableColumns<RedisKey>>(() => [
  ...(props.canManage ? [{ type: 'selection' as const }] : []),
  {
    key: 'key',
    title: t('databases.keys.key'),
    minWidth: 240,
    render: (k) => h(NButton, { text: true, onClick: () => void open(k) }, () => h('span', { class: 'mono', dir: 'ltr' }, k.key)),
  },
  { key: 'type', title: t('common.field.type'), width: 100, render: (k) => h(NTag, { size: 'small', bordered: false }, () => k.type) },
  { key: 'ttl', title: t('databases.keys.ttl'), width: 160, render: (k) => ttlText(k.ttl) },
])
</script>

<template>
  <div>
    <div class="toolbar">
      <NSelect v-model:value="db" :options="dbOptions" class="toolbar__db" @update:value="() => scan(true)" />
      <NInput
        v-model:value="pattern"
        :placeholder="t('databases.keys.pattern')"
        :input-props="{ spellcheck: false, 'aria-label': t('databases.keys.pattern') }"
        class="toolbar__pattern"
        @keyup.enter="scan(true)"
      />
      <NButton type="primary" :loading="scanning" @click="scan(true)">{{ t('databases.keys.scan') }}</NButton>
      <NButton v-if="canManage" type="error" ghost :disabled="checked.length === 0" @click="removeChecked">
        {{ t('databases.keys.deleteSelected', { count: checked.length }) }}
      </NButton>
    </div>
    <NAlert v-if="scanError" type="error" class="block">{{ errorMessage(scanError) }}</NAlert>
    <NDataTable
      v-if="cursor !== null && keys.length > 0"
      v-model:checked-row-keys="checked"
      :columns="columns"
      :data="keys"
      :row-key="(k: RedisKey) => k.key"
      :scroll-x="600"
      size="small"
    />
    <NEmpty v-else-if="cursor !== null" :description="t('databases.keys.empty')" class="empty" />
    <NText v-else depth="3" tag="p" class="intro">{{ t('databases.keys.intro') }}</NText>
    <div v-if="cursor && cursor !== '0'" class="more">
      <NButton :loading="scanning" @click="scan(false)">{{ t('common.action.loadMore') }}</NButton>
    </div>

    <NModal
      :show="value !== null"
      preset="card"
      :title="value?.key ?? ''"
      class="value-dialog"
      @update:show="(open: boolean) => !open && (value = null)"
    >
      <template v-if="value">
        <div class="meta">
          <NTag size="small" :bordered="false">{{ value.type }}</NTag>
          <NText depth="3">{{ t('databases.keys.length', { count: formatNumber(value.length) }) }}</NText>
          <NText depth="3">{{ ttlText(value.ttl) }}</NText>
        </div>
        <pre v-if="value.value !== undefined && !value.items" class="value" dir="ltr">{{ value.value }}</pre>
        <table v-else class="items">
          <tbody>
            <tr v-for="(row, index) in rows" :key="index">
              <td v-for="(cell, cellIndex) in row" :key="cellIndex" class="mono" dir="ltr">{{ cell }}</td>
            </tr>
          </tbody>
        </table>
        <NText v-if="value.truncated" depth="3" tag="p" class="hint">{{ t('databases.keys.truncated') }}</NText>
        <div v-if="canManage" class="ttl">
          <NInputNumber v-model:value="ttlSeconds" :min="0" :show-button="false" class="ttl__input" />
          <NButton :loading="savingTtl" @click="saveTtl">{{ t('databases.keys.setTtl') }}</NButton>
          <NText depth="3" class="hint">{{ t('databases.keys.ttlHint') }}</NText>
        </div>
      </template>
    </NModal>
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-block-end: 12px;
}

.toolbar__db {
  inline-size: 100px;
}

.toolbar__pattern {
  inline-size: 240px;
}

.block {
  margin-block-end: 12px;
}

.intro {
  margin: 0;
  font-size: 13px;
}

.empty {
  padding-block: 24px;
}

.more {
  display: flex;
  justify-content: center;
  margin-block-start: 12px;
}

.value-dialog {
  inline-size: min(760px, calc(100vw - 32px));
}

.meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 16px;
  margin-block-end: 12px;
}

.value,
.items {
  max-block-size: 50vh;
  overflow: auto;
}

.value {
  margin: 0;
  padding: 10px 12px;
  border-radius: 4px;
  background: rgb(128 128 128 / 10%);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12.5px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.items {
  display: block;
  border-collapse: collapse;
  font-size: 12.5px;
}

.items td {
  padding: 4px 8px;
  border-block-end: 1px solid rgb(128 128 128 / 15%);
  vertical-align: top;
  overflow-wrap: anywhere;
}

.hint {
  font-size: 12px;
}

.ttl {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-block-start: 16px;
}

.ttl__input {
  inline-size: 160px;
}

.mono,
:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
