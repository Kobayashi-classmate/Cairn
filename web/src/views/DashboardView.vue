<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NCard, NGi, NGrid, NH2, NIcon, NSpin, NTag, NText, useThemeVars } from 'naive-ui'
import { ArrowDownOutline, ArrowUpOutline } from '@vicons/ionicons5'
import { isAbortError } from '@/api/client'
import {
  fetchSystemOverview,
  type CurrentMetrics,
  type HistoryPoint,
  type SystemOverview,
} from '@/api/system'
import type { TimeSeries } from '@/charts/echarts'
import TimeSeriesChart from '@/components/TimeSeriesChart.vue'
import UsageBar from '@/components/UsageBar.vue'
import { usePolling } from '@/composables/usePolling'
import { errorMessage } from '@/i18n/errors'
import {
  formatByteRate,
  formatBytes,
  formatDuration,
  formatMinutes,
  formatNumber,
  formatPercent,
  formatTime,
} from '@/i18n/format'

const POLL_INTERVAL_MS = 5000
const HISTORY_WINDOW_SECONDS = 3600

const { t } = useI18n()
const themeVars = useThemeVars()

const overview = ref<SystemOverview | null>(null)
const loadError = ref<unknown>(null)
/** Samples taken from `current` that are newer than the server's history. */
const livePoints = ref<HistoryPoint[]>([])

function percentOf(used: number, total: number): number {
  return total > 0 ? (used / total) * 100 : 0
}

function toHistoryPoint(current: CurrentMetrics): HistoryPoint {
  return {
    ts: current.ts,
    cpuPercent: current.cpuPercent,
    memUsedPercent: percentOf(current.memUsed, current.memTotal),
    netRxBps: current.netRxBps,
    netTxBps: current.netTxBps,
  }
}

async function refresh(signal: AbortSignal) {
  try {
    const data = await fetchSystemOverview(signal)
    // Keep only live samples the server's history does not cover yet.
    const newestHistory = data.history.at(-1)?.ts ?? -Infinity
    const live = livePoints.value.filter((p) => p.ts > newestHistory && p.ts < data.current.ts)
    if (data.current.ts > newestHistory) live.push(toHistoryPoint(data.current))
    livePoints.value = live
    overview.value = data
    loadError.value = null
  } catch (err) {
    if (isAbortError(err)) return
    // A 401 is handled globally (redirect to sign-in); show anything else.
    loadError.value = err
  }
}

usePolling(refresh, POLL_INTERVAL_MS)

/** Server history followed by live samples, limited to the last hour. */
const points = computed<HistoryPoint[]>(() => {
  const all = [...(overview.value?.history ?? []), ...livePoints.value]
  const newest = all.at(-1)?.ts ?? 0
  return all.filter((p) => p.ts > newest - HISTORY_WINDOW_SECONDS)
})

const current = computed(() => overview.value?.current ?? null)
const host = computed(() => overview.value?.host ?? null)

const hostRows = computed(() => {
  const h = host.value
  if (!h) return []
  return [
    { label: t('dashboard.host.hostname'), value: h.hostname },
    { label: t('dashboard.host.os'), value: h.os },
    { label: t('dashboard.host.kernel'), value: h.kernel },
    { label: t('dashboard.host.arch'), value: h.arch },
    { label: t('dashboard.host.cpu'), value: h.cpuModel },
    { label: t('dashboard.host.uptime'), value: formatDuration(h.uptimeSeconds) },
  ]
})

const loadRows = computed(() => {
  const c = current.value
  if (!c) return []
  return [
    { label: formatMinutes(1), value: c.load1 },
    { label: formatMinutes(5), value: c.load5 },
    { label: formatMinutes(15), value: c.load15 },
  ]
})

const cpuSeries = computed<TimeSeries[]>(() => [
  {
    name: t('dashboard.charts.cpu'),
    points: points.value.map((p) => [p.ts, p.cpuPercent]),
    area: true,
  },
])

const memorySeries = computed<TimeSeries[]>(() => [
  {
    name: t('dashboard.charts.memory'),
    points: points.value.map((p) => [p.ts, p.memUsedPercent]),
    area: true,
  },
])

const networkSeries = computed<TimeSeries[]>(() => [
  { name: t('dashboard.network.receive'), points: points.value.map((p) => [p.ts, p.netRxBps]) },
  { name: t('dashboard.network.transmit'), points: points.value.map((p) => [p.ts, p.netTxBps]) },
])

function usage(used: number, total: number): string {
  return t('dashboard.usage', { used: formatBytes(used), total: formatBytes(total) })
}

const percentAxis = (value: number) => formatPercent(value, 0)
</script>

<template>
  <div class="dashboard">
    <header class="dashboard__header">
      <NH2 class="dashboard__title">{{ t('dashboard.title') }}</NH2>
      <NText v-if="current" depth="3">
        {{ t('dashboard.updatedAt', { time: formatTime(current.ts) }) }}
      </NText>
    </header>

    <NAlert v-if="loadError" type="error" :title="t('dashboard.loadFailed')" class="dashboard__alert">
      {{ errorMessage(loadError) }}
    </NAlert>

    <div v-if="!overview && !loadError" class="dashboard__loading">
      <NSpin />
    </div>

    <template v-if="overview && current && host">
      <NCard :title="t('dashboard.host.title')" size="small" class="dashboard__section">
        <dl class="host">
          <div v-for="row in hostRows" :key="row.label" class="host__item">
            <dt><NText depth="3">{{ row.label }}</NText></dt>
            <dd>{{ row.value }}</dd>
          </div>
        </dl>
      </NCard>

      <NGrid cols="1 s:2 l:3" responsive="screen" :x-gap="16" :y-gap="16" class="dashboard__section">
        <NGi>
          <NCard :title="t('dashboard.cpu.title')" size="small" class="dashboard__card">
            <div class="metric">{{ formatPercent(current.cpuPercent) }}</div>
            <UsageBar
              :percent="current.cpuPercent"
              :show-percent="false"
              :detail="t('dashboard.cpu.cores', { count: host.cpuCores }, host.cpuCores)"
            />
          </NCard>
        </NGi>

        <NGi>
          <NCard :title="t('dashboard.load.title')" size="small" class="dashboard__card">
            <div class="load">
              <div v-for="row in loadRows" :key="row.label" class="load__item">
                <div class="metric">{{ formatNumber(row.value, 2) }}</div>
                <NText depth="3">{{ row.label }}</NText>
              </div>
            </div>
          </NCard>
        </NGi>

        <NGi>
          <NCard :title="t('dashboard.network.title')" size="small" class="dashboard__card">
            <div class="network">
              <div class="network__row">
                <NIcon :size="18" class="network__icon"><ArrowDownOutline /></NIcon>
                <NText depth="3">{{ t('dashboard.network.receive') }}</NText>
                <span class="network__value">{{ formatByteRate(current.netRxBps) }}</span>
              </div>
              <div class="network__row">
                <NIcon :size="18" class="network__icon"><ArrowUpOutline /></NIcon>
                <NText depth="3">{{ t('dashboard.network.transmit') }}</NText>
                <span class="network__value">{{ formatByteRate(current.netTxBps) }}</span>
              </div>
            </div>
          </NCard>
        </NGi>

        <NGi>
          <NCard :title="t('dashboard.memory.title')" size="small" class="dashboard__card">
            <div class="metric">{{ formatPercent(percentOf(current.memUsed, current.memTotal)) }}</div>
            <UsageBar
              :percent="percentOf(current.memUsed, current.memTotal)"
              :show-percent="false"
              :detail="usage(current.memUsed, current.memTotal)"
            />
            <NText depth="3" class="dashboard__note">
              {{ t('dashboard.memory.available', { value: formatBytes(current.memAvailable) }) }}
            </NText>
          </NCard>
        </NGi>

        <NGi>
          <NCard :title="t('dashboard.swap.title')" size="small" class="dashboard__card">
            <template v-if="current.swapTotal > 0">
              <div class="metric">{{ formatPercent(percentOf(current.swapUsed, current.swapTotal)) }}</div>
              <UsageBar
                :percent="percentOf(current.swapUsed, current.swapTotal)"
                :show-percent="false"
                :detail="usage(current.swapUsed, current.swapTotal)"
              />
            </template>
            <NText v-else depth="3">{{ t('dashboard.swap.none') }}</NText>
          </NCard>
        </NGi>

        <NGi>
          <NCard :title="t('dashboard.disks.title')" size="small" class="dashboard__card">
            <div v-if="current.disks.length > 0" class="disks">
              <div v-for="disk in current.disks" :key="disk.mount" class="disks__item">
                <div class="disks__head">
                  <span class="disks__mount" dir="ltr">{{ disk.mount }}</span>
                  <NTag size="small" :bordered="false">{{ disk.fstype }}</NTag>
                </div>
                <UsageBar :percent="percentOf(disk.used, disk.total)" :detail="usage(disk.used, disk.total)" />
              </div>
            </div>
            <NText v-else depth="3">{{ t('dashboard.disks.none') }}</NText>
          </NCard>
        </NGi>
      </NGrid>

      <NGrid cols="1 m:2 xl:3" responsive="screen" :x-gap="16" :y-gap="16" class="dashboard__section">
        <NGi>
          <NCard :title="t('dashboard.charts.cpuTitle')" size="small">
            <TimeSeriesChart
              :series="cpuSeries"
              :format-value="percentAxis"
              :max="100"
              :label="t('dashboard.charts.cpuTitle')"
            />
          </NCard>
        </NGi>
        <NGi>
          <NCard :title="t('dashboard.charts.memoryTitle')" size="small">
            <TimeSeriesChart
              :series="memorySeries"
              :format-value="percentAxis"
              :max="100"
              :label="t('dashboard.charts.memoryTitle')"
            />
          </NCard>
        </NGi>
        <NGi>
          <NCard :title="t('dashboard.charts.networkTitle')" size="small">
            <TimeSeriesChart
              :series="networkSeries"
              :format-value="formatByteRate"
              :label="t('dashboard.charts.networkTitle')"
            />
          </NCard>
        </NGi>
      </NGrid>
    </template>
  </div>
</template>

<style scoped>
.dashboard {
  max-inline-size: 1600px;
}

.dashboard__header {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px 16px;
  margin-block-end: 16px;
}

.dashboard__title {
  margin: 0;
}

.dashboard__alert,
.dashboard__section {
  margin-block-end: 16px;
}

.dashboard__loading {
  display: flex;
  justify-content: center;
  padding-block: 64px;
}

.dashboard__card {
  block-size: 100%;
}

.dashboard__note {
  display: block;
  margin-block-start: 8px;
  font-size: 13px;
}

.metric {
  margin-block-end: 8px;
  font-size: 26px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  line-height: 1.2;
}

.host {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 12px 24px;
  margin: 0;
}

.host__item dt {
  font-size: 13px;
}

.host__item dd {
  margin: 2px 0 0;
  overflow-wrap: anywhere;
}

.load {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
}

.network {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.network__row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.network__value {
  margin-inline-start: auto;
  font-size: 18px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.disks {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.disks__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-block-end: 4px;
}

.disks__mount {
  font-family: v-bind('themeVars.fontFamilyMono');
  overflow-wrap: anywhere;
}
</style>
