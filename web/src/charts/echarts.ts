// SPDX-License-Identifier: AGPL-3.0-only
//
// Registers only the ECharts parts the panel uses, keeping the bundle small.
// Import chart types and components here before using them in an option.
import { use, type ComposeOption } from 'echarts/core'
import { LineChart, type LineSeriesOption } from 'echarts/charts'
import {
  GridComponent,
  LegendComponent,
  TooltipComponent,
  type GridComponentOption,
  type LegendComponentOption,
  type TooltipComponentOption,
} from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

use([LineChart, GridComponent, LegendComponent, TooltipComponent, CanvasRenderer])

export type LineChartOption = ComposeOption<
  LineSeriesOption | GridComponentOption | LegendComponentOption | TooltipComponentOption
>

/** One line of a time-series chart. */
export interface TimeSeries {
  name: string
  /** [unix seconds, value] pairs, oldest first. */
  points: Array<[number, number]>
  /** Fill the area under the line. */
  area?: boolean
}
