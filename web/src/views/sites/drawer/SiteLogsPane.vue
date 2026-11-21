<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isAbortError } from '@/api/client'
import { fetchSiteLog, type SiteLogKind } from '@/api/sites'
import Card from '@/components/common/Card.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { formatBytes, formatLogTime, formatNumber } from '@/i18n/format'
import { cn } from '@/lib/utils'

// The site's nginx access and error logs, followed while open. Access log
// lines are JSON (one request each) and shown as a table coloured by the
// status code; error log lines are shown as they are.
const POLL_MS = 2000
const MAX_LINES = 1000

const props = defineProps<{ code: string }>()

const { t } = useI18n()

const kind = ref<SiteLogKind>('access')
const follow = ref(true)
const lines = ref<string[]>([])
const error = ref<unknown>(null)
const loading = ref(false)
/** Rows whose raw line is shown. */
const expanded = ref(new Set<number>())
const box = ref<HTMLElement | null>(null)

/** Where the next read continues; null until the first read. */
let offset: number | null = null
/** Text after the last newline, completed by the next read. */
let partial = ''
let timer: ReturnType<typeof setTimeout> | undefined
let controller: AbortController | null = null
let unmounted = false

const kinds = computed(() => [
  { value: 'access' as const, label: t('sites.logs.access') },
  { value: 'error' as const, label: t('sites.logs.error') },
])

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

function retry() {
  reset()
  void read()
}

watch(kind, retry, { immediate: true })

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
    }
  } catch {
    return { raw }
  }
}

const entries = computed(() => (kind.value === 'access' ? lines.value.map(parseAccess) : []))

function statusClass(status?: number): string {
  if (status === undefined) return 'text-muted-foreground'
  if (status >= 500) return 'text-destructive'
  if (status >= 400) return 'text-warning'
  if (status >= 300) return 'text-muted-foreground'
  return 'text-success'
}

function timeText(time?: string): string {
  if (!time) return ''
  const ms = Date.parse(time)
  return Number.isNaN(ms) ? time : formatLogTime(ms)
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
  <Card :level="3" flush>
    <template #actions>
      <SegmentedControl v-model="kind" :options="kinds" :label="t('sites.logs.kind')" class="w-56" />
      <label class="inline-flex cursor-pointer items-center gap-2 text-detail">
        <Switch v-model="follow" :aria-label="t('sites.logs.follow')" />
        {{ t('sites.logs.follow') }}
      </label>
    </template>

    <ErrorState v-if="error && lines.length === 0" :error="error" compact @retry="retry" />
    <div v-else-if="loading && lines.length === 0" class="flex flex-col gap-2 px-4 py-4" aria-hidden="true">
      <Skeleton v-for="row in 5" :key="row" class="h-3.5" :style="{ width: `${90 - row * 8}%` }" />
    </div>
    <div v-else ref="box" class="max-h-[min(65vh,620px)] overflow-auto">
      <p v-if="lines.length === 0" class="m-0 px-4 py-4 text-detail text-muted-foreground">{{ t('sites.logs.empty') }}</p>

      <table v-else-if="kind === 'access'" class="w-full min-w-[640px] border-collapse font-mono text-[12.5px]">
        <thead class="sticky top-0 bg-muted text-start">
          <tr class="text-caption font-sans text-muted-foreground">
            <th scope="col" class="px-4 py-2 text-start font-medium">{{ t('sites.logs.column.time') }}</th>
            <th scope="col" class="px-2 py-2 text-start font-medium">{{ t('sites.logs.column.status') }}</th>
            <th scope="col" class="px-2 py-2 text-start font-medium">{{ t('sites.logs.column.request') }}</th>
            <th scope="col" class="px-2 py-2 text-end font-medium">{{ t('sites.logs.column.bytes') }}</th>
            <th scope="col" class="px-2 py-2 text-end font-medium">{{ t('sites.logs.column.duration') }}</th>
            <th scope="col" class="px-4 py-2 text-start font-medium">{{ t('sites.logs.column.remote') }}</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="(entry, index) in entries" :key="index">
            <tr class="border-t">
              <template v-if="entry.status !== undefined || entry.uri">
                <td class="px-4 py-1.5 whitespace-nowrap text-muted-foreground">{{ timeText(entry.time) }}</td>
                <td :class="cn('px-2 py-1.5 font-medium', statusClass(entry.status))">{{ entry.status }}</td>
                <td class="max-w-[420px] px-2 py-1.5" dir="ltr">
                  <button
                    type="button"
                    class="block w-full truncate rounded-sm text-start outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50"
                    :aria-expanded="expanded.has(index)"
                    :title="entry.host ? `${entry.host}${entry.uri ?? ''}` : entry.uri"
                    @click="toggleRaw(index)"
                  >
                    {{ entry.method }} {{ entry.uri }}
                  </button>
                </td>
                <td class="px-2 py-1.5 text-end whitespace-nowrap text-muted-foreground">
                  {{ entry.bytes !== undefined ? formatBytes(entry.bytes) : '' }}
                </td>
                <td class="px-2 py-1.5 text-end whitespace-nowrap text-muted-foreground">{{ durationText(entry.duration) }}</td>
                <td class="px-4 py-1.5 whitespace-nowrap text-muted-foreground" dir="ltr">{{ entry.remote }}</td>
              </template>
              <td v-else colspan="6" class="px-4 py-1.5 whitespace-pre-wrap [overflow-wrap:anywhere]" dir="ltr">{{ entry.raw }}</td>
            </tr>
            <tr v-if="expanded.has(index)" class="bg-muted/50">
              <td colspan="6" class="px-4 py-2 whitespace-pre-wrap [overflow-wrap:anywhere]" dir="ltr">{{ entry.raw }}</td>
            </tr>
          </template>
        </tbody>
      </table>

      <pre v-else class="m-0 px-4 py-3 font-mono text-xs leading-relaxed whitespace-pre-wrap [overflow-wrap:anywhere]" dir="ltr">{{
        lines.join('\n')
      }}</pre>
    </div>
  </Card>
</template>
