<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// File manager: browse a root, change entries (most changes run as jobs),
// upload, edit text files and use the trash. Where the user is lives in the
// address (?root=&path=&trash=1), so reloads and links keep the place.
import { computed, h, onBeforeUnmount, onMounted, ref, watch, type VNodeChild } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
  NAlert,
  NBreadcrumb,
  NBreadcrumbItem,
  NButton,
  NDataTable,
  NDropdown,
  NEmpty,
  NH2,
  NIcon,
  NSpace,
  NSpin,
  NSwitch,
  NText,
  NTooltip,
  useMessage,
  type DataTableColumns,
  type DropdownOption,
} from 'naive-ui'
import {
  AddOutline,
  ArrowUpOutline,
  CloudDownloadOutline,
  CloudUploadOutline,
  DocumentOutline,
  EllipsisHorizontal,
  FolderOutline,
  ImageOutline,
  LinkOutline,
  RefreshOutline,
  SearchOutline,
  Star,
  StarOutline,
} from '@vicons/ionicons5'
import { isAbortError, isApiError, isStepUpRequired } from '@/api/client'
import {
  FILE_BATCH_MAX,
  UPLOAD_MAX,
  addFavorite,
  downloadUrl,
  fetchFavorites,
  fetchRoots,
  listDir,
  makeDir,
  removeFavorite,
  renameEntry,
  setFullDisk,
  touchFile,
  type FileEntry,
  type FileList,
  type FileMatch,
  type FileRef,
  type FileRoots,
} from '@/api/files'
import { followJobLog, type Job } from '@/api/jobs'
import JobProgress from '@/components/JobProgress.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { useJobRun } from '@/composables/useJobRun'
import { modeString, octal } from '@/files/modes'
import {
  absolutePath,
  archiveKind,
  isImage,
  joinPath,
  parentPath,
  pathSegments,
  rememberRoots,
  rootLabel,
} from '@/files/paths'
import { errorMessage } from '@/i18n/errors'
import { formatBytes, formatDateTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'
import { useUploadsStore } from '@/stores/uploads'
import ChmodDialog from './files/ChmodDialog.vue'
import ChownDialog from './files/ChownDialog.vue'
import CompressDialog from './files/CompressDialog.vue'
import DeleteDialog from './files/DeleteDialog.vue'
import ExtractDialog from './files/ExtractDialog.vue'
import FetchDialog from './files/FetchDialog.vue'
import FileEditor from './files/FileEditor.vue'
import FilesSidebar from './files/FilesSidebar.vue'
import ImagePreview from './files/ImagePreview.vue'
import NameDialog from './files/NameDialog.vue'
import SearchDrawer from './files/SearchDrawer.vue'
import TransferDialog from './files/TransferDialog.vue'
import TrashPanel from './files/TrashPanel.vue'
import UploadsPanel from './files/UploadsPanel.vue'

const HIDDEN_KEY = 'cairn.files.hidden'
/** Most entries the server lists in one directory. */
const LIST_MAX = 5000
/** Rows per page of a long directory. */
const PAGE_SIZE = 200

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()
const session = useSessionStore()
const uploads = useUploadsStore()

const canManage = computed(() => session.can('files.manage'))
const canAdmin = computed(() => session.can('files.admin'))

const roots = ref<FileRoots | null>(null)
const rootsError = ref<unknown>(null)
const favorites = ref<FileRef[]>([])
const list = ref<FileList | null>(null)
const loading = ref(false)
const loadError = ref<unknown>(null)
const checked = ref<string[]>([])
const hidden = ref(readHidden())
const dragging = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
let controller: AbortController | null = null

function readHidden(): boolean {
  try {
    return window.localStorage.getItem(HIDDEN_KEY) === '1'
  } catch {
    return false
  }
}

watch(hidden, (value) => {
  try {
    window.localStorage.setItem(HIDDEN_KEY, value ? '1' : '0')
  } catch {
    // Not remembered; the switch still works for this visit.
  }
  void load()
})

// Where we are -----------------------------------------------------------------

function queryString(name: string): string {
  const value = route.query[name]
  return typeof value === 'string' ? value : ''
}

/** The root asked for, or the first one that is available. */
const root = computed(() => {
  const asked = queryString('root')
  const available = roots.value?.roots.filter((entry) => entry.available) ?? []
  if (asked && (!roots.value || available.some((entry) => entry.id === asked))) return asked
  return available[0]?.id ?? 'sites'
})
const path = computed(() => queryString('path'))
const inTrash = computed(() => queryString('trash') === '1')
const segments = computed(() => pathSegments(path.value))

function go(nextRoot: string, nextPath: string) {
  void router.push({ query: { root: nextRoot, ...(nextPath ? { path: nextPath } : {}) } })
}

function openTrash() {
  void router.push({ query: { root: root.value, trash: '1' } })
}

async function loadRoots() {
  try {
    roots.value = await fetchRoots()
    rememberRoots(roots.value.roots)
    rootsError.value = null
  } catch (err) {
    rootsError.value = err
  }
}

async function loadFavorites() {
  try {
    favorites.value = await fetchFavorites()
  } catch {
    // The list of favourites is a convenience; browsing works without it.
  }
}

async function load() {
  if (inTrash.value || !roots.value) return
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  try {
    list.value = await listDir(root.value, path.value, hidden.value, current.signal)
    loadError.value = null
    const paths = new Set(list.value.entries.map((entry) => entry.path))
    checked.value = checked.value.filter((p) => paths.has(p))
  } catch (err) {
    if (isAbortError(err)) return
    list.value = null
    loadError.value = err
  } finally {
    if (controller === current) loading.value = false
  }
}

watch([root, path, inTrash], ([, , trash], [, oldPath]) => {
  if (oldPath !== undefined) checked.value = []
  if (!trash) void load()
})

onMounted(async () => {
  await Promise.all([loadRoots(), loadFavorites()])
  void load()
})
onBeforeUnmount(() => controller?.abort())

// An upload into this directory landed.
watch(
  () => uploads.placedVersion,
  () => {
    const placed = uploads.lastPlaced
    if (placed && placed.root === root.value && placed.dir === path.value) void load()
  },
)

const entries = computed(() => list.value?.entries ?? [])
const selected = computed(() => entries.value.filter((entry) => checked.value.includes(entry.path)))

// Full-disk access ------------------------------------------------------------

const settings = computed(() => roots.value?.settings ?? null)
const switching = ref(false)

async function toggleFullDisk(enabled: boolean) {
  if (enabled) {
    const ok = await confirm({
      title: t('files.fullDisk.enableTitle'),
      content: t('files.fullDisk.enableText'),
      confirmText: t('files.fullDisk.enable'),
      danger: true,
    })
    if (!ok) return
  }
  switching.value = true
  try {
    await setFullDisk(enabled)
    await loadRoots()
    if (!enabled && root.value === 'system') go('sites', '')
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  } finally {
    switching.value = false
  }
}

// Favourites -----------------------------------------------------------------

const isFavorite = computed(() => favorites.value.some((f) => f.root === root.value && f.path === path.value))

async function toggleFavorite() {
  try {
    if (isFavorite.value) await removeFavorite(root.value, path.value)
    else await addFavorite(root.value, path.value)
    await loadFavorites()
  } catch (err) {
    showError(err)
  }
}

async function dropFavorite(favorite: FileRef) {
  try {
    await removeFavorite(favorite.root, favorite.path)
    await loadFavorites()
  } catch (err) {
    showError(err)
  }
}

// Jobs -------------------------------------------------------------------------

interface DeleteRequest {
  root: string
  paths: string[]
}

/** What each delete job here was asked to move to the trash. */
const deletes = new Map<string, DeleteRequest>()

const { jobId, busy, follow, finished, dismiss } = useJobRun((job: Job) => {
  void load()
  const request = deletes.get(job.id)
  deletes.delete(job.id)
  if (request) void offerPermanent(job, request)
})

function deleteStarted(job: Job, permanent: boolean) {
  if (!permanent) deletes.set(job.id, { root: remove.value.root, paths: [...remove.value.paths] })
  follow(job)
}

/** Paths a delete job could not move to the trash because it is on another file system. */
async function crossDevicePaths(job: Job, request: DeleteRequest): Promise<string[]> {
  // Nothing went to the trash, all for this reason: the job failed with it.
  if (job.state === 'failed') return job.error?.code === 'file.cross_device' ? request.paths : []
  if (job.state !== 'succeeded') return []
  // Some entries went: the log names the others ("! <path>: <code>").
  try {
    const { log } = await followJobLog(job.id, 0, 0)
    const suffix = ': file.cross_device'
    return log
      .split('\n')
      .filter((line) => line.startsWith('! ') && line.endsWith(suffix))
      .map((line) => line.slice(2, -suffix.length))
  } catch {
    return []
  }
}

async function offerPermanent(job: Job, request: DeleteRequest) {
  const paths = await crossDevicePaths(job, request)
  if (paths.length === 0) return
  const ok = await confirm({
    title: t('files.delete.crossDeviceTitle', { count: paths.length }, paths.length),
    content: t('files.delete.crossDeviceText'),
    confirmText: t('files.delete.submitPermanent'),
    danger: true,
  })
  if (ok) openDelete(paths, true, request.root)
}

// Dialogs ------------------------------------------------------------------------

interface NameDialogState {
  show: boolean
  title: string
  initial: string
  submitText: string
  action: (name: string) => Promise<void>
}

const nameDialog = ref<NameDialogState>({ show: false, title: '', initial: '', submitText: '', action: async () => {} })
const transfer = ref<{ show: boolean; mode: 'copy' | 'move'; paths: string[] }>({ show: false, mode: 'copy', paths: [] })
const chmod = ref<{ show: boolean; entries: FileEntry[] }>({ show: false, entries: [] })
const chown = ref<{ show: boolean; entries: FileEntry[] }>({ show: false, entries: [] })
const compress = ref<{ show: boolean; names: string[] }>({ show: false, names: [] })
const extract = ref<{ show: boolean; entry: FileEntry | null }>({ show: false, entry: null })
const remove = ref<{ show: boolean; root: string; paths: string[]; permanent: boolean }>({ show: false, root: '', paths: [], permanent: false })
const showFetch = ref(false)
const showSearch = ref(false)
const editor = ref<{ path: string | null; line?: number }>({ path: null })
const preview = ref<string | null>(null)

function newFolder() {
  const dir = path.value
  nameDialog.value = {
    show: true,
    title: t('files.newFolder.title'),
    initial: '',
    submitText: t('common.action.create'),
    action: async (name) => {
      await makeDir(root.value, dir, name)
      await load()
    },
  }
}

function newFile() {
  const dir = path.value
  nameDialog.value = {
    show: true,
    title: t('files.newFile.title'),
    initial: '',
    submitText: t('common.action.create'),
    action: async (name) => {
      await touchFile(root.value, dir, name)
      await load()
      editor.value = { path: joinPath(dir, name) }
    },
  }
}

function rename(entry: FileEntry) {
  nameDialog.value = {
    show: true,
    title: t('files.rename.title', { name: entry.name }),
    initial: entry.name,
    submitText: t('files.rename.submit'),
    action: async (name) => {
      if (name !== entry.name) await renameEntry(root.value, entry.path, name)
      await load()
    },
  }
}

function openTransfer(mode: 'copy' | 'move', paths: string[]) {
  transfer.value = { show: true, mode, paths }
}

function openDelete(paths: string[], permanent = false, inRoot = root.value) {
  remove.value = { show: true, root: inRoot, paths, permanent }
}

/** Selections larger than one operation takes are refused up front. */
function batch(paths: string[]): string[] | null {
  if (paths.length <= FILE_BATCH_MAX) return paths
  message.warning(t('files.selection.tooMany', { max: FILE_BATCH_MAX }))
  return null
}

function selectionAction(action: 'copy' | 'move' | 'compress' | 'chmod' | 'chown' | 'delete') {
  const paths = batch(selected.value.map((entry) => entry.path))
  if (!paths) return
  switch (action) {
    case 'copy':
    case 'move':
      openTransfer(action, paths)
      break
    case 'compress':
      compress.value = { show: true, names: selected.value.map((entry) => entry.name) }
      break
    case 'chmod':
      chmod.value = { show: true, entries: [...selected.value] }
      break
    case 'chown':
      chown.value = { show: true, entries: [...selected.value] }
      break
    case 'delete':
      openDelete(paths)
      break
  }
}

// Opening entries --------------------------------------------------------------------

function openFile(entry: FileEntry, line?: number) {
  if (isImage(entry.name)) preview.value = entry.path
  else editor.value = { path: entry.path, line }
}

async function openEntry(entry: FileEntry) {
  if (entry.type === 'dir') {
    go(root.value, entry.path)
    return
  }
  if (entry.type === 'link') {
    // A link to a directory opens it; a link to a file opens the file.
    try {
      await listDir(root.value, entry.path, false)
      go(root.value, entry.path)
    } catch (err) {
      if (isApiError(err) && err.code === 'file.not_dir') openFile(entry)
      else showError(err)
    }
    return
  }
  if (entry.type !== 'file') return
  if (archiveKind(entry.name) && canManage.value) extract.value = { show: true, entry }
  else openFile(entry)
}

function pickMatch(match: FileMatch) {
  showSearch.value = false
  if (match.type === 'dir') {
    go(root.value, match.path)
    return
  }
  go(root.value, parentPath(match.path))
  if (isImage(match.path)) preview.value = match.path
  else editor.value = { path: match.path, line: match.line }
}

// Uploads ------------------------------------------------------------------------------

function chooseFiles() {
  fileInput.value?.click()
}

function onFilesChosen(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  input.value = ''
  void startUpload(files)
}

async function startUpload(files: File[]) {
  if (files.length === 0 || !canManage.value) return
  const tooLarge = files.filter((file) => file.size > UPLOAD_MAX)
  if (tooLarge.length > 0) {
    message.error(t('files.upload.tooLarge', { name: tooLarge.map((file) => file.name).join(', '), size: formatBytes(UPLOAD_MAX) }))
  }
  const accepted = files.filter((file) => file.size <= UPLOAD_MAX)
  if (accepted.length === 0) return
  const existing = new Set(entries.value.map((entry) => entry.name))
  const conflicts = accepted.filter((file) => existing.has(file.name)).map((file) => file.name)
  if (conflicts.length > 0) {
    const ok = await confirm({
      title: t('files.upload.existsTitle', { count: conflicts.length }, conflicts.length),
      content: () => h('div', { class: 'conflicts' }, conflicts.map((name) => h('div', { class: 'mono' }, name))),
      confirmText: t('files.upload.replace'),
    })
    if (!ok) return
  }
  const replace = new Set(conflicts)
  uploads.add(root.value, path.value, accepted, (name) => replace.has(name))
}

function onDragOver(event: DragEvent) {
  if (!canManage.value || inTrash.value || !event.dataTransfer?.types.includes('Files')) return
  event.preventDefault()
  dragging.value = true
}

function onDrop(event: DragEvent) {
  dragging.value = false
  if (!canManage.value || inTrash.value) return
  event.preventDefault()
  // Only files can be uploaded; dropped folders are left out.
  const files: File[] = []
  let folders = 0
  for (const item of Array.from(event.dataTransfer?.items ?? [])) {
    if (item.kind !== 'file') continue
    if (item.webkitGetAsEntry()?.isDirectory) {
      folders++
      continue
    }
    const file = item.getAsFile()
    if (file) files.push(file)
  }
  if (folders > 0) message.warning(t('files.upload.noFolders'))
  void startUpload(files)
}

// Table ---------------------------------------------------------------------------------

function iconFor(entry: FileEntry) {
  if (entry.type === 'dir') return FolderOutline
  if (entry.type === 'link') return LinkOutline
  if (isImage(entry.name)) return ImageOutline
  return DocumentOutline
}

function rowOptions(entry: FileEntry): DropdownOption[] {
  const options: DropdownOption[] = []
  const manage = canManage.value
  if (entry.type === 'dir' || entry.type === 'link') options.push({ key: 'open', label: t('files.action.open') })
  if (entry.type === 'file' || entry.type === 'link') {
    options.push({ key: 'edit', label: isImage(entry.name) ? t('files.action.preview') : t('files.action.edit') })
  }
  if (entry.type === 'file') options.push({ key: 'download', label: t('files.action.download') })
  if (manage && entry.type === 'file' && archiveKind(entry.name)) options.push({ key: 'extract', label: t('files.action.extract') })
  if (manage) {
    options.push(
      { type: 'divider', key: 'd1' },
      { key: 'rename', label: t('files.action.rename') },
      { key: 'copy', label: t('files.action.copy') },
      { key: 'move', label: t('files.action.move') },
      { key: 'compress', label: t('files.action.compress') },
      { key: 'chmod', label: t('files.action.chmod') },
      { key: 'chown', label: t('files.action.chown') },
      { type: 'divider', key: 'd2' },
      { key: 'delete', label: t('common.action.delete'), props: { style: 'color: var(--n-option-text-color-error, #d03050)' } },
    )
  }
  return options
}

function onRowAction(key: string, entry: FileEntry) {
  switch (key) {
    case 'open':
      void openEntry(entry)
      break
    case 'edit':
      openFile(entry)
      break
    case 'download':
      window.location.assign(downloadUrl(root.value, entry.path))
      break
    case 'extract':
      extract.value = { show: true, entry }
      break
    case 'rename':
      rename(entry)
      break
    case 'copy':
    case 'move':
      openTransfer(key, [entry.path])
      break
    case 'compress':
      compress.value = { show: true, names: [entry.name] }
      break
    case 'chmod':
      chmod.value = { show: true, entries: [entry] }
      break
    case 'chown':
      chown.value = { show: true, entries: [entry] }
      break
    case 'delete':
      openDelete([entry.path])
      break
  }
}

function nameCell(entry: FileEntry): VNodeChild {
  return h('span', { class: 'entry' }, [
    h(NIcon, { size: 18, class: `entry__icon entry__icon--${entry.type}` }, () => h(iconFor(entry))),
    h('button', { type: 'button', class: 'entry__name', onClick: () => void openEntry(entry) }, entry.name),
    entry.type === 'link' && entry.target ? h('span', { class: 'entry__target', dir: 'ltr' }, `→ ${entry.target}`) : null,
  ])
}

const columns = computed<DataTableColumns<FileEntry>>(() => [
  ...(canManage.value ? [{ type: 'selection' as const }] : []),
  { key: 'name', title: t('common.field.name'), minWidth: 240, ellipsis: false, render: nameCell },
  {
    key: 'size',
    title: t('files.column.size'),
    width: 100,
    render: (entry) => (entry.type === 'file' ? formatBytes(entry.size) : '-'),
  },
  {
    key: 'mode',
    title: t('files.column.permissions'),
    width: 150,
    render: (entry) =>
      h(
        canManage.value ? 'button' : 'span',
        {
          type: canManage.value ? 'button' : undefined,
          class: ['mode', canManage.value ? 'mode--edit' : ''],
          title: canManage.value ? t('files.action.chmod') : undefined,
          onClick: canManage.value ? () => (chmod.value = { show: true, entries: [entry] }) : undefined,
        },
        [h('span', { class: 'mono' }, octal(entry.mode)), h('span', { class: 'mono mode__text' }, modeString(entry.type, entry.mode))],
      ),
  },
  { key: 'owner', title: t('files.column.owner'), width: 150, render: (entry) => h('span', { class: 'mono' }, `${entry.owner}:${entry.group}`) },
  { key: 'modTime', title: t('files.column.modified'), width: 170, render: (entry) => formatDateTime(entry.modTime) },
  {
    key: 'actions',
    title: '',
    width: 56,
    render: (entry) =>
      h(
        NDropdown,
        { trigger: 'click', options: rowOptions(entry), onSelect: (key: string | number) => onRowAction(String(key), entry) },
        () =>
          h(NButton, { size: 'small', quaternary: true, 'aria-label': t('files.action.more', { name: entry.name }) }, {
            icon: () => h(NIcon, null, () => h(EllipsisHorizontal)),
          }),
      ),
  },
])

/** Long directories are shown in pages, so thousands of rows do not render at once. */
const pagination = computed(() => (entries.value.length > PAGE_SIZE ? { pageSize: PAGE_SIZE } : false))

const rowProps = (entry: FileEntry) => ({
  onDblclick: (event: MouseEvent) => {
    // Double-clicking anywhere on a row opens it (but not on its controls).
    if ((event.target as HTMLElement).closest('button, a, input, .n-checkbox')) return
    void openEntry(entry)
  },
})
</script>

<template>
  <div class="files">
    <div class="files__header">
      <NH2 class="files__title">{{ t('files.title') }}</NH2>
      <label v-if="canAdmin && settings" class="full-disk">
        <NSwitch
          :value="settings.fullDisk"
          size="small"
          :disabled="settings.locked || switching"
          :loading="switching"
          @update:value="toggleFullDisk"
        />
        <span>{{ t('files.fullDisk.label') }}</span>
      </label>
    </div>

    <NAlert v-if="rootsError" type="error" class="block">{{ errorMessage(rootsError) }}</NAlert>
    <NAlert v-if="canAdmin && settings?.locked" type="info" class="block">{{ t('files.fullDisk.locked') }}</NAlert>
    <NAlert v-if="settings?.fullDisk && root === 'system'" type="warning" class="block">{{ t('files.fullDisk.active') }}</NAlert>

    <UploadsPanel />

    <div v-if="roots" class="files__body">
      <aside class="files__side">
        <FilesSidebar
          :roots="roots.roots"
          :root="root"
          :path="path"
          :trash="inTrash"
          :favorites="favorites"
          @go="go"
          @open-trash="openTrash"
          @remove-favorite="dropFavorite"
        />
      </aside>

      <main class="files__main">
        <template v-if="inTrash">
          <div class="crumbs">
            <NButton size="small" quaternary @click="go(root, '')">
              <template #icon>
                <NIcon><ArrowUpOutline /></NIcon>
              </template>
              {{ t('files.trash.back', { root: rootLabel(root) }) }}
            </NButton>
          </div>
          <TrashPanel :root="root" :can-manage="canManage" />
        </template>

        <template v-else>
          <div class="crumbs">
            <NButton size="small" quaternary :disabled="!path" :aria-label="t('files.action.up')" @click="go(root, parentPath(path))">
              <template #icon>
                <NIcon><ArrowUpOutline /></NIcon>
              </template>
            </NButton>
            <NBreadcrumb class="crumbs__trail">
              <NBreadcrumbItem @click="go(root, '')">{{ rootLabel(root) }}</NBreadcrumbItem>
              <NBreadcrumbItem v-for="segment in segments" :key="segment.path" @click="go(root, segment.path)">
                {{ segment.name }}
              </NBreadcrumbItem>
            </NBreadcrumb>
            <NTooltip>
              <template #trigger>
                <NButton size="small" quaternary circle :aria-label="isFavorite ? t('files.favorite.remove') : t('files.favorite.add')" @click="toggleFavorite">
                  <template #icon>
                    <NIcon :color="isFavorite ? '#f0a020' : undefined">
                      <Star v-if="isFavorite" />
                      <StarOutline v-else />
                    </NIcon>
                  </template>
                </NButton>
              </template>
              {{ isFavorite ? t('files.favorite.remove') : t('files.favorite.add') }}
            </NTooltip>
          </div>
          <NText depth="3" tag="div" class="where" dir="ltr">{{ absolutePath(root, path) }}</NText>

          <div class="toolbar">
            <NSpace :size="6" align="center">
              <template v-if="canManage">
                <NButton size="small" @click="newFolder">
                  <template #icon>
                    <NIcon><AddOutline /></NIcon>
                  </template>
                  {{ t('files.action.newFolder') }}
                </NButton>
                <NButton size="small" @click="newFile">{{ t('files.action.newFile') }}</NButton>
                <NButton size="small" type="primary" @click="chooseFiles">
                  <template #icon>
                    <NIcon><CloudUploadOutline /></NIcon>
                  </template>
                  {{ t('files.action.upload') }}
                </NButton>
                <NButton size="small" @click="showFetch = true">
                  <template #icon>
                    <NIcon><CloudDownloadOutline /></NIcon>
                  </template>
                  {{ t('files.action.fetch') }}
                </NButton>
              </template>
              <NButton size="small" @click="showSearch = true">
                <template #icon>
                  <NIcon><SearchOutline /></NIcon>
                </template>
                {{ t('files.action.search') }}
              </NButton>
            </NSpace>
            <NSpace :size="10" align="center">
              <label class="toggle">
                <NSwitch v-model:value="hidden" size="small" />
                <span>{{ t('files.hidden') }}</span>
              </label>
              <NButton size="small" quaternary :loading="loading" :aria-label="t('files.action.refresh')" @click="load">
                <template #icon>
                  <NIcon><RefreshOutline /></NIcon>
                </template>
              </NButton>
            </NSpace>
          </div>

          <div v-if="canManage && selected.length > 0" class="selection">
            <NText strong>{{ t('files.selection.selected', { count: selected.length }) }}</NText>
            <NSpace :size="4">
              <NButton size="small" @click="selectionAction('copy')">{{ t('files.action.copy') }}</NButton>
              <NButton size="small" @click="selectionAction('move')">{{ t('files.action.move') }}</NButton>
              <NButton size="small" @click="selectionAction('compress')">{{ t('files.action.compress') }}</NButton>
              <NButton size="small" @click="selectionAction('chmod')">{{ t('files.action.chmod') }}</NButton>
              <NButton size="small" @click="selectionAction('chown')">{{ t('files.action.chown') }}</NButton>
              <NButton size="small" type="error" ghost @click="selectionAction('delete')">{{ t('common.action.delete') }}</NButton>
              <NButton size="small" quaternary @click="checked = []">{{ t('files.selection.clear') }}</NButton>
            </NSpace>
          </div>

          <div v-if="jobId" class="job">
            <JobProgress :job-id="jobId" @finished="finished" />
            <NButton size="small" quaternary :disabled="busy" @click="dismiss">{{ t('sites.create.close') }}</NButton>
          </div>

          <NAlert v-if="list?.truncated" type="info" class="block">{{ t('files.truncated', { max: LIST_MAX }) }}</NAlert>
          <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>

          <div
            class="drop"
            :class="{ 'drop--active': dragging }"
            @dragover="onDragOver"
            @dragleave="dragging = false"
            @drop="onDrop"
          >
            <NDataTable
              v-if="!loadError"
              v-model:checked-row-keys="checked"
              :columns="columns"
              :data="entries"
              :row-key="(entry: FileEntry) => entry.path"
              :row-props="rowProps"
              :loading="loading"
              :pagination="pagination"
              :scroll-x="860"
              size="small"
            >
              <template #empty>
                <NEmpty :description="t('files.empty')">
                  <template v-if="canManage" #extra>
                    <NText depth="3">{{ t('files.dropHint') }}</NText>
                  </template>
                </NEmpty>
              </template>
            </NDataTable>
            <div v-if="dragging" class="drop__overlay">{{ t('files.dropHere', { dir: absolutePath(root, path) }) }}</div>
          </div>
        </template>
      </main>
    </div>
    <NSpin v-else-if="!rootsError" class="files__wait" />

    <input ref="fileInput" type="file" multiple hidden @change="onFilesChosen" />

    <NameDialog
      v-model:show="nameDialog.show"
      :title="nameDialog.title"
      :initial="nameDialog.initial"
      :submit-text="nameDialog.submitText"
      :action="nameDialog.action"
    />
    <TransferDialog
      v-model:show="transfer.show"
      :mode="transfer.mode"
      :root="root"
      :dir="path"
      :paths="transfer.paths"
      @started="follow"
    />
    <ChmodDialog v-model:show="chmod.show" :root="root" :entries="chmod.entries" @started="follow" />
    <ChownDialog v-model:show="chown.show" :root="root" :entries="chown.entries" @started="follow" />
    <CompressDialog v-model:show="compress.show" :root="root" :dir="path" :names="compress.names" @started="follow" />
    <ExtractDialog
      v-model:show="extract.show"
      :root="root"
      :entry="extract.entry"
      :archivers="roots?.archivers ?? { sevenZip: false, rar: false }"
      @started="follow"
    />
    <DeleteDialog
      v-model:show="remove.show"
      :root="remove.root"
      :paths="remove.paths"
      :permanent="remove.permanent"
      @started="deleteStarted"
    />
    <FetchDialog v-model:show="showFetch" :root="root" :dir="path" @started="follow" />
    <SearchDrawer v-model:show="showSearch" :root="root" :dir="path" @pick="pickMatch" />
    <FileEditor
      :root="root"
      :path="editor.path"
      :line="editor.line"
      :can-manage="canManage"
      @close="editor = { path: null }"
      @saved="load"
    />
    <ImagePreview :root="root" :path="preview" @close="preview = null" />
  </div>
</template>

<style scoped>
.files {
  max-inline-size: 1400px;
}

.files__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 16px;
  margin-block-end: 12px;
}

.files__title {
  margin: 0;
}

.full-disk,
.toggle {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  font-size: 13px;
}

.block {
  margin-block-end: 12px;
}

.files__body {
  display: grid;
  grid-template-columns: 220px minmax(0, 1fr);
  gap: 20px;
  align-items: start;
}

.files__side {
  position: sticky;
  inset-block-start: 12px;
}

.files__main {
  min-inline-size: 0;
}

.files__wait {
  display: flex;
  justify-content: center;
  padding-block: 64px;
}

.crumbs {
  display: flex;
  align-items: center;
  gap: 4px;
  min-inline-size: 0;
}

.crumbs__trail {
  flex: 1;
  min-inline-size: 0;
  overflow-wrap: anywhere;
}

.where {
  margin-block: 2px 10px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  overflow-wrap: anywhere;
}

.toolbar {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  gap: 8px;
  margin-block-end: 10px;
}

.selection {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
  margin-block-end: 10px;
  padding: 6px 10px;
  border-radius: 3px;
  background: rgba(24, 160, 88, 0.08);
}

.job {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 4px;
  margin-block-end: 12px;
}

.job > :first-child {
  align-self: stretch;
}

.drop {
  position: relative;
}

.drop__overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  border: 2px dashed #18a058;
  border-radius: 4px;
  background: rgba(24, 160, 88, 0.08);
  text-align: center;
  pointer-events: none;
}

:deep(.entry) {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  min-inline-size: 0;
}

:deep(.entry__icon--dir) {
  color: #f0a020;
}

:deep(.entry__name) {
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: start;
  cursor: pointer;
  overflow-wrap: anywhere;
}

:deep(.entry__name:hover),
:deep(.entry__name:focus-visible) {
  text-decoration: underline;
}

:deep(.entry__target) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  opacity: 0.65;
  overflow-wrap: anywhere;
}

:deep(.mode) {
  display: inline-flex;
  gap: 8px;
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
}

:deep(.mode--edit) {
  cursor: pointer;
}

:deep(.mode--edit:hover) {
  text-decoration: underline;
}

:deep(.mode__text) {
  opacity: 0.6;
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
}

@media (max-width: 900px) {
  .files__body {
    grid-template-columns: minmax(0, 1fr);
  }

  .files__side {
    position: static;
  }
}
</style>
