<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts" generic="T">
import { computed, nextTick, ref, watch, type HTMLAttributes, type VNodeChild } from 'vue'
import { useI18n } from 'vue-i18n'
import { useResizeObserver } from '@vueuse/core'
import { ArrowDown, ArrowUp, ArrowUpDown, Search, SearchX, X } from '@lucide/vue'
import {
  FlexRender,
  getCoreRowModel,
  getSortedRowModel,
  useVueTable,
  type Column,
  type ColumnDef,
  type SortingState,
} from '@tanstack/vue-table'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'
import EmptyState from './EmptyState.vue'
import ErrorState from './ErrorState.vue'
import Notice from './Notice.vue'
import './dataTable'

// A list page's table (interface spec, sections 6, 7 and 9): a search box
// and the page's filters on top, sortable headers, optional row selection
// with a bulk-action bar that appears only while rows are selected, its own
// horizontal scroll container, skeleton rows while loading, and the empty
// and error states in place of the rows.
//
// Cells render through `cell-<column id>` slots (with { row, value }) or the
// column's own `cell` function.
const props = withDefaults(
  defineProps<{
    data: readonly T[]
    columns: ColumnDef<T, any>[]
    rowKey: (row: T) => string
    /** Accessible name of the table. */
    label: string
    loading?: boolean
    /** Why loading failed; shown in place of the rows (or above them when some are known). */
    error?: unknown
    /** Shows the search box. */
    searchPlaceholder?: string
    /** The text the search matches; defaults to every string field of the row. */
    searchText?: (row: T) => string
    /** Adds a checkbox per row; the `bulk` slot gets the selected rows. */
    selectable?: boolean
    /** Rows that may be selected (all by default); the others show a disabled checkbox. */
    canSelect?: (row: T) => boolean
    emptyTitle?: string
    emptyText?: string
    /** Narrowest width of the table before it scrolls inside its card (px). */
    minWidth?: number
    initialSort?: SortingState
    skeletonRows?: number
    /** Leave out the card's border and background (inside another card). */
    flat?: boolean
    /**
     * Clicking a row (outside its buttons, links and checkbox) emits
     * `row-click`. A mouse shortcut only: the row must still offer a button
     * that does the same for keyboard users.
     */
    rowClickable?: boolean
    class?: HTMLAttributes['class']
  }>(),
  {
    loading: false,
    error: undefined,
    searchPlaceholder: undefined,
    searchText: undefined,
    canSelect: undefined,
    emptyTitle: undefined,
    emptyText: undefined,
    minWidth: 720,
    initialSort: () => [],
    skeletonRows: 4,
    class: undefined,
  },
)

const emit = defineEmits<{ retry: []; 'row-click': [row: T] }>()

const search = defineModel<string>('search', { default: '' })
/** Keys of the selected rows. */
const selection = defineModel<string[]>('selection', { default: () => [] })

defineSlots<
  {
    /** Filters and buttons on the right of the toolbar. */
    toolbar?: () => VNodeChild
    /** Actions for the selected rows. */
    bulk?: (props: { rows: T[]; keys: string[]; clear: () => void }) => VNodeChild
    /** Main action of the empty state. */
    empty?: () => VNodeChild
  } & {
    [name: `cell-${string}`]: (props: { row: T; value: unknown }) => VNodeChild
  }
>()

const { t } = useI18n()

function defaultText(row: T): string {
  if (typeof row !== 'object' || row === null) return String(row)
  return Object.values(row as Record<string, unknown>)
    .flatMap((value) => (Array.isArray(value) ? value : [value]))
    .filter((value): value is string | number => typeof value === 'string' || typeof value === 'number')
    .join(' ')
}

const query = computed(() => search.value.trim().toLowerCase())

const rows = computed<T[]>(() => {
  const all = props.data as T[]
  if (!query.value) return all
  const text = props.searchText ?? defaultText
  return all.filter((row) => text(row).toLowerCase().includes(query.value))
})

const sorting = ref<SortingState>([...props.initialSort])

const table = useVueTable<T>({
  get data() {
    return rows.value
  },
  get columns() {
    return props.columns
  },
  state: {
    get sorting() {
      return sorting.value
    },
  },
  onSortingChange: (updater) => {
    sorting.value = typeof updater === 'function' ? updater(sorting.value) : updater
  },
  getRowId: (row) => props.rowKey(row),
  getCoreRowModel: getCoreRowModel(),
  getSortedRowModel: getSortedRowModel(),
})

const columnCount = computed(() => props.columns.length + (props.selectable ? 1 : 0))
const showToolbar = computed(() => props.searchPlaceholder !== undefined)
const initialLoading = computed(() => props.loading && props.data.length === 0 && !props.error)
const failedEmpty = computed(() => !!props.error && props.data.length === 0)

// A table wider than its card scrolls sideways. When nothing inside it can
// take focus (no rows, read-only rows), the scroller itself joins the tab
// order so the keyboard can scroll it.
const scroller = ref<HTMLElement | null>(null)
const scrollFocusable = ref(false)
const FOCUSABLE = 'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])'
function updateScrollFocusable() {
  const el = scroller.value
  scrollFocusable.value = !!el && el.scrollWidth > el.clientWidth + 1 && !el.querySelector(`tbody :is(${FOCUSABLE})`)
}
useResizeObserver(scroller, updateScrollFocusable)
watch(rows, () => void nextTick(updateScrollFocusable))

// Rows that are no longer shown (deleted, filtered out by a refresh, hidden
// by the search) leave the selection: bulk actions act only on rows in view.
watch(rows, (shown) => {
  if (selection.value.length === 0) return
  const keys = new Set(shown.map((row) => props.rowKey(row)))
  const kept = selection.value.filter((key) => keys.has(key))
  if (kept.length !== selection.value.length) selection.value = kept
})

const visibleKeys = computed(() =>
  rows.value.filter((row) => !props.canSelect || props.canSelect(row)).map((row) => props.rowKey(row)),
)
const selectedSet = computed(() => new Set(selection.value))
const allSelected = computed(
  () => visibleKeys.value.length > 0 && visibleKeys.value.every((key) => selectedSet.value.has(key)),
)
const someSelected = computed(() => visibleKeys.value.some((key) => selectedSet.value.has(key)))
const selectedRows = computed(() => (props.data as T[]).filter((row) => selectedSet.value.has(props.rowKey(row))))

function toggleAll(checked: boolean | 'indeterminate') {
  if (checked === true) {
    selection.value = [...new Set([...selection.value, ...visibleKeys.value])]
  } else {
    const visible = new Set(visibleKeys.value)
    selection.value = selection.value.filter((key) => !visible.has(key))
  }
}

function toggleRow(key: string, checked: boolean | 'indeterminate') {
  selection.value = checked === true ? [...selection.value, key] : selection.value.filter((k) => k !== key)
}

function clearSelection() {
  selection.value = []
}

function ariaSort(column: Column<T, unknown>): 'ascending' | 'descending' | 'none' | undefined {
  if (!column.getCanSort()) return undefined
  const sorted = column.getIsSorted()
  return sorted === 'asc' ? 'ascending' : sorted === 'desc' ? 'descending' : 'none'
}

function headerText(column: Column<T, unknown>): string {
  const header = column.columnDef.header
  return typeof header === 'string' ? header : ''
}

/** Controls inside a row keep their own clicks. */
const INTERACTIVE = 'button, a, input, select, textarea, label, [role="checkbox"], [role="menuitem"], [contenteditable="true"], [data-select-cell]'

function onRowClick(event: MouseEvent, row: T) {
  if (!props.rowClickable) return
  const target = event.target instanceof Element ? event.target : null
  if (target?.closest(INTERACTIVE)) return
  // Selecting text in a row is not a click on it.
  if (window.getSelection()?.toString()) return
  emit('row-click', row)
}

function rowLabel(row: T): string {
  return t('common.table.selectRow', { row: props.rowKey(row) })
}
</script>

<template>
  <div
    :class="cn('flex min-w-0 flex-col', !flat && 'overflow-hidden rounded-lg border bg-card', props.class)"
  >
    <div
      v-if="showToolbar || $slots.toolbar"
      class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b px-4 py-3"
    >
      <label
        v-if="searchPlaceholder !== undefined"
        class="flex h-9 min-w-0 flex-[1_1_220px] items-center gap-2 rounded-md border border-input bg-card px-2.5 text-muted-foreground focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/50 sm:max-w-80 max-md:h-11"
      >
        <Search class="size-4 shrink-0" aria-hidden="true" />
        <input
          v-model="search"
          type="search"
          :aria-label="searchPlaceholder"
          :placeholder="searchPlaceholder"
          class="h-full min-w-0 flex-1 bg-transparent text-base text-foreground outline-none placeholder:text-muted-foreground md:text-sm [&::-webkit-search-cancel-button]:hidden"
        />
        <button
          v-if="search"
          type="button"
          class="inline-flex size-6 shrink-0 items-center justify-center rounded-sm outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 max-md:-me-2.5 max-md:size-11"
          :aria-label="t('common.table.clearSearch')"
          @click="search = ''"
        >
          <X class="size-3.5" aria-hidden="true" />
        </button>
      </label>
      <div v-if="$slots.toolbar" class="ms-auto flex flex-wrap items-center gap-2">
        <slot name="toolbar" />
      </div>
    </div>

    <div v-if="error && data.length > 0" class="border-b px-4 py-2">
      <Notice tone="destructive" class="py-1.5">
        {{ t('common.state.refreshFailed') }}
        <template #action>
          <button type="button" class="underline underline-offset-2 hover:no-underline" @click="emit('retry')">
            {{ t('common.action.retry') }}
          </button>
        </template>
      </Notice>
    </div>

    <!-- relative: visually hidden text in cells is positioned inside the
         scroller instead of widening the page. Without rows the table drops
         its minimum width, so the empty state fits without scrolling. -->
    <div
      ref="scroller"
      class="relative min-w-0 overflow-x-auto focus-visible:-outline-offset-2"
      :tabindex="scrollFocusable ? 0 : undefined"
      :role="scrollFocusable ? 'group' : undefined"
      :aria-label="scrollFocusable ? label : undefined"
    >
      <table
        class="w-full border-collapse text-sm"
        :style="{ minWidth: rows.length > 0 || initialLoading ? `${minWidth}px` : undefined }"
        :aria-label="label"
        :aria-busy="loading || undefined"
      >
        <thead>
          <tr v-for="group in table.getHeaderGroups()" :key="group.id" class="bg-muted">
            <th v-if="selectable" scope="col" class="w-10 px-4 py-2 text-start">
              <Checkbox
                :model-value="allSelected ? true : someSelected ? 'indeterminate' : false"
                :disabled="visibleKeys.length === 0"
                :aria-label="t('common.table.selectAll')"
                @update:model-value="toggleAll"
              />
            </th>
            <th
              v-for="header in group.headers"
              :key="header.id"
              scope="col"
              :aria-sort="ariaSort(header.column)"
              :class="
                cn(
                  'h-9 px-4 py-2 text-start align-middle text-caption whitespace-nowrap text-muted-foreground',
                  header.column.columnDef.meta?.headClass ?? header.column.columnDef.meta?.class,
                )
              "
            >
              <button
                v-if="header.column.getCanSort()"
                type="button"
                class="-mx-1 inline-flex items-center gap-1 rounded-sm px-1 outline-none tap-area hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
                @click="header.column.toggleSorting(header.column.getIsSorted() === 'asc')"
              >
                {{ headerText(header.column) }}
                <ArrowUp v-if="header.column.getIsSorted() === 'asc'" class="size-3.5" aria-hidden="true" />
                <ArrowDown v-else-if="header.column.getIsSorted() === 'desc'" class="size-3.5" aria-hidden="true" />
                <ArrowUpDown v-else class="size-3.5 opacity-50" aria-hidden="true" />
              </button>
              <template v-else-if="!header.isPlaceholder">
                <span v-if="headerText(header.column)">{{ headerText(header.column) }}</span>
                <!-- The row actions column has no visible heading. -->
                <span v-else-if="header.column.id === 'actions'" class="sr-only">{{ t('common.table.actions') }}</span>
                <FlexRender v-else :render="header.column.columnDef.header" :props="header.getContext()" />
              </template>
            </th>
          </tr>
        </thead>
        <tbody>
          <template v-if="initialLoading">
            <tr v-for="index in skeletonRows" :key="`skeleton-${index}`" class="border-t" aria-hidden="true">
              <td v-if="selectable" class="px-4 py-3"><Skeleton class="size-4" /></td>
              <td v-for="column in columns" :key="column.id ?? index" class="px-4 py-3.5">
                <Skeleton class="h-3.5" :style="{ width: `${50 + ((index * 17) % 40)}%` }" />
              </td>
            </tr>
          </template>
          <tr v-else-if="failedEmpty">
            <td :colspan="columnCount">
              <ErrorState :error="error" compact @retry="emit('retry')" />
            </td>
          </tr>
          <tr v-else-if="rows.length === 0">
            <td :colspan="columnCount">
              <EmptyState
                v-if="query"
                :icon="SearchX"
                compact
                :title="t('common.table.noMatches', { query: search.trim() })"
              >
                <template #actions>
                  <Button variant="outline" size="sm" @click="search = ''">{{ t('common.table.clearSearch') }}</Button>
                </template>
              </EmptyState>
              <EmptyState v-else :title="emptyTitle ?? t('common.table.empty')" :text="emptyText">
                <template v-if="$slots.empty" #actions>
                  <slot name="empty" />
                </template>
              </EmptyState>
            </td>
          </tr>
          <template v-else>
            <tr
              v-for="row in table.getRowModel().rows"
              :key="row.id"
              :data-state="selectedSet.has(row.id) ? 'selected' : undefined"
              :class="
                cn(
                  'border-t transition-colors hover:bg-accent/50 data-[state=selected]:bg-primary-soft/60',
                  rowClickable && 'cursor-pointer',
                )
              "
              @click="onRowClick($event, row.original)"
            >
              <td v-if="selectable" class="w-10 px-4 py-[11px] align-middle" data-select-cell>
                <Checkbox
                  :model-value="selectedSet.has(row.id)"
                  :disabled="canSelect ? !canSelect(row.original) : false"
                  :aria-label="rowLabel(row.original)"
                  @update:model-value="(checked: boolean | 'indeterminate') => toggleRow(row.id, checked)"
                />
              </td>
              <td
                v-for="cell in row.getVisibleCells()"
                :key="cell.id"
                :class="cn('px-4 py-[11px] align-middle', cell.column.columnDef.meta?.class)"
              >
                <slot
                  v-if="$slots[`cell-${cell.column.id}`]"
                  :name="`cell-${cell.column.id}`"
                  :row="row.original"
                  :value="cell.getValue()"
                />
                <FlexRender v-else :render="cell.column.columnDef.cell" :props="cell.getContext()" />
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>

    <div
      v-if="selectable && selection.length > 0"
      role="region"
      :aria-label="t('common.table.bulkLabel')"
      class="flex flex-wrap items-center gap-2 border-t bg-muted/60 px-4 py-2.5"
    >
      <span class="me-auto text-detail font-medium" role="status">
        {{ t('common.table.selected', { count: selection.length }, selection.length) }}
      </span>
      <slot name="bulk" :rows="selectedRows" :keys="selection" :clear="clearSelection" />
      <Button variant="ghost" size="sm" @click="clearSelection">{{ t('common.table.clearSelection') }}</Button>
    </div>
  </div>
</template>
