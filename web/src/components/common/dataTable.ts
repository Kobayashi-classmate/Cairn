// SPDX-License-Identifier: AGPL-3.0-only
//
// Column definitions for DataTable come from TanStack Table. Cells are
// usually rendered with a `cell-<column id>` slot of the table; `meta`
// carries the classes of a column.
import type { RowData } from '@tanstack/vue-table'

declare module '@tanstack/vue-table' {
  interface ColumnMeta<TData extends RowData, TValue> {
    /** Classes of the column's cells (width, alignment). */
    class?: string
    /** Classes of the column's header cell. */
    headClass?: string
    /** Unused; keeps the type parameters of the augmented interface. */
    readonly __types?: [TData, TValue]
  }
}

export type { ColumnDef, SortingState } from '@tanstack/vue-table'
