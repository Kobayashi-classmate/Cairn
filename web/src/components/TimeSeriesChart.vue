<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import VChart from 'vue-echarts'
import { useThemeVars } from 'naive-ui'
import '@/charts/echarts'
import type { LineChartOption, TimeSeries } from '@/charts/echarts'
import { formatTime } from '@/i18n/format'

const props = defineProps<{
  series: TimeSeries[]
  /** Formats axis labels and tooltip values. */
  formatValue: (value: number) => string
  /** Fixed upper bound of the value axis (for example 100 for percentages). */
  max?: number
  /** Accessible description of the chart. */
  label: string
}>()

const vars = useThemeVars()

// Colours come from the Naive UI theme so the chart follows light/dark mode.
const option = computed<LineChartOption>(() => {
  const theme = vars.value
  const axisText = { color: theme.textColor3, fontFamily: theme.fontFamily }
  return {
    animation: false,
    backgroundColor: 'transparent',
    color: [theme.primaryColor, theme.infoColor, theme.warningColor],
    grid: { top: 36, bottom: 8, left: 8, right: 16 },
    legend: {
      top: 0,
      icon: 'roundRect',
      textStyle: { color: theme.textColor2, fontFamily: theme.fontFamily },
    },
    tooltip: {
      trigger: 'axis',
      backgroundColor: theme.popoverColor,
      borderColor: theme.dividerColor,
      textStyle: { color: theme.textColor2, fontFamily: theme.fontFamily },
      valueFormatter: (value) => (typeof value === 'number' ? props.formatValue(value) : '-'),
    },
    xAxis: {
      type: 'time',
      axisLabel: { ...axisText, hideOverlap: true, formatter: (ms: number) => formatTime(ms / 1000, false) },
      axisLine: { lineStyle: { color: theme.dividerColor } },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value',
      min: 0,
      max: props.max,
      axisLabel: { ...axisText, formatter: (value: number) => props.formatValue(value) },
      splitLine: { lineStyle: { color: theme.dividerColor } },
    },
    series: props.series.map((s) => ({
      type: 'line',
      name: s.name,
      showSymbol: false,
      lineStyle: { width: 1.5 },
      areaStyle: s.area ? { opacity: 0.12 } : undefined,
      data: s.points.map(([ts, value]) => [ts * 1000, value]),
    })),
  }
})
</script>

<template>
  <div class="time-series-chart" role="img" :aria-label="label">
    <VChart :option="option" autoresize />
  </div>
</template>

<style scoped>
.time-series-chart {
  block-size: 240px;
}
</style>
