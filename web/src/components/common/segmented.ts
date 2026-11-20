// SPDX-License-Identifier: AGPL-3.0-only
import type { Component } from 'vue'

/** One choice of a SegmentedControl. */
export interface SegmentedOption<V extends string> {
  value: V
  label: string
  icon?: Component
}
