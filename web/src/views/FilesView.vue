<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter, type LocationQuery, type RouteLocationRaw } from 'vue-router'
import { ChevronDown, CloudDownload, File as FileIcon, FolderPlus, Plus, Search, Upload } from '@lucide/vue'
import { isApiError } from '@/api/client'
import {
  UPLOAD_MAX,
  listDir,
  makeDir,
  renameEntry,
  setFullDisk,
  touchFile,
  type FileEntry,
  type FileMatch,
} from '@/api/files'
import AccessSwitch from '@/components/common/AccessSwitch.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import Notice from '@/components/common/Notice.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import type { PageTab } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { confirmAction } from '@/composables/useConfirmDialog'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { LINE_QUERY, OPEN_QUERY, SECTION_QUERY, TAB_QUERY } from '@/composables/usePageTab'
import { FILES_TABS, legacyFilesQuery, type FilesTab } from '@/files/route'
import { absolutePath, archiveKind, isImage, joinPath, parentPath, rootLabel } from '@/files/paths'
import { formatBytes, formatNumber } from '@/i18n/format'
import { notifyError, notifyErrorText, notifySuccess } from '@/lib/notify'
import { useFilesStore } from '@/stores/files'
import { useSessionStore } from '@/stores/session'
import { useUploadsStore } from '@/stores/uploads'
import { askDelete, download, withinBatch, type FileActionKey } from './files/actions'
import ChmodSheet from './files/ChmodSheet.vue'
import ChownSheet from './files/ChownSheet.vue'
import CompressSheet from './files/CompressSheet.vue'
import ExtractSheet from './files/ExtractSheet.vue'
import FetchSheet from './files/FetchSheet.vue'
import FileEditor from './files/FileEditor.vue'
import FileTable from './files/FileTable.vue'
import ImagePreview from './files/ImagePreview.vue'
import NameDialog from './files/NameDialog.vue'
import SearchSheet from './files/SearchSheet.vue'
import TransferSheet from './files/TransferSheet.vue'
import TrashTable from './files/TrashTable.vue'

// File manager ("list" skeleton, interface spec section 6): a tab per root
// (sites, backups, the whole disk when it is switched on) and one for the
// trash. The folder is in the address (`?tab=<root>&path=<folder>`), the
// trash shown in `?tab=trash&root=<root>`, an open file in
// `?open=edit:<path>` or `?open=view:<path>`. Most changes run as jobs the
// job dock follows; forms open in sheets.
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const store = useFilesStore()
const session = useSessionStore()
const uploads = useUploadsStore()
const editor = useDrawerRoute('edit')
const viewer = useDrawerRoute('view')

const canManage = computed(() => session.can('files.manage'))
const canAdmin = computed(() => session.can('files.admin'))

/**
 * The root tabs there are, as the server offers them (only the roots this
 * account may open). Until they are known, every tab counts for those who
 * may open them all; the others only reach the sites.
 */
const validTabs = computed<FilesTab[]>(() => {
  if (!store.roots) return canAdmin.value ? [...FILES_TABS] : ['sites', 'trash']
  const ids = store.available.map((root) => root.id).filter((id): id is FilesTab => (FILES_TABS as readonly string[]).includes(id))
  return [...ids.filter((id) => id !== 'trash'), 'trash']
})

function queryText(name: string): string {
  const value = route.query[name]
  return typeof value === 'string' ? value : ''
}

const tab = computed<FilesTab>(() => {
  const value = queryText(TAB_QUERY)
  if ((validTabs.value as string[]).includes(value)) return value as FilesTab
  return validTabs.value[0] ?? 'sites'
})
/** The root whose folder is shown; null on the trash tab. */
const root = computed(() => (tab.value === 'trash' ? null : tab.value))
/** The folder asked for; a folder of a root this account cannot open is not looked for in another one. */
const path = computed(() => {
  const asked = queryText(TAB_QUERY)
  return !asked || asked === tab.value ? queryText('path') : ''
})
const trashRoot = computed(() => {
  const asked = queryText('root')
  if (asked && (!store.roots || store.available.some((entry) => entry.id === asked))) return asked
  return store.available[0]?.id ?? 'sites'
})

/** The page's own link for a tab: a root at its top, or the trash. */
function tabLink(value: FilesTab): RouteLocationRaw {
  if (value === 'trash') return { query: { [TAB_QUERY]: 'trash', ...(trashRoot.value !== 'sites' ? { root: trashRoot.value } : {}) } }
  return { query: value === validTabs.value[0] ? {} : { [TAB_QUERY]: value } }
}

function showTrash(next: string) {
  void router.replace({ query: { [TAB_QUERY]: 'trash', root: next } })
}

function folderQuery(target: string, folder: string): LocationQuery {
  const query: LocationQuery = target === validTabs.value[0] ? {} : { [TAB_QUERY]: target }
  if (folder) query.path = folder
  return query
}

function go(target: string, folder: string) {
  void router.push({ query: folderQuery(target, folder) })
}

const tabs = computed<PageTab<FilesTab>[]>(() =>
  validTabs.value.map((value) => ({
    value,
    label: t(`files.tabs.${value}`),
    count: value === 'trash' ? store.trashCount : undefined,
  })),
)

// Old addresses (?root=&path=, ?trash=1) keep working.
watch(
  () => route.query,
  (query) => {
    const next = legacyFilesQuery(query)
    if (next) void router.replace({ query: next })
  },
  { immediate: true },
)

watch(
  [root, path],
  ([current, folder]) => {
    if (current) store.show(current, folder)
  },
  { immediate: true },
)

onMounted(async () => {
  void store.loadFavorites()
  await store.loadRoots()
  store.loadAllTrash()
})
onBeforeUnmount(() => store.leave())

// An upload into this folder landed.
watch(
  () => uploads.placedVersion,
  () => {
    const placed = uploads.lastPlaced
    if (placed && placed.root === root.value && placed.dir === path.value) void store.loadList()
  },
)

const status = computed(() => {
  if (tab.value === 'trash') {
    const entries = store.trash[trashRoot.value]
    const head = t('files.trash.title', { root: rootLabel(trashRoot.value) })
    if (!entries) return head
    const size = entries.reduce((sum, entry) => sum + entry.size, 0)
    return `${head} · ${t('files.trash.total', { count: entries.length, size: formatBytes(size) }, entries.length)}`
  }
  return ''
})

const entryCount = computed(() => store.list?.entries.length)

// Full-disk access -----------------------------------------------------------------

const switching = ref(false)

async function toggleFullDisk(on: boolean) {
  if (on) {
    const ok = await confirmAction({
      title: t('files.fullDisk.enableTitle'),
      description: t('files.fullDisk.enableText'),
      confirmLabel: t('files.fullDisk.enable'),
      destructive: true,
    })
    if (!ok) return
  }
  switching.value = true
  try {
    // Step-up protected: the client asks for the password and retries.
    await setFullDisk(on)
    await store.loadRoots()
    if (!on && tab.value === 'system') go(validTabs.value[0] ?? 'sites', '')
    notifySuccess(on ? t('files.fullDisk.enabled') : t('files.fullDisk.disabled'))
  } catch (err) {
    notifyError(err)
  } finally {
    switching.value = false
  }
}

// Opening entries ------------------------------------------------------------------

const line = computed(() => {
  const value = Number.parseInt(queryText(LINE_QUERY), 10)
  return Number.isFinite(value) && value > 0 ? value : undefined
})

function openFile(file: string, at?: number) {
  if (isImage(file)) viewer.open(file)
  else editor.open(file, undefined, at ? { [LINE_QUERY]: String(at) } : {})
}

async function openEntry(entry: FileEntry) {
  const current = root.value
  if (!current) return
  if (entry.type === 'dir') {
    go(current, entry.path)
    return
  }
  if (entry.type === 'link') {
    // A link to a folder opens it; a link to a file opens the file.
    try {
      await listDir(current, entry.path, false)
      go(current, entry.path)
    } catch (err) {
      if (isApiError(err) && err.code === 'file.not_dir') openFile(entry.path)
      else notifyError(err)
    }
    return
  }
  if (entry.type !== 'file') return
  if (archiveKind(entry.name) && canManage.value) openExtract(entry)
  else openFile(entry.path)
}

async function pickMatch(match: FileMatch) {
  const current = root.value
  if (!current) return
  if (match.type === 'dir') {
    go(current, match.path)
    return
  }
  await router.push({ query: folderQuery(current, parentPath(match.path)) })
  openFile(match.path, match.line)
}

// Sheets and dialogs -------------------------------------------------------------------

interface NameRequest {
  title: string
  description: string
  initial: string
  submitLabel: string
  action: (name: string) => Promise<void>
}

const nameOpen = ref(false)
const nameRequest = ref<NameRequest>({ title: '', description: '', initial: '', submitLabel: '', action: async () => {} })
const transfer = ref<{ open: boolean; mode: 'copy' | 'move'; paths: string[] }>({ open: false, mode: 'copy', paths: [] })
const chmod = ref<{ open: boolean; entries: FileEntry[] }>({ open: false, entries: [] })
const chown = ref<{ open: boolean; entries: FileEntry[] }>({ open: false, entries: [] })
const compress = ref<{ open: boolean; names: string[] }>({ open: false, names: [] })
const extract = ref<{ open: boolean; entry: FileEntry | null }>({ open: false, entry: null })
const showFetch = ref(false)
const showSearch = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

function askName(request: NameRequest) {
  nameRequest.value = request
  nameOpen.value = true
}

function newFolder() {
  const current = root.value
  if (!current) return
  const dir = path.value
  askName({
    title: t('files.newFolder.title'),
    description: absolutePath(current, dir),
    initial: '',
    submitLabel: t('files.newFolder.submit'),
    action: async (name) => {
      await makeDir(current, dir, name)
      void store.loadList()
    },
  })
}

function newFile() {
  const current = root.value
  if (!current) return
  const dir = path.value
  askName({
    title: t('files.newFile.title'),
    description: absolutePath(current, dir),
    initial: '',
    submitLabel: t('files.newFile.submit'),
    action: async (name) => {
      await touchFile(current, dir, name)
      void store.loadList()
      openFile(joinPath(dir, name))
    },
  })
}

function rename(entry: FileEntry) {
  const current = root.value
  if (!current) return
  askName({
    title: t('files.rename.title', { name: entry.name }),
    description: absolutePath(current, parentPath(entry.path)),
    initial: entry.name,
    submitLabel: t('files.rename.submit'),
    action: async (name) => {
      if (name === entry.name) return
      await renameEntry(current, entry.path, name)
      notifySuccess(t('files.rename.done', { name }))
      void store.loadList()
    },
  })
}

function openExtract(entry: FileEntry) {
  extract.value = { open: true, entry }
}

function onAction(key: FileActionKey, entries: FileEntry[]) {
  const current = root.value
  const first = entries[0]
  if (!current || !first) return
  const paths = entries.map((entry) => entry.path)
  switch (key) {
    case 'edit':
      openFile(first.path)
      break
    case 'download':
      download(current, first.path)
      break
    case 'extract':
      openExtract(first)
      break
    case 'rename':
      rename(first)
      break
    case 'copy':
    case 'move':
      if (withinBatch(paths.length)) transfer.value = { open: true, mode: key, paths }
      break
    case 'compress':
      if (withinBatch(paths.length)) compress.value = { open: true, names: entries.map((entry) => entry.name) }
      break
    case 'chmod':
      if (withinBatch(paths.length)) chmod.value = { open: true, entries: [...entries] }
      break
    case 'chown':
      if (withinBatch(paths.length)) chown.value = { open: true, entries: [...entries] }
      break
    case 'delete':
      void askDelete(current, paths)
      break
  }
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
  const current = root.value
  if (files.length === 0 || !canManage.value || !current) return
  const dir = path.value
  const tooLarge = files.filter((file) => file.size > UPLOAD_MAX)
  if (tooLarge.length > 0) {
    notifyErrorText(t('files.upload.tooLarge', { name: tooLarge.map((file) => file.name).join(', '), size: formatBytes(UPLOAD_MAX) }))
  }
  const accepted = files.filter((file) => file.size <= UPLOAD_MAX)
  if (accepted.length === 0) return
  const existing = new Set((store.list?.entries ?? []).map((entry) => entry.name))
  const conflicts = accepted.filter((file) => existing.has(file.name)).map((file) => file.name)
  if (conflicts.length > 0) {
    const ok = await confirmAction({
      title: t('files.upload.existsTitle', { count: conflicts.length }, conflicts.length),
      description: t('files.upload.existsText', { names: conflicts.slice(0, 5).join(', '), count: conflicts.length }, conflicts.length),
      confirmLabel: t('files.upload.replace'),
      destructive: true,
    })
    if (!ok) return
  }
  const replace = new Set(conflicts)
  uploads.add(current, dir, accepted, (name) => replace.has(name))
}

/** Whether an editor or viewer address belongs to this tab. */
const editorPath = computed(() => (root.value ? editor.id.value : null))
const viewerPath = computed(() => (root.value ? viewer.id.value : null))

// An editor left open on the trash tab (an old address) closes.
watch(
  () => [tab.value, route.query[OPEN_QUERY]] as const,
  ([current, open]) => {
    if (current === 'trash' && typeof open === 'string' && (open.startsWith('edit:') || open.startsWith('view:'))) {
      const query = { ...route.query }
      delete query[OPEN_QUERY]
      delete query[SECTION_QUERY]
      delete query[LINE_QUERY]
      void router.replace({ query })
    }
  },
  { immediate: true },
)
</script>

<template>
  <div>
    <PageHeader :title="t('files.title')" :status="status" :tabs="tabs" :tab="tab" :tab-location="tabLink">
      <template v-if="root" #status>
        <span class="inline-flex min-w-0 flex-wrap items-baseline gap-x-1.5">
          <span>{{ rootLabel(root) }}</span>
          <span aria-hidden="true">·</span>
          <span class="font-mono text-[13px] [overflow-wrap:anywhere]" dir="ltr">{{ absolutePath(root, path) }}</span>
          <template v-if="entryCount !== undefined">
            <span aria-hidden="true">·</span>
            <span>{{ t('files.status.entries', { count: formatNumber(entryCount) }, entryCount) }}</span>
          </template>
        </span>
      </template>
      <template #actions>
        <AccessSwitch
          v-if="canAdmin && store.settings"
          :label="t('files.fullDisk.label')"
          :description="t('files.fullDisk.enableText')"
          :on="store.settings.fullDisk"
          :locked="store.settings.locked"
          :locked-text="t('files.fullDisk.locked')"
          :busy="switching"
          @update="toggleFullDisk"
        />
        <template v-if="root">
          <Button variant="outline" class="max-md:h-11" @click="showSearch = true">
            <Search aria-hidden="true" />
            {{ t('files.action.search') }}
          </Button>
          <template v-if="canManage">
            <Button variant="outline" class="max-md:h-11" @click="showFetch = true">
              <CloudDownload aria-hidden="true" />
              {{ t('files.action.fetch') }}
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger as-child>
                <Button variant="outline" class="max-md:h-11">
                  <Plus aria-hidden="true" />
                  {{ t('files.action.new') }}
                  <ChevronDown class="text-muted-foreground" aria-hidden="true" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" class="min-w-44">
                <DropdownMenuItem class="max-md:min-h-11" @select="newFolder">
                  <FolderPlus aria-hidden="true" />
                  {{ t('files.action.newFolder') }}
                </DropdownMenuItem>
                <DropdownMenuItem class="max-md:min-h-11" @select="newFile">
                  <FileIcon aria-hidden="true" />
                  {{ t('files.action.newFile') }}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
            <Button class="max-md:h-11" @click="chooseFiles">
              <Upload aria-hidden="true" />
              {{ t('files.action.upload') }}
            </Button>
          </template>
        </template>
      </template>
    </PageHeader>

    <div class="flex flex-col gap-4">
      <ErrorState v-if="store.rootsError && !store.roots" :error="store.rootsError" class="rounded-lg border bg-card" @retry="store.loadRoots()" />
      <template v-else>
        <Notice v-if="tab === 'system' && store.settings?.fullDisk" tone="warning">{{ t('files.fullDisk.active') }}</Notice>

        <TrashTable
          v-if="tab === 'trash'"
          :root="trashRoot"
          :can-manage="canManage"
          @update:root="showTrash"
        />
        <FileTable
          v-else-if="root"
          :root="root"
          :path="path"
          :can-manage="canManage"
          @open="openEntry"
          @navigate="(next: string) => go(root!, next)"
          @go="go"
          @action="onAction"
          @upload="startUpload"
          @choose="chooseFiles"
        />
      </template>
    </div>

    <input ref="fileInput" type="file" multiple hidden @change="onFilesChosen" />

    <template v-if="root">
      <SearchSheet v-model:open="showSearch" :root="root" :dir="path" @pick="pickMatch" />
      <FileEditor
        :root="root"
        :path="editorPath"
        :line="line"
        :can-manage="canManage"
        @close="editor.close()"
        @saved="store.loadList()"
      />
      <ImagePreview :root="root" :path="viewerPath" @close="viewer.close()" />
      <template v-if="canManage">
        <NameDialog
          v-model:open="nameOpen"
          :title="nameRequest.title"
          :description="nameRequest.description"
          :initial="nameRequest.initial"
          :submit-label="nameRequest.submitLabel"
          :action="nameRequest.action"
        />
        <TransferSheet v-model:open="transfer.open" :mode="transfer.mode" :root="root" :dir="path" :paths="transfer.paths" />
        <ChmodSheet v-model:open="chmod.open" :root="root" :entries="chmod.entries" />
        <ChownSheet v-model:open="chown.open" :root="root" :entries="chown.entries" />
        <CompressSheet v-model:open="compress.open" :root="root" :dir="path" :names="compress.names" />
        <ExtractSheet
          v-model:open="extract.open"
          :root="root"
          :entry="extract.entry"
          :archivers="store.roots?.archivers ?? { sevenZip: false, rar: false }"
        />
        <FetchSheet v-model:open="showFetch" :root="root" :dir="path" />
      </template>
    </template>
  </div>
</template>
