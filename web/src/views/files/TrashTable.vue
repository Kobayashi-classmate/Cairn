<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { File, Folder, Link2, RefreshCw, RotateCcw, Trash2 } from '@lucide/vue'
import type { TrashEntry } from '@/api/files'
import DataTable from '@/components/common/DataTable.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import RowActions from '@/components/common/RowActions.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { SegmentedOption } from '@/components/common/segmented'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { absolutePath, baseName, parentPath, rootLabel } from '@/files/paths'
import { formatBytes } from '@/i18n/format'
import { useFilesStore } from '@/stores/files'
import { askEmpty, askPurge, restore } from './actions'

// A root's trash: what was deleted there, from where, by whom and when.
// Entries go back to where they were, or are deleted for good.
const props = defineProps<{ root: string; canManage: boolean }>()
const emit = defineEmits<{ 'update:root': [root: string] }>()

const { t } = useI18n()
const store = useFilesStore()
const selection = ref<string[]>([])
const search = ref('')

watch(
  () => props.root,
  (root) => {
    selection.value = []
    void store.loadTrash(root)
  },
  { immediate: true },
)

const rootOptions = computed<SegmentedOption<string>[]>(() =>
  store.available.map((root) => ({ value: root.id, label: rootLabel(root.id) })),
)
const entries = computed(() => store.trash[props.root] ?? [])
const loading = computed(() => store.trashLoading[props.root] === true)
const error = computed(() => store.trashErrors[props.root])
const totalSize = computed(() => entries.value.reduce((sum, entry) => sum + entry.size, 0))

const columns = computed<ColumnDef<TrashEntry>[]>(() => [
  { id: 'name', header: t('common.field.name'), accessorFn: (entry) => baseName(entry.path).toLowerCase(), meta: { class: 'min-w-[200px]' } },
  { id: 'size', header: t('files.column.size'), accessorFn: (entry) => (entry.type === 'dir' ? -1 : entry.size), meta: { class: 'w-[100px] text-end', headClass: 'text-end' } },
  { id: 'deletedAt', header: t('files.trash.deletedAt'), accessorFn: (entry) => entry.deletedAt, meta: { class: 'w-[150px]' } },
  { id: 'from', header: t('files.trash.from'), accessorFn: (entry) => entry.path, meta: { class: 'min-w-[220px]' } },
  { id: 'user', header: t('files.trash.by'), accessorFn: (entry) => entry.user ?? '', meta: { class: 'w-[130px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[60px] text-end' } },
])

function searchText(entry: TrashEntry): string {
  return [entry.path, entry.user ?? ''].join(' ')
}

function iconOf(entry: TrashEntry) {
  return entry.type === 'dir' ? Folder : entry.type === 'link' ? Link2 : File
}

function actions(entry: TrashEntry): RowAction[] {
  if (!props.canManage) return []
  return [
    { key: 'restore', label: t('files.trash.restore'), icon: RotateCcw, onSelect: () => void restore(props.root, [entry]) },
    { key: 'purge', label: t('files.trash.purge'), icon: Trash2, destructive: true, onSelect: () => void askPurge(props.root, [entry]) },
  ]
}

function picked(keys: readonly string[]): TrashEntry[] {
  const set = new Set(keys)
  return entries.value.filter((entry) => set.has(entry.id))
}
</script>

<template>
  <div class="flex flex-col gap-3">
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
      <SegmentedControl
        v-if="rootOptions.length > 1"
        :model-value="root"
        :options="rootOptions"
        :label="t('files.trash.which')"
        class="w-full sm:w-auto"
        @update:model-value="(value: string) => emit('update:root', value)"
      />
      <p class="m-0 text-detail text-muted-foreground" role="status">
        <template v-if="store.trash[root]">
          {{ t('files.trash.total', { count: entries.length, size: formatBytes(totalSize) }, entries.length) }}
        </template>
      </p>
    </div>

    <DataTable
      v-model:selection="selection"
      v-model:search="search"
      :data="entries"
      :columns="columns"
      :row-key="(entry) => entry.id"
      :label="t('files.trash.title', { root: rootLabel(root) })"
      :loading="loading && !store.trash[root]"
      :error="error"
      :selectable="canManage"
      :search-placeholder="t('files.trash.search')"
      :search-text="searchText"
      :empty-title="t('files.trash.emptyList')"
      :empty-text="t('files.trash.intro')"
      :min-width="860"
      :initial-sort="[{ id: 'deletedAt', desc: true }]"
      @retry="store.loadTrash(root)"
    >
      <template #toolbar>
        <Button
          variant="ghost"
          size="icon"
          class="max-md:size-11"
          :disabled="loading"
          :aria-label="t('common.action.refresh')"
          :title="t('common.action.refresh')"
          @click="store.loadTrash(root)"
        >
          <RefreshCw :class="loading ? 'animate-spin' : ''" aria-hidden="true" />
        </Button>
        <Button
          v-if="canManage"
          variant="outline"
          class="text-destructive hover:bg-destructive-soft hover:text-destructive max-md:h-11"
          :disabled="entries.length === 0"
          @click="askEmpty(root, entries.length)"
        >
          <Trash2 aria-hidden="true" />
          {{ t('files.trash.empty') }}
        </Button>
      </template>
      <template #cell-name="{ row }">
        <span class="flex min-w-0 items-center gap-2.5">
          <component :is="iconOf(row)" :class="['size-[18px] shrink-0', row.type === 'dir' ? 'text-primary' : 'text-muted-foreground']" aria-hidden="true" />
          <span class="truncate font-medium" dir="auto" :title="baseName(row.path)">{{ baseName(row.path) }}</span>
        </span>
      </template>
      <template #cell-size="{ row }">
        <span class="whitespace-nowrap text-muted-foreground tabular-nums">{{ row.type === 'dir' ? '—' : formatBytes(row.size) }}</span>
      </template>
      <template #cell-deletedAt="{ row }">
        <RelativeTime :value="row.deletedAt" class="text-muted-foreground" />
      </template>
      <template #cell-from="{ row }">
        <span class="block truncate font-mono text-[12.5px]" dir="ltr" :title="absolutePath(root, row.path)">
          {{ absolutePath(root, parentPath(row.path)) }}
        </span>
      </template>
      <template #cell-user="{ row }">
        <span :class="row.user ? '' : 'text-muted-foreground'">{{ row.user || '—' }}</span>
      </template>
      <template #cell-actions="{ row }">
        <div class="flex justify-end">
          <RowActions :actions="actions(row)" :label="t('files.action.more', { name: baseName(row.path) })" />
        </div>
      </template>
      <template #bulk="{ keys }">
        <Button variant="outline" size="sm" class="max-md:h-11" @click="restore(root, picked(keys))">
          <RotateCcw aria-hidden="true" />
          {{ t('files.trash.restore') }}
        </Button>
        <span class="mx-1 h-5 w-px bg-border max-sm:hidden" aria-hidden="true" />
        <Button
          variant="outline"
          size="sm"
          class="text-destructive hover:bg-destructive-soft hover:text-destructive max-md:h-11"
          @click="askPurge(root, picked(keys))"
        >
          <Trash2 aria-hidden="true" />
          {{ t('files.trash.purge') }}
        </Button>
      </template>
    </DataTable>
  </div>
</template>
