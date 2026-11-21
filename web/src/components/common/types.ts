// SPDX-License-Identifier: AGPL-3.0-only
//
// Shapes shared by the composite components in this folder.
import type { Component } from 'vue'
import type { RouteLocationRaw } from 'vue-router'
import type { Tone } from '@/lib/tone'

/** One choice of a SelectField; "" is allowed. */
export interface SelectOption {
  value: string
  label: string
  disabled?: boolean
}

/** One choice of an OptionGroup (a radio card). */
export interface OptionItem {
  value: string
  label: string
  /** Second line, muted. */
  hint?: string
  disabled?: boolean
}

/** One entry of a row's "more" menu. */
export interface RowAction {
  key: string
  label: string
  icon?: Component
  /** Run on selection; ignored when `href` or `to` is set. */
  onSelect?: () => void
  /** Opens an address in a new tab (visiting a site). */
  href?: string
  /** Opens a page of the panel. */
  to?: RouteLocationRaw
  disabled?: boolean
  /** Listed after the others, separated and in the danger colour. */
  destructive?: boolean
}

/** One underline tab of a page header. */
export interface PageTab<V extends string = string> {
  value: V
  label: string
  /** Shown in a small pill after the label. */
  count?: number
}

/** One entry of a settings drawer's sub-menu. */
export interface DrawerItem<V extends string = string> {
  value: V
  label: string
  icon?: Component
}

/** The status pill in a drawer's header. */
export interface DrawerStatus {
  tone: Tone
  label: string
}
