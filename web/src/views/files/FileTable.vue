<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Archive,
  ArrowUp,
  Bookmark,
  Copy,
  Download,
  Eye,
  EyeOff,
  File as FileIcon,
  FileArchive,
  FileImage,
  Folder,
  FolderInput,
  FolderOpen,
  KeyRound,
  Link2,
  PackageOpen,
  Pencil,
  RefreshCw,
  Search,
  Star,
  TextCursorInput,
  Trash2,
  Upload,
  UserCog,
  X,
} from '@lucide/vue'
import type { FileEntry } from '@/api/files'
import DataTable from '@/components/common/DataTable.vue'
import Notice from '@/components/common/Notice.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import RowActions from '@/components/common/RowActions.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { modeString, octal } from '@/files/modes'
import { absolutePath, archiveKind, baseName, isImage, parentPath, rootLabel } from '@/files/paths'
import { formatBytes, formatNumber } from '@/i18n/format'
import { notifyError, notifyErrorText } from '@/lib/notify'
import { cn } from '@/lib/utils'
import { useFilesStore } from '@/stores/files'
import type { FileActionKey } from './actions'
import FileBreadcrumb from './FileBreadcrumb.vue'
import UploadsPanel from './UploadsPanel.vue'

// The folder shown ("list" skeleton, interface spec section 6): a toolbar
// with the path, a filter for this folder, favourites, hidden files and
// Refresh; the entries with row selection and a bulk bar; files dropped on
// the card are uploaded into the folder.
const props = defineProps<{ root: string; path: string; canManage: boolean }>()

const emit = defineEmits<{
  open: [entry: FileEntry]
  navigate: [path: string]
  go: [root: string, path: string]
  action: [key: FileActionKey, entries: FileEntry[]]
  upload: [files: File[]]
  choose: []
}>()

/** Most entries the server lists in one folder. */
const LIST_MAX = 5000
/** Rows rendered at first; long folders show more on request. */
const PAGE = 500

const { t } = useI18n()
const store = useFilesStore()

const filter = ref('')
const limit = ref(PAGE)
const selection = ref<string[]>([])
const dragDepth = ref(0)

watch(
  () => [props.root, props.path] as const,
  () => {
    filter.value = ''
    limit.value = PAGE
    selection.value = []
  },
)

const entries = computed(() => store.list?.entries ?? [])
const matching = computed(() => {
  const query = filter.value.trim().toLowerCase()
  return query ? entries.value.filter((entry) => entry.name.toLowerCase().includes(query)) : entries.value
})
const rows = computed(() => matching.value.slice(0, limit.value))
const favorite = computed(() => store.isFavorite(props.root, props.path))

const columns = computed<ColumnDef<FileEntry>[]>(() => [
  { id: 'name', header: t('common.field.name'), enableSorting: false, meta: { class: 'min-w-[240px]' } },
  { id: 'size', header: t('files.column.size'), enableSorting: false, meta: { class: 'w-[100px] text-end', headClass: 'text-end' } },
  { id: 'modTime', header: t('files.column.modified'), enableSorting: false, meta: { class: 'w-[150px]' } },
  { id: 'owner', header: t('files.column.owner'), enableSorting: false, meta: { class: 'w-[170px]' } },
  { id: 'mode', header: t('files.column.permissions'), enableSorting: false, meta: { class: 'w-[150px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[60px] text-end' } },
])

function iconOf(entry: FileEntry): { icon: Component; class: string } {
  if (entry.type === 'dir') return { icon: Folder, class: 'text-primary' }
  if (entry.type === 'link') return { icon: Link2, class: 'text-muted-foreground' }
  if (isImage(entry.name)) return { icon: FileImage, class: 'text-muted-foreground' }
  if (archiveKind(entry.name)) return { icon: FileArchive, class: 'text-warning' }
  return { icon: FileIcon, class: 'text-muted-foreground' }
}

function act(key: FileActionKey, entry: FileEntry) {
  emit('action', key, [entry])
}

function actions(entry: FileEntry): RowAction[] {
  const list: RowAction[] = []
  if (entry.type === 'dir' || entry.type === 'link') {
    list.push({ key: 'open', label: t('files.action.open'), icon: FolderOpen, onSelect: () => emit('open', entry) })
  }
  if (entry.type === 'file' || entry.type === 'link') {
    const image = isImage(entry.name)
    list.push({
      key: 'edit',
      label: image ? t('files.action.preview') : props.canManage ? t('files.action.edit') : t('files.action.view'),
      icon: image ? FileImage : Pencil,
      onSelect: () => act('edit', entry),
    })
  }
  if (entry.type === 'file') list.push({ key: 'download', label: t('files.action.download'), icon: Download, onSelect: () => act('download', entry) })
  if (props.canManage) {
    if (entry.type === 'file' && archiveKind(entry.name)) {
      list.push({ key: 'extract', label: t('files.action.extract'), icon: PackageOpen, onSelect: () => act('extract', entry) })
    }
    list.push(
      { key: 'rename', label: t('files.action.rename'), icon: TextCursorInput, onSelect: () => act('rename', entry) },
      { key: 'copy', label: t('files.action.copy'), icon: Copy, onSelect: () => act('copy', entry) },
      { key: 'move', label: t('files.action.move'), icon: FolderInput, onSelect: () => act('move', entry) },
      { key: 'compress', label: t('files.action.compress'), icon: Archive, onSelect: () => act('compress', entry) },
      { key: 'chmod', label: t('files.action.chmod'), icon: KeyRound, onSelect: () => act('chmod', entry) },
      { key: 'chown', label: t('files.action.chown'), icon: UserCog, onSelect: () => act('chown', entry) },
      { key: 'delete', label: t('files.action.delete'), icon: Trash2, destructive: true, onSelect: () => act('delete', entry) },
    )
  }
  return list
}

async function toggleFavorite() {
  try {
    await store.setFavorite(props.root, props.path, !favorite.value)
  } catch (err) {
    notifyError(err)
  }
}

async function dropFavorite(root: string, path: string) {
  try {
    await store.setFavorite(root, path, false)
  } catch (err) {
    notifyError(err)
  }
}

// Dropping files ----------------------------------------------------------------

function hasFiles(event: DragEvent): boolean {
  return props.canManage && !!event.dataTransfer?.types.includes('Files')
}

function onDragEnter(event: DragEvent) {
  if (!hasFiles(event)) return
  event.preventDefault()
  dragDepth.value++
}

function onDragOver(event: DragEvent) {
  if (!hasFiles(event)) return
  event.preventDefault()
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy'
}

function onDragLeave(event: DragEvent) {
  if (!hasFiles(event)) return
  dragDepth.value = Math.max(0, dragDepth.value - 1)
}

function onDrop(event: DragEvent) {
  dragDepth.value = 0
  if (!hasFiles(event)) return
  event.preventDefault()
  const files: File[] = []
  let folders = 0
  for (const item of Array.from(event.dataTransfer?.items ?? [])) {
    if (item.kind !== 'file') continue
    // Only files can be uploaded; dropped folders are left out.
    if (item.webkitGetAsEntry()?.isDirectory) {
      folders++
      continue
    }
    const file = item.getAsFile()
    if (file) files.push(file)
  }
  if (folders > 0) notifyErrorText(t('files.upload.noFolders'))
  emit('upload', files)
}

const toolButton = 'max-md:size-11'
</script>

<template>
  <section
    class="relative min-w-0 overflow-hidden rounded-lg border bg-card"
    :aria-label="t('files.folderLabel', { path: absolutePath(root, path) })"
    @dragenter="onDragEnter"
    @dragover="onDragOver"
    @dragleave="onDragLeave"
    @drop="onDrop"
  >
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b px-4 py-3">
      <div class="flex min-w-0 flex-[1_1_320px] items-center gap-1">
        <Button
          variant="ghost"
          size="icon"
          :class="toolButton"
          :disabled="!path"
          :aria-label="t('files.action.up')"
          :title="t('files.action.up')"
          @click="emit('navigate', parentPath(path))"
        >
          <ArrowUp aria-hidden="true" />
        </Button>
        <FileBreadcrumb :root="root" :path="path" @navigate="(next: string) => emit('navigate', next)" />
      </div>
      <div class="flex min-w-0 flex-[1_1_auto] flex-wrap items-center justify-end gap-1.5">
        <label
          class="flex h-9 min-w-0 flex-[1_1_180px] items-center gap-2 rounded-md border border-input bg-card px-2.5 text-muted-foreground focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/50 max-sm:basis-full sm:max-w-60 max-md:h-11"
        >
          <Search class="size-4 shrink-0" aria-hidden="true" />
          <input
            v-model="filter"
            type="search"
            :aria-label="t('files.filter')"
            :placeholder="t('files.filter')"
            class="h-full min-w-0 flex-1 bg-transparent text-base text-foreground outline-none placeholder:text-muted-foreground md:text-sm [&::-webkit-search-cancel-button]:hidden"
          />
          <button
            v-if="filter"
            type="button"
            class="inline-flex size-6 shrink-0 items-center justify-center rounded-sm outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
            :aria-label="t('common.table.clearSearch')"
            @click="filter = ''"
          >
            <X class="size-3.5" aria-hidden="true" />
          </button>
        </label>
        <Button
          variant="ghost"
          size="icon"
          :class="toolButton"
          :aria-pressed="favorite"
          :aria-label="favorite ? t('files.favorite.remove') : t('files.favorite.add')"
          :title="favorite ? t('files.favorite.remove') : t('files.favorite.add')"
          @click="toggleFavorite"
        >
          <Star :class="favorite ? 'fill-current text-warning' : ''" aria-hidden="true" />
        </Button>
        <Popover>
          <PopoverTrigger as-child>
            <Button variant="ghost" size="icon" :class="toolButton" :aria-label="t('files.favorite.list')" :title="t('files.favorite.list')">
              <Bookmark aria-hidden="true" />
            </Button>
          </PopoverTrigger>
          <PopoverContent align="end" class="w-[min(360px,calc(100vw-32px))] p-1.5">
            <p class="m-0 px-2 pt-1 pb-1.5 text-caption text-muted-foreground">{{ t('files.favorite.list') }}</p>
            <p v-if="store.favorites.length === 0" class="m-0 px-2 pb-2 text-detail text-muted-foreground">
              {{ t('files.favorite.none') }}
            </p>
            <ul v-else class="m-0 flex max-h-72 list-none flex-col overflow-y-auto p-0">
              <li v-for="item in store.favorites" :key="`${item.root}:${item.path}`" class="flex items-center gap-1">
                <button
                  type="button"
                  class="flex min-w-0 flex-1 flex-col rounded-sm px-2 py-1.5 text-start outline-none hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring/50 max-md:min-h-11"
                  @click="emit('go', item.root, item.path)"
                >
                  <span class="truncate text-detail font-medium">{{ item.path ? baseName(item.path) : rootLabel(item.root) }}</span>
                  <span class="truncate font-mono text-xs text-muted-foreground" dir="ltr">{{ absolutePath(item.root, item.path) }}</span>
                </button>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  class="max-md:size-11"
                  :aria-label="t('files.favorite.removeNamed', { path: absolutePath(item.root, item.path) })"
                  :title="t('files.favorite.remove')"
                  @click="dropFavorite(item.root, item.path)"
                >
                  <X aria-hidden="true" />
                </Button>
              </li>
            </ul>
          </PopoverContent>
        </Popover>
        <Button
          variant="ghost"
          size="icon"
          :class="cn(toolButton, store.hidden && 'bg-accent')"
          :aria-pressed="store.hidden"
          :aria-label="t('files.hidden')"
          :title="t('files.hidden')"
          @click="store.hidden = !store.hidden"
        >
          <Eye v-if="store.hidden" aria-hidden="true" />
          <EyeOff v-else aria-hidden="true" />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          :class="toolButton"
          :disabled="store.listLoading"
          :aria-label="t('common.action.refresh')"
          :title="t('common.action.refresh')"
          @click="store.loadList()"
        >
          <RefreshCw :class="store.listLoading ? 'animate-spin' : ''" aria-hidden="true" />
        </Button>
      </div>
    </div>

    <UploadsPanel />

    <div v-if="store.list?.truncated" class="border-b px-4 py-2.5">
      <Notice tone="info">{{ t('files.truncated', { max: formatNumber(LIST_MAX) }) }}</Notice>
    </div>

    <DataTable
      v-model:selection="selection"
      flat
      row-clickable
      :data="rows"
      :columns="columns"
      :row-key="(entry) => entry.path"
      :label="t('files.tableLabel')"
      :loading="store.listLoading && !store.list"
      :error="store.listError"
      :selectable="canManage"
      :empty-title="filter ? t('common.table.noMatches', { query: filter.trim() }) : t('files.empty')"
      :empty-text="!filter && canManage ? t('files.dropHint') : undefined"
      :min-width="860"
      :skeleton-rows="6"
      @row-click="(entry: FileEntry) => emit('open', entry)"
      @retry="store.loadList()"
    >
      <template #cell-name="{ row }">
        <span class="flex min-w-0 items-center gap-2.5">
          <component :is="iconOf(row).icon" :class="cn('size-[18px] shrink-0', iconOf(row).class)" aria-hidden="true" />
          <span class="flex min-w-0 flex-col">
            <button
              type="button"
              :class="
                cn(
                  'max-w-full truncate rounded-sm text-start outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50',
                  row.type === 'dir' ? 'font-semibold' : 'font-medium',
                )
              "
              dir="auto"
              @click="emit('open', row)"
            >
              {{ row.name }}
            </button>
            <span v-if="row.type === 'link' && row.target" class="truncate font-mono text-xs text-muted-foreground" dir="ltr" :title="row.target">
              → {{ row.target }}
            </span>
          </span>
        </span>
      </template>
      <template #cell-size="{ row }">
        <span class="whitespace-nowrap text-muted-foreground tabular-nums">{{ row.type === 'file' ? formatBytes(row.size) : '—' }}</span>
      </template>
      <template #cell-modTime="{ row }">
        <RelativeTime :value="row.modTime" class="text-muted-foreground" />
      </template>
      <template #cell-owner="{ row }">
        <span class="block truncate font-mono text-[12.5px]" dir="ltr" :title="`${row.owner}:${row.group}`">{{ row.owner }}:{{ row.group }}</span>
      </template>
      <template #cell-mode="{ row }">
        <component
          :is="canManage ? 'button' : 'span'"
          :type="canManage ? 'button' : undefined"
          :class="
            cn(
              'inline-flex items-baseline gap-2 font-mono text-[12.5px] whitespace-nowrap',
              canManage && 'rounded-sm outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50',
            )
          "
          :title="canManage ? t('files.action.chmod') : undefined"
          dir="ltr"
          @click="canManage && act('chmod', row)"
        >
          <span>{{ modeString(row.type, row.mode) }}</span>
          <span class="text-muted-foreground">{{ octal(row.mode) }}</span>
        </component>
      </template>
      <template #cell-actions="{ row }">
        <div class="flex justify-end">
          <RowActions :actions="actions(row)" :label="t('files.action.more', { name: row.name })" />
        </div>
      </template>
      <template v-if="canManage && !filter" #empty>
        <Button variant="outline" @click="emit('choose')">
          <Upload aria-hidden="true" />
          {{ t('files.action.upload') }}
        </Button>
      </template>
      <template #bulk="{ rows: picked }">
        <Button variant="outline" size="sm" class="max-md:h-11" @click="emit('action', 'copy', picked)">
          <Copy aria-hidden="true" />
          {{ t('files.action.copy') }}
        </Button>
        <Button variant="outline" size="sm" class="max-md:h-11" @click="emit('action', 'move', picked)">
          <FolderInput aria-hidden="true" />
          {{ t('files.action.move') }}
        </Button>
        <Button variant="outline" size="sm" class="max-md:h-11" @click="emit('action', 'compress', picked)">
          <Archive aria-hidden="true" />
          {{ t('files.action.compress') }}
        </Button>
        <Button variant="outline" size="sm" class="max-md:h-11" @click="emit('action', 'chmod', picked)">
          <KeyRound aria-hidden="true" />
          {{ t('files.action.chmod') }}
        </Button>
        <Button variant="outline" size="sm" class="max-md:h-11" @click="emit('action', 'chown', picked)">
          <UserCog aria-hidden="true" />
          {{ t('files.action.chown') }}
        </Button>
        <span class="mx-1 h-5 w-px bg-border max-sm:hidden" aria-hidden="true" />
        <Button
          variant="outline"
          size="sm"
          class="text-destructive hover:bg-destructive-soft hover:text-destructive max-md:h-11"
          @click="emit('action', 'delete', picked)"
        >
          <Trash2 aria-hidden="true" />
          {{ t('files.action.delete') }}
        </Button>
      </template>
    </DataTable>

    <div
      v-if="matching.length > rows.length"
      class="flex flex-wrap items-center justify-center gap-3 border-t px-4 py-3 text-detail text-muted-foreground"
    >
      <span>{{ t('files.showing', { shown: formatNumber(rows.length), total: formatNumber(matching.length) }) }}</span>
      <Button variant="outline" size="sm" class="max-md:h-11" @click="limit += PAGE">{{ t('files.showMore') }}</Button>
    </div>

    <div
      v-if="dragDepth > 0"
      class="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-lg border-2 border-dashed border-primary bg-primary-soft/85 p-6 text-center font-medium text-primary-soft-foreground"
      aria-hidden="true"
    >
      <span class="flex flex-col items-center gap-2">
        <Upload class="size-6" />
        <span class="[overflow-wrap:anywhere]">{{ t('files.dropHere', { dir: absolutePath(root, path) }) }}</span>
      </span>
    </div>
  </section>
</template>
