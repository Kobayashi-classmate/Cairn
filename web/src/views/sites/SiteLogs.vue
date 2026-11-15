<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NRadioButton, NRadioGroup, NSwitch, NTag, NText } from 'naive-ui'
import { isAbortError } from '@/api/client'
import { fetchSiteLog, type SiteLogKind } from '@/api/sites'
import { errorMessage } from '@/i18n/errors'
import { formatBytes, formatDateTime, formatNumber } from '@/i18n/format'

/** Polling interval while following the log. */
const POLL_MS = 2000
/** Lines kept in memory and shown; older ones are dropped. */
const MAX_LINES = 1000

const props = defineProps<{ code: string }>()

const { t } = useI18n()

const kind = ref<SiteLogKind>('access')
const follow = ref(true)
const lines = ref<string[]>([])
const error = ref<unknown>(null)
const loading = ref(false)
/** Raw lines of access log rows the user expanded. */
const expanded = ref(new Set<number>())
const box = ref<HTMLElement | null>(null)

/** Where the next read continues; null until the first read. */
let offset: number | null = null
/** Text after the last newline, completed by the next read. */
let partial = ''
let timer: ReturnType<typeof setTimeout> | undefined
let controller: AbortController | null = null
let unmounted = false

const byteLength = (text: string) => new TextEncoder().encode(text).length

function append(data: string) {
  const parts = (partial + data).split('\n')
  partial = parts.pop() ?? ''
  const next = [...lines.value, ...parts.filter((line) => line !== '')]
  lines.value = next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next
}

function reset() {
  clearTimeout(timer)
  controller?.abort()
  offset = null
  partial = ''
  lines.value = []
  expanded.value = new Set()
  error.value = null
}

async function read() {
  clearTimeout(timer)
  controller?.abort()
  const current = new AbortController()
  controller = current
  const requested = offset ?? -1
  if (offset === null) loading.value = true
  try {
    const chunk = await fetchSiteLog(props.code, kind.value, requested, current.signal)
    if (requested >= 0 && chunk.size < requested) {
      // The file was rotated: start again from its beginning.
      partial = ''
      offset = 0
      timer = setTimeout(() => void read(), 0)
      return
    }
    let data = chunk.data
    // The last part of a file usually starts in the middle of a line.
    if (requested < 0 && chunk.offset - byteLength(data) > 0) {
      const newline = data.indexOf('\n')
      data = newline >= 0 ? data.slice(newline + 1) : ''
    }
    append(data)
    offset = chunk.offset
    error.value = null
    scrollToEnd()
  } catch (err) {
    if (isAbortError(err)) return
    error.value = err
  } finally {
    if (controller === current) {
      controller = null
      loading.value = false
    }
  }
  schedule()
}

function schedule() {
  clearTimeout(timer)
  if (!unmounted && follow.value) timer = setTimeout(() => void read(), POLL_MS)
}

function scrollToEnd() {
  if (!follow.value) return
  void nextTick(() => {
    if (box.value) box.value.scrollTop = box.value.scrollHeight
  })
}

watch(
  kind,
  () => {
    reset()
    void read()
  },
  { immediate: true },
)

watch(follow, (on) => {
  if (on) void read()
  else clearTimeout(timer)
})

onBeforeUnmount(() => {
  unmounted = true
  clearTimeout(timer)
  controller?.abort()
})

/** One access log line; nginx writes them as JSON objects. */
interface AccessEntry {
  raw: string
  time?: string
  remote?: string
  host?: string
  method?: string
  uri?: string
  status?: number
  bytes?: number
  duration?: number
  upstream?: string
  referer?: string
  agent?: string
}

function parseAccess(raw: string): AccessEntry {
  try {
    const value: unknown = JSON.parse(raw)
    if (typeof value !== 'object' || value === null) return { raw }
    const v = value as Record<string, unknown>
    const text = (key: string) => (typeof v[key] === 'string' ? (v[key] as string) : undefined)
    const number = (key: string) => {
      const n = typeof v[key] === 'number' ? (v[key] as number) : Number(v[key])
      return Number.isFinite(n) ? n : undefined
    }
    return {
      raw,
      time: text('time'),
      remote: text('remote'),
      host: text('host'),
      method: text('method'),
      uri: text('uri'),
      status: number('status'),
      bytes: number('bytes'),
      duration: number('duration'),
      upstream: text('upstream'),
      referer: text('referer'),
      agent: text('agent'),
    }
  } catch {
    return { raw }
  }
}

const entries = computed(() => (kind.value === 'access' ? lines.value.map(parseAccess) : []))

function statusType(status?: number): 'success' | 'info' | 'warning' | 'error' | 'default' {
  if (status === undefined) return 'default'
  if (status >= 500) return 'error'
  if (status >= 400) return 'warning'
  if (status >= 300) return 'info'
  return 'success'
}

function timeText(time?: string): string {
  if (!time) return ''
  const ms = Date.parse(time)
  return Number.isNaN(ms) ? time : formatDateTime(Math.floor(ms / 1000))
}

function durationText(seconds?: number): string {
  return seconds === undefined ? '' : t('common.format.ms', { value: formatNumber(seconds * 1000) })
}

function toggleRaw(index: number) {
  const next = new Set(expanded.value)
  if (next.has(index)) next.delete(index)
  else next.add(index)
  expanded.value = next
}
</script>

<template>
  <div class="logs">
    <div class="logs__bar">
      <NRadioGroup v-model:value="kind" size="small">
        <NRadioButton value="access">{{ t('sites.logs.access') }}</NRadioButton>
        <NRadioButton value="error">{{ t('sites.logs.error') }}</NRadioButton>
      </NRadioGroup>
      <label class="logs__follow">
        <NSwitch v-model:value="follow" size="small" />
        <span>{{ t('sites.logs.follow') }}</span>
      </label>
    </div>

    <NAlert v-if="error" type="error" class="logs__block">{{ errorMessage(error) }}</NAlert>

    <div ref="box" class="logs__box">
      <NText v-if="!loading && lines.length === 0" depth="3" class="logs__empty">{{ t('sites.logs.empty') }}</NText>

      <table v-else-if="kind === 'access'" class="access">
        <thead>
          <tr>
            <th>{{ t('sites.logs.column.time') }}</th>
            <th>{{ t('sites.logs.column.remote') }}</th>
            <th>{{ t('sites.logs.column.request') }}</th>
            <th>{{ t('sites.logs.column.status') }}</th>
            <th>{{ t('sites.logs.column.bytes') }}</th>
            <th>{{ t('sites.logs.column.duration') }}</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="(entry, index) in entries" :key="index">
            <tr class="access__row" :title="entry.raw" @click="toggleRaw(index)">
              <template v-if="entry.status !== undefined || entry.uri">
                <td>{{ timeText(entry.time) }}</td>
                <td dir="ltr">{{ entry.remote }}</td>
                <td dir="ltr" class="access__request">
                  <span class="mono">{{ entry.method }} {{ entry.uri }}</span>
                  <NText v-if="entry.host" depth="3" class="access__host">{{ entry.host }}</NText>
                </td>
                <td>
                  <NTag size="small" :bordered="false" :type="statusType(entry.status)">{{ entry.status }}</NTag>
                </td>
                <td>{{ entry.bytes !== undefined ? formatBytes(entry.bytes) : '' }}</td>
                <td>{{ durationText(entry.duration) }}</td>
              </template>
              <td v-else colspan="6" class="mono" dir="ltr">{{ entry.raw }}</td>
            </tr>
            <tr v-if="expanded.has(index)">
              <td colspan="6" class="access__raw mono" dir="ltr">{{ entry.raw }}</td>
            </tr>
          </template>
        </tbody>
      </table>

      <pre v-else class="errors" dir="ltr">{{ lines.join('\n') }}</pre>
    </div>
  </div>
</template>

<style scoped>
.logs__bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 16px;
  margin-block-end: 12px;
}

.logs__follow {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}

.logs__block {
  margin-block-end: 12px;
}

.logs__box {
  max-block-size: min(65vh, 620px);
  overflow: auto;
  border-radius: 4px;
  background: rgb(128 128 128 / 8%);
}

.logs__empty {
  display: block;
  padding: 16px;
}

.access {
  inline-size: 100%;
  border-collapse: collapse;
  font-size: 12.5px;
}

.access th,
.access td {
  padding: 5px 8px;
  border-block-end: 1px solid rgb(128 128 128 / 15%);
  text-align: start;
  white-space: nowrap;
  vertical-align: top;
}

.access th {
  position: sticky;
  inset-block-start: 0;
  background: rgb(128 128 128 / 12%);
  font-weight: 500;
}

.access__row {
  cursor: pointer;
}

.access__request {
  max-inline-size: 420px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.access__host {
  display: block;
  font-size: 11px;
}

.access__raw {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.errors {
  margin: 0;
  padding: 10px 12px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
