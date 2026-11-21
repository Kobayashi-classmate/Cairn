<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CurrentMetrics, HistoryPoint, HostInfo } from '@/api/system'
import ErrorState from '@/components/common/ErrorState.vue'
import StatCard from '@/components/common/StatCard.vue'
import { formatByteRate, formatBytes, formatNumber, formatPercent } from '@/i18n/format'
import { cn } from '@/lib/utils'

// The four metric cards of the overview: CPU with load, memory, the root
// disk and network traffic with the last minute and a half as small bars.
const props = defineProps<{
  host: HostInfo | null
  current: CurrentMetrics | null
  history: readonly HistoryPoint[]
  error: unknown
}>()

const emit = defineEmits<{ retry: [] }>()

const { t } = useI18n()

/** Bars of the network card. */
const SPARK_BARS = 16

function percentOf(used: number, total: number): number {
  return total > 0 ? (used / total) * 100 : 0
}

function usage(used: number, total: number): string {
  return t('dashboard.usage', { used: formatBytes(used), total: formatBytes(total) })
}

const loading = computed(() => props.current === null)

const cpuCaption = computed(() => {
  const c = props.current
  if (!c) return ''
  const load = [c.load1, c.load5, c.load15].map((value) => formatNumber(value, 2)).join(' / ')
  const cores = props.host ? t('dashboard.cpu.cores', { count: props.host.cpuCores }, props.host.cpuCores) : ''
  return cores ? t('overview.metric.cpuCaption', { cores, load }) : t('overview.metric.load', { load })
})

const disk = computed(() => {
  const disks = props.current?.disks ?? []
  return disks.find((d) => d.mount === '/') ?? disks[0] ?? null
})

const spark = computed(() => {
  const points = props.history.slice(-SPARK_BARS).map((p) => p.netRxBps + p.netTxBps)
  const max = Math.max(1, ...points)
  return points.map((value, index) => ({
    height: 4 + Math.round((value / max) * 18),
    last: index === points.length - 1,
  }))
})
</script>

<template>
  <ErrorState v-if="error && !current" :error="error" compact class="rounded-lg border bg-card" @retry="emit('retry')" />
  <div v-else class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:gap-5 xl:grid-cols-4">
    <StatCard
      :label="t('dashboard.cpu.title')"
      :loading="loading"
      :value="current ? formatPercent(current.cpuPercent) : ''"
      :caption="cpuCaption"
      :percent="current?.cpuPercent"
    />
    <StatCard
      :label="t('dashboard.memory.title')"
      :loading="loading"
      :value="current ? formatPercent(percentOf(current.memUsed, current.memTotal)) : ''"
      :caption="current ? usage(current.memUsed, current.memTotal) : ''"
      :percent="current ? percentOf(current.memUsed, current.memTotal) : null"
    />
    <StatCard
      :label="disk ? t('overview.metric.disk', { mount: disk.mount }) : t('dashboard.disks.title')"
      :loading="loading"
      :value="disk ? formatPercent(percentOf(disk.used, disk.total)) : '—'"
      :caption="disk ? usage(disk.used, disk.total) : t('dashboard.disks.none')"
      :percent="disk ? percentOf(disk.used, disk.total) : null"
    />
    <StatCard
      :label="t('dashboard.network.title')"
      :loading="loading"
      :value="current ? formatByteRate(current.netRxBps + current.netTxBps) : ''"
      :caption="
        current
          ? t('overview.metric.netCaption', { rx: formatByteRate(current.netRxBps), tx: formatByteRate(current.netTxBps) })
          : ''
      "
    >
      <template #extra>
        <div v-if="spark.length > 1" class="mt-0.5 flex h-[22px] items-end gap-[3px]" aria-hidden="true">
          <div
            v-for="(bar, index) in spark"
            :key="index"
            :class="cn('flex-1 rounded-[2px]', bar.last ? 'bg-primary' : 'bg-track')"
            :style="{ height: `${bar.height}px` }"
          />
        </div>
      </template>
    </StatCard>
  </div>
</template>
