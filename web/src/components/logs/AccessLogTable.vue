<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatBytes, formatLogTime, formatNumber } from '@/i18n/format'
import { isRequest, parseAccess, statusTone } from '@/logs/access'
import { TONE_TEXT } from '@/lib/tone'
import { cn } from '@/lib/utils'

// A site's access log as a table, one request a row, the status code in
// the colour of its class (2xx fine, 3xx quiet, 4xx warning, 5xx bad).
// Clicking a request shows the raw line. Rendered inside a LogView.
const props = defineProps<{ lines: readonly string[]; wrap: boolean }>()

const { t } = useI18n()
/** Raw lines whose row is open. */
const expanded = ref(new Set<string>())

const entries = computed(() => props.lines.map(parseAccess))

// Lines that scrolled out of the kept window are no longer open.
watch(
  () => props.lines,
  (lines) => {
    if (expanded.value.size === 0) return
    const kept = new Set(lines)
    expanded.value = new Set([...expanded.value].filter((raw) => kept.has(raw)))
  },
)

function toggle(raw: string) {
  const next = new Set(expanded.value)
  if (next.has(raw)) next.delete(raw)
  else next.add(raw)
  expanded.value = next
}

function timeText(time?: string): string {
  if (!time) return ''
  const ms = Date.parse(time)
  return Number.isNaN(ms) ? time : formatLogTime(ms)
}

function durationText(seconds?: number): string {
  return seconds === undefined ? '' : t('common.format.ms', { value: formatNumber(seconds * 1000) })
}
</script>

<template>
  <table class="w-full min-w-[640px] border-collapse font-mono text-[12.5px]">
    <thead class="sticky top-0 z-[1] bg-muted">
      <tr class="font-sans text-caption text-muted-foreground">
        <th scope="col" class="px-3 py-2 text-start font-medium">{{ t('sites.logs.column.time') }}</th>
        <th scope="col" class="px-2 py-2 text-start font-medium">{{ t('sites.logs.column.status') }}</th>
        <th scope="col" class="px-2 py-2 text-start font-medium">{{ t('sites.logs.column.request') }}</th>
        <th scope="col" class="px-2 py-2 text-end font-medium">{{ t('sites.logs.column.bytes') }}</th>
        <th scope="col" class="px-2 py-2 text-end font-medium">{{ t('sites.logs.column.duration') }}</th>
        <th scope="col" class="px-3 py-2 text-start font-medium">{{ t('sites.logs.column.remote') }}</th>
      </tr>
    </thead>
    <tbody>
      <template v-for="(entry, index) in entries" :key="index">
        <tr class="border-t hover:bg-accent/40">
          <template v-if="isRequest(entry)">
            <td class="px-3 py-1.5 align-top whitespace-nowrap text-muted-foreground tabular-nums">{{ timeText(entry.time) }}</td>
            <td :class="cn('px-2 py-1.5 align-top font-semibold tabular-nums', TONE_TEXT[statusTone(entry.status)])">{{ entry.status }}</td>
            <td :class="cn('px-2 py-1.5 align-top', wrap ? 'max-w-[520px]' : 'max-w-[420px]')">
              <button
                type="button"
                :class="
                  cn(
                    'block w-full rounded-sm text-start outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50',
                    wrap ? 'whitespace-pre-wrap [overflow-wrap:anywhere]' : 'truncate',
                  )
                "
                :aria-expanded="expanded.has(entry.raw)"
                :title="entry.host ? `${entry.host}${entry.uri ?? ''}` : entry.uri"
                @click="toggle(entry.raw)"
              >
                <span class="text-muted-foreground">{{ entry.method }}</span> {{ entry.uri }}
              </button>
            </td>
            <td class="px-2 py-1.5 text-end align-top whitespace-nowrap text-muted-foreground tabular-nums">
              {{ entry.bytes !== undefined ? formatBytes(entry.bytes) : '' }}
            </td>
            <td class="px-2 py-1.5 text-end align-top whitespace-nowrap text-muted-foreground tabular-nums">{{ durationText(entry.duration) }}</td>
            <td class="px-3 py-1.5 align-top whitespace-nowrap text-muted-foreground">{{ entry.remote }}</td>
          </template>
          <td v-else colspan="6" :class="cn('px-3 py-1.5', wrap ? 'whitespace-pre-wrap [overflow-wrap:anywhere]' : 'whitespace-pre')">{{ entry.raw }}</td>
        </tr>
        <tr v-if="expanded.has(entry.raw)" class="bg-muted/50">
          <td colspan="6" class="px-3 py-2 whitespace-pre-wrap [overflow-wrap:anywhere]">{{ entry.raw }}</td>
        </tr>
      </template>
    </tbody>
  </table>
</template>
