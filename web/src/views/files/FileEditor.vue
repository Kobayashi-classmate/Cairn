<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import { Download, FileText, LoaderCircle, Lock, Save, X } from '@lucide/vue'
import { isApiError, isStepUpRequired } from '@/api/client'
import { FILE_READ_MAX, downloadUrl, readFile, saveFile, type FileEntry } from '@/api/files'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import Notice from '@/components/common/Notice.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { keepOpenOnCompanion, trackOverlay } from '@/components/common/overlayGuard'
import { Button } from '@/components/ui/button'
import { Kbd } from '@/components/ui/kbd'
import { Sheet, SheetContent, SheetDescription, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { stepUpOpen } from '@/auth/stepUp'
import { OPEN_QUERY } from '@/composables/usePageTab'
import { confirmAction } from '@/composables/useConfirmDialog'
import { absolutePath, baseName } from '@/files/paths'
import { errorMessage } from '@/i18n/errors'
import { formatBytes } from '@/i18n/format'
import { notifySuccess } from '@/lib/notify'
import { onDownloadClick } from './actions'

// A text file in the editor, in a wide sheet whose state is in the address
// (`?open=edit:<path>`, `&line=` from a search). Saving names the version
// that was read, so changes someone else made meanwhile are not lost: the
// user reloads or overwrites them knowingly. Unsaved changes are kept from
// closing, Back and leaving the page by a confirmation.
const CodeEditor = defineAsyncComponent(() => import('./CodeEditor.vue'))

const props = defineProps<{
  root: string
  /** File to edit; null closes the editor. */
  path: string | null
  /** Line to show (search results). */
  line?: number
  canManage: boolean
}>()
const emit = defineEmits<{ close: []; saved: [] }>()

const { t } = useI18n()
const open = computed(() => props.path !== null)
trackOverlay(() => open.value)

const editor = ref<{ getValue: () => string; focus: () => void } | null>(null)
const content = ref('')
const entry = ref<FileEntry | null>(null)
const etag = ref('')
const loading = ref(false)
const loadError = ref<unknown>(null)
const saveError = ref<unknown>(null)
const dirty = ref(false)
const saving = ref(false)
/** Someone changed the file since it was read. */
const conflict = ref(false)
/** The user agreed to drop the changes; the next navigation may close. */
let leaving = false
let loadToken = 0

const name = computed(() => (props.path ? baseName(props.path) : ''))
const fullPath = computed(() => (props.path ? absolutePath(props.root, props.path) : ''))
/** The server cannot show this file as text; the download is offered instead. */
const notText = computed(() => isApiError(loadError.value) && ['file.binary', 'file.too_large'].includes(loadError.value.code))
const readOnly = computed(() => !props.canManage)
const isMac = /Mac|iPhone|iPad/i.test(navigator.platform || navigator.userAgent)

async function load() {
  const path = props.path
  if (path === null) return
  const token = ++loadToken
  loading.value = true
  loadError.value = null
  saveError.value = null
  conflict.value = false
  try {
    const file = await readFile(props.root, path)
    if (token !== loadToken) return
    content.value = file.content
    entry.value = file.entry
    etag.value = file.etag
    dirty.value = false
  } catch (err) {
    if (token === loadToken) loadError.value = err
  } finally {
    if (token === loadToken) loading.value = false
  }
}

watch(
  () => [props.root, props.path] as const,
  ([, path]) => {
    content.value = ''
    entry.value = null
    etag.value = ''
    dirty.value = false
    leaving = false
    if (path !== null) void load()
  },
  { immediate: true },
)

function byteLength(text: string): number {
  return new TextEncoder().encode(text).length
}

async function save(overwrite = false) {
  if (readOnly.value || saving.value || props.path === null || !editor.value || loading.value || loadError.value) return
  const path = props.path
  const text = editor.value.getValue()
  saveError.value = null
  if (byteLength(text) > FILE_READ_MAX) {
    saveError.value = t('files.editor.tooLarge', { size: formatBytes(FILE_READ_MAX) })
    return
  }
  saving.value = true
  try {
    let version = etag.value
    // Overwriting: save over whatever version is there now.
    if (overwrite) version = (await readFile(props.root, path)).etag
    const saved = await saveFile(props.root, path, text, version)
    if (props.path !== path) return
    etag.value = saved.etag
    entry.value = saved.entry
    // Typing while the save ran leaves the file modified.
    dirty.value = editor.value?.getValue() !== text
    conflict.value = false
    notifySuccess(t('files.editor.saved', { name: baseName(path) }))
    emit('saved')
  } catch (err) {
    if (isApiError(err) && err.code === 'file.changed') conflict.value = true
    else if (!isStepUpRequired(err)) saveError.value = err
  } finally {
    saving.value = false
  }
}

function confirmDiscard(): Promise<boolean> {
  return confirmAction({
    title: t('files.editor.discardTitle', { name: name.value }),
    description: t('files.editor.discardText'),
    confirmLabel: t('files.editor.discard'),
    destructive: true,
  })
}

async function reload() {
  if (dirty.value && !(await confirmDiscard())) return
  await load()
}

/** Close, Escape, a click on the overlay: ask first when there are changes. */
async function requestClose() {
  if (dirty.value && !(await confirmDiscard())) return
  leaving = true
  emit('close')
}

/** Back, a link or another tab while the file has changes. */
async function guard(to: { query: Record<string, unknown> }): Promise<boolean> {
  if (!open.value || !dirty.value || leaving) return true
  if (to.query[OPEN_QUERY] === `edit:${props.path}`) return true
  const ok = await confirmDiscard()
  if (ok) leaving = true
  return ok
}

onBeforeRouteUpdate((to) => guard(to))
onBeforeRouteLeave((to) => guard(to))

function onBeforeUnload(event: BeforeUnloadEvent) {
  if (!open.value || !dirty.value) return
  event.preventDefault()
}

onMounted(() => window.addEventListener('beforeunload', onBeforeUnload))
onBeforeUnmount(() => window.removeEventListener('beforeunload', onBeforeUnload))

function onOpenChange(value: boolean) {
  if (!value) void requestClose()
}

function onEscape(event: KeyboardEvent) {
  // The code editor used it (closing its search panel, dropping a
  // selection), or the re-authentication dialog is open: stay. Otherwise
  // the editor itself decides whether it may close.
  const handled = event.defaultPrevented
  event.preventDefault()
  if (!handled && !stepUpOpen.value) void requestClose()
}

function onInteractOutside(event: Parameters<typeof keepOpenOnCompanion>[0]) {
  keepOpenOnCompanion(event)
  // The editor holds unsaved work; it never closes by a click beside it.
  event.preventDefault()
}

function onKeydown(event: KeyboardEvent) {
  // The code editor handles its own Ctrl/Cmd S.
  if (event.defaultPrevented) return
  if ((event.metaKey || event.ctrlKey) && !event.altKey && event.key.toLowerCase() === 's') {
    event.preventDefault()
    void save()
  }
}

/**
 * Start in the text rather than on the first header button. The editor
 * mounts once the file is read and focuses itself then.
 */
function onOpenAutoFocus(event: Event) {
  event.preventDefault()
  editor.value?.focus()
}

const saveErrorText = computed(() => (typeof saveError.value === 'string' ? saveError.value : errorMessage(saveError.value)))
const size = computed(() => (entry.value ? formatBytes(entry.value.size) : ''))
</script>

<template>
  <Sheet :open="open" @update:open="onOpenChange">
    <SheetContent
      side="right"
      :show-close-button="false"
      class="h-dvh w-full max-w-none gap-0 bg-background p-0 sm:max-w-[1040px] md:w-[min(1040px,100%)]"
      @interact-outside="onInteractOutside"
      @escape-key-down="onEscape"
      @keydown="onKeydown"
      @open-auto-focus="onOpenAutoFocus"
    >
      <header class="flex flex-wrap items-center gap-x-4 gap-y-3 border-b bg-card px-4 py-3 md:px-5">
        <span
          class="inline-flex size-[38px] shrink-0 items-center justify-center rounded-lg bg-primary-soft text-primary-soft-foreground"
          aria-hidden="true"
        >
          <FileText class="size-[18px]" />
        </span>
        <div class="flex min-w-0 flex-[1_1_240px] flex-col gap-0.5">
          <div class="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1">
            <SheetTitle class="m-0 min-w-0 truncate text-lg font-semibold tracking-[-0.01em]" dir="auto">{{ name }}</SheetTitle>
            <StatusPill v-if="dirty" tone="warning" dot>{{ t('files.editor.modified') }}</StatusPill>
            <StatusPill v-if="readOnly" tone="neutral">{{ t('files.editor.readOnly') }}</StatusPill>
          </div>
          <SheetDescription class="m-0 flex min-w-0 flex-wrap gap-x-2 text-xs text-muted-foreground">
            <span class="min-w-0 truncate font-mono" dir="ltr" :title="fullPath">{{ fullPath }}</span>
            <span v-if="size" class="whitespace-nowrap">· {{ size }}</span>
          </SheetDescription>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <Button v-if="path" as="a" variant="outline" :href="downloadUrl(root, path)" :download="name" class="max-md:h-11" @click="onDownloadClick($event, root, path)" @auxclick="onDownloadClick($event, root, path)">
            <Download aria-hidden="true" />
            {{ t('files.action.download') }}
          </Button>
          <Button
            v-if="!readOnly"
            class="max-md:h-11"
            :disabled="!dirty || saving || loading || !!loadError"
            :title="isMac ? t('files.editor.saveHintMac') : t('files.editor.saveHint')"
            @click="save()"
          >
            <LoaderCircle v-if="saving" class="animate-spin" aria-hidden="true" />
            <Save v-else aria-hidden="true" />
            {{ t('common.action.save') }}
            <Kbd class="ms-1 border-primary-foreground/30 bg-transparent text-primary-foreground/80 max-md:hidden">
              {{ isMac ? t('files.editor.saveKeyMac') : t('files.editor.saveKey') }}
            </Kbd>
          </Button>
          <Button variant="ghost" size="icon-lg" class="max-md:size-11" :aria-label="t('common.action.close')" @click="requestClose">
            <X class="size-[18px]" aria-hidden="true" />
          </Button>
        </div>
      </header>

      <div v-if="conflict || saveError" class="flex flex-col gap-2 border-b bg-card px-4 py-3 md:px-5">
        <Notice v-if="conflict" tone="warning">
          {{ t('errors.file.changed') }}
          <template #action>
            <button type="button" class="underline underline-offset-2 hover:no-underline" @click="reload">
              {{ t('files.editor.reload') }}
            </button>
            <button type="button" class="underline underline-offset-2 hover:no-underline" :disabled="saving" @click="save(true)">
              {{ t('files.editor.overwrite') }}
            </button>
          </template>
        </Notice>
        <Notice v-if="saveError" tone="destructive">{{ saveErrorText }}</Notice>
      </div>

      <div class="flex min-h-0 flex-1 flex-col p-3 md:p-4">
        <div v-if="loading" class="flex flex-col gap-2 rounded-md border bg-card p-4" aria-hidden="true">
          <Skeleton v-for="row in 10" :key="row" class="h-3.5" :style="{ width: `${85 - ((row * 23) % 50)}%` }" />
        </div>
        <div v-else-if="loadError && notText" class="flex flex-col items-center gap-3 rounded-md border bg-card px-4 py-12 text-center">
          <FileText class="size-6 text-muted-foreground" aria-hidden="true" />
          <p class="m-0 font-medium">{{ errorMessage(loadError) }}</p>
          <Button v-if="path" as="a" variant="outline" :href="downloadUrl(root, path)" :download="name" @click="onDownloadClick($event, root, path)" @auxclick="onDownloadClick($event, root, path)">
            <Download aria-hidden="true" />
            {{ t('files.action.download') }}
          </Button>
        </div>
        <EmptyState
          v-else-if="isStepUpRequired(loadError)"
          :icon="Lock"
          class="rounded-md border bg-card"
          :title="t('files.stepUp.file')"
          :text="t('files.stepUp.text')"
        >
          <template #actions>
            <Button variant="outline" size="sm" class="max-md:h-11" @click="load">{{ t('common.action.retry') }}</Button>
          </template>
        </EmptyState>
        <ErrorState v-else-if="loadError" :error="loadError" class="rounded-md border bg-card" @retry="load" />
        <CodeEditor
          v-else-if="path"
          ref="editor"
          class="min-h-0 flex-1"
          :value="content"
          :path="path"
          :line="line"
          :read-only="readOnly"
          @changed="dirty = true"
          @save="save()"
        />
      </div>
    </SheetContent>
  </Sheet>
</template>
