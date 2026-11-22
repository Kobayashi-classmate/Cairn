<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowLeft, ChevronRight, FileCog, History, ListChecks, LoaderCircle, Lock, ShieldCheck, X } from '@lucide/vue'
import { isAbortError, isApiError, isStepUpRequired, type ErrorParams } from '@/api/client'
import type { Job } from '@/api/jobs'
import {
  fetchConfig,
  fetchConfigVersion,
  saveConfig,
  type ConfigFile,
  type ConfigVersionContent,
  type ConfigView,
  type Offer,
  type SoftwareEntry,
} from '@/api/software'
import Card from '@/components/common/Card.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import Notice from '@/components/common/Notice.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { confirmAction } from '@/composables/useConfirmDialog'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatBytes } from '@/i18n/format'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useSoftwareStore } from '@/stores/software'

// The line's configuration files: pick one, edit it in the code editor and
// save it as a job that keeps the current file as a version, checks the new
// one and reloads or restarts the service, putting the old file back when
// that fails. Earlier versions can be viewed or loaded into the editor.
// Reading and saving ask for the password (step-up): the files can hold
// secrets, and writing them is root-level power.
const CodeEditor = defineAsyncComponent(() => import('@/views/files/CodeEditor.vue'))

const props = defineProps<{ entry: SoftwareEntry; offer: Offer }>()

const { t } = useI18n()
const store = useSoftwareStore()

const configs = computed(() => props.offer.configs ?? [])
const current = ref<ConfigFile | null>(null)
const view = ref<ConfigView | null>(null)
const loadError = ref<unknown>(null)
const loading = ref(false)

/** The text the editor starts from; `editorKey` gives it a fresh document. */
const editorValue = ref('')
const editorKey = ref(0)
const editor = ref<{ getValue: () => string } | null>(null)
const dirty = ref(false)

const saving = ref(false)
/** A save job is running. */
const applying = ref(false)
const saveError = ref<unknown>(null)
/** The file changed on the server since it was read. */
const conflict = ref(false)
/** Why the last save job failed, with the check's or service's output. */
const failure = ref<{ code: string; params?: ErrorParams } | null>(null)

const showVersions = ref(false)
const version = ref<ConfigVersionContent | null>(null)
const versionLoading = ref<string | null>(null)

let controller: AbortController | null = null
let unmounted = false
onBeforeUnmount(() => {
  unmounted = true
  controller?.abort()
})

const busy = computed(() => store.runningFor(props.entry.id) !== null)

/** Files whose name alone does not say what they are for. */
const CONFIG_LABELS: Record<string, string> = { 'php-cli.ini': 'software.configs.label.phpCli' }

function configLabel(file: ConfigFile): string {
  const key = CONFIG_LABELS[file.id]
  return key ? t(key) : file.id
}

function applyText(file: ConfigFile): string {
  const service = file.service ?? ''
  if (file.apply === 'reload') return t('software.configs.apply.reload', { service })
  if (file.apply === 'restart') return t('software.configs.apply.restart', { service })
  return t('software.configs.apply.none')
}

async function load(file: ConfigFile) {
  controller?.abort()
  const request = new AbortController()
  controller = request
  loading.value = true
  loadError.value = null
  try {
    const loaded = await fetchConfig(props.entry.id, props.offer.line, file.id, request.signal)
    if (current.value?.id !== file.id) return
    view.value = loaded
    editorValue.value = loaded.content
    editorKey.value++
    dirty.value = false
    conflict.value = false
  } catch (err) {
    if (isAbortError(err)) return
    loadError.value = err
  } finally {
    if (controller === request) {
      controller = null
      loading.value = false
    }
  }
}

function openFile(file: ConfigFile) {
  versionSeq++
  versionLoading.value = null
  current.value = file
  view.value = null
  version.value = null
  showVersions.value = false
  saveError.value = null
  failure.value = null
  void load(file)
}

function reloadFile() {
  if (current.value) void load(current.value)
}

function closeFile() {
  versionSeq++
  versionLoading.value = null
  controller?.abort()
  current.value = null
  view.value = null
  version.value = null
  loadError.value = null
}

function discard() {
  if (!view.value) return
  editorValue.value = view.value.content
  editorKey.value++
  dirty.value = false
}

const stepUpDeclined = computed(() => isStepUpRequired(loadError.value))
const locked = computed(() => view.value !== null && !view.value.editable)
const isDatabase = computed(() => props.entry.category === 'database' || props.entry.category === 'cache')

/** What saving does to the system ("Will do"). */
const steps = computed(() => {
  const file = current.value
  if (!file) return []
  const list = [t('software.configs.steps.keep'), t('software.configs.steps.write', { path: file.path })]
  if (file.check) list.push(t('software.configs.steps.check'))
  if (file.apply === 'reload') list.push(t('software.configs.steps.reload', { service: file.service ?? '' }))
  if (file.apply === 'restart') {
    list.push(
      isDatabase.value
        ? t('software.configs.steps.restartDatabase', { service: file.service ?? '' })
        : t('software.configs.steps.restart', { service: file.service ?? '' }),
    )
  }
  if (file.check || file.apply !== 'none') list.push(t('software.configs.steps.rollback'))
  return list
})

function onSaved(job: Job, file: ConfigFile, content: string) {
  applying.value = false
  if (job.state === 'succeeded') notifySuccess(t('software.configs.saved', { path: file.path }))
  // Gone (another file, the drawer closed): nothing on this page to update.
  if (unmounted || current.value?.id !== file.id) return
  if (job.state === 'succeeded') {
    // Edits made meanwhile (the editor is read-only, but a version may have
    // been loaded) are not overwritten.
    if (!dirty.value) void load(file)
    return
  }
  if (job.error) failure.value = job.error
  // The old file stays: what the editor holds is unsaved again.
  if (view.value && content !== view.value.content) dirty.value = true
}

async function save() {
  const file = current.value
  const loaded = view.value
  if (!file || !loaded || !loaded.editable || saving.value || applying.value) return
  const content = editor.value?.getValue() ?? editorValue.value
  saving.value = true
  saveError.value = null
  failure.value = null
  conflict.value = false
  try {
    const job = await store.runJob(() => saveConfig(props.entry.id, props.offer.line, file.id, content, loaded.sha256), {
      rethrow: true,
      onFinished: (done) => onSaved(done, file, content),
    })
    // The editor keeps the new text, read-only, while the job applies it.
    if (job) {
      applying.value = true
      editorValue.value = content
      dirty.value = false
    }
  } catch (err) {
    if (isApiError(err) && err.code === 'pkg.config_changed') conflict.value = true
    else if (isApiError(err) && err.code === 'pkg.config_locked') {
      view.value = { ...loaded, editable: false }
      saveError.value = err
    } else if (!isStepUpRequired(err)) saveError.value = err
  } finally {
    saving.value = false
  }
}

const failureText = computed(() => (failure.value ? (errorText(failure.value.code, failure.value.params ?? {}) ?? failure.value.code) : ''))
const failureOutput = computed(() => {
  const output = failure.value?.params?.output
  return typeof output === 'string' ? output.trim() : ''
})

/** Numbers the version reads, so a late answer never lands with another file. */
let versionSeq = 0

async function viewVersion(id: string) {
  const file = current.value
  if (!file) return
  const mine = ++versionSeq
  versionLoading.value = id
  try {
    const shown = await fetchConfigVersion(props.entry.id, props.offer.line, file.id, id)
    if (mine === versionSeq && current.value?.id === file.id) version.value = shown
  } catch (err) {
    if (mine === versionSeq && current.value?.id === file.id) notifyError(err)
  } finally {
    if (mine === versionSeq) versionLoading.value = null
  }
}

/** Reads the file again; unsaved edits go only after asking. */
async function reloadDiscarding() {
  if (
    dirty.value &&
    !(await confirmAction({
      title: t('software.configs.reloadTitle'),
      description: t('software.configs.reloadText'),
      confirmLabel: t('software.configs.reload'),
      destructive: true,
    }))
  ) {
    return
  }
  reloadFile()
}

/** Puts the version shown into the editor; saving it restores it. */
function loadShownVersion() {
  const shown = version.value
  if (!shown) return
  editorValue.value = shown.content
  editorKey.value++
  dirty.value = view.value ? shown.content !== view.value.content : true
  version.value = null
}
</script>

<template>
  <!-- The list of files -->
  <template v-if="!current">
    <p class="m-0 text-muted-foreground">{{ t('software.configs.intro') }}</p>
    <Card :level="3" flush>
      <EmptyState v-if="configs.length === 0" :icon="FileCog" compact :title="t('software.configs.empty')" />
      <ul v-else class="m-0 list-none divide-y p-0">
        <li v-for="file in configs" :key="file.id">
          <button
            type="button"
            class="flex w-full items-center gap-3 px-4 py-3 text-start outline-none transition-colors hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset md:px-[18px]"
            @click="openFile(file)"
          >
            <FileCog class="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
            <span class="flex min-w-0 flex-1 flex-col gap-0.5">
              <span class="font-medium">{{ configLabel(file) }}</span>
              <span class="font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]" dir="ltr">{{ file.path }}</span>
              <span class="flex flex-wrap gap-x-3 text-xs text-muted-foreground">
                <span>{{ applyText(file) }}</span>
                <span v-if="file.check" class="inline-flex items-center gap-1">
                  <ShieldCheck class="size-3.5" aria-hidden="true" />
                  {{ t('software.configs.checked') }}
                </span>
              </span>
            </span>
            <ChevronRight class="size-4 shrink-0 text-muted-foreground rtl:rotate-180" aria-hidden="true" />
          </button>
        </li>
      </ul>
    </Card>
  </template>

  <!-- One file in the editor -->
  <template v-else>
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
      <Button variant="ghost" size="sm" class="-ms-2 max-md:h-11" @click="closeFile">
        <ArrowLeft class="rtl:rotate-180" aria-hidden="true" />
        {{ t('software.configs.back') }}
      </Button>
      <span class="min-w-0 flex-1 font-mono text-[12.5px] [overflow-wrap:anywhere]" dir="ltr">{{ current.path }}</span>
      <span v-if="view" class="flex items-center gap-2 text-xs text-muted-foreground tabular-nums">
        {{ formatBytes(view.size) }} ·
        <RelativeTime :value="view.modTime" />
      </span>
    </div>

    <template v-if="loadError">
      <EmptyState
        v-if="stepUpDeclined"
        :icon="Lock"
        class="rounded-lg border bg-card"
        :title="t('software.configs.stepUp')"
        :text="t('software.configs.stepUpText')"
      >
        <template #actions>
          <Button variant="outline" size="sm" @click="reloadFile">{{ t('common.action.retry') }}</Button>
        </template>
      </EmptyState>
      <ErrorState v-else :error="loadError" class="rounded-lg border bg-card" @retry="reloadFile" />
    </template>
    <template v-else-if="!view">
      <Skeleton class="h-[min(60vh,520px)] w-full rounded-md" />
    </template>
    <template v-else>
      <Notice v-if="locked" tone="info">{{ t('errors.pkg.config_locked') }}</Notice>
      <Notice v-if="conflict" tone="warning">
        {{ t('errors.pkg.config_changed') }}
        <template #action>
          <button type="button" class="underline underline-offset-2 hover:no-underline" @click="reloadDiscarding">
            {{ t('software.configs.reload') }}
          </button>
        </template>
      </Notice>
      <div v-if="failure" role="alert" class="flex flex-col gap-2 rounded-lg bg-destructive-soft px-4 py-3 text-destructive">
        <div class="flex items-start gap-2 font-medium">
          <span class="flex-1">{{ failureText }}</span>
          <button
            type="button"
            class="inline-flex size-7 shrink-0 items-center justify-center rounded-sm opacity-80 outline-none hover:opacity-100 focus-visible:ring-2 focus-visible:ring-ring/50"
            :aria-label="t('common.action.dismiss')"
            @click="failure = null"
          >
            <X class="size-4" aria-hidden="true" />
          </button>
        </div>
        <pre
          v-if="failureOutput"
          tabindex="0"
          class="m-0 max-h-60 overflow-auto rounded-md bg-card p-3 font-mono text-xs whitespace-pre-wrap text-foreground [overflow-wrap:anywhere]"
          dir="ltr"
        >{{ failureOutput }}</pre>
      </div>
      <Notice v-if="saveError" tone="destructive">{{ errorMessage(saveError) }}</Notice>
      <Notice v-if="applying" tone="info">
        <span class="inline-flex items-center gap-2">
          <LoaderCircle class="size-4 animate-spin" aria-hidden="true" />
          {{ t('software.configs.applying') }}
        </span>
      </Notice>

      <div class="h-[min(60vh,520px)] min-h-64">
        <CodeEditor
          ref="editor"
          :key="editorKey"
          :value="editorValue"
          :path="current.path"
          :read-only="locked || applying"
          :autofocus="false"
          @changed="dirty = true"
          @save="save"
        />
      </div>

      <section v-if="!locked && dirty" class="flex flex-col gap-2" :aria-label="t('common.sheet.willDo')">
        <h3 class="m-0 flex items-center gap-1.5 text-detail font-semibold">
          <ListChecks class="size-4 text-muted-foreground" aria-hidden="true" />
          {{ t('common.sheet.willDo') }}
        </h3>
        <ol class="m-0 flex list-decimal flex-col gap-1.5 rounded-md bg-muted py-3 ps-8 pe-4 text-detail text-muted-foreground">
          <li v-for="(step, index) in steps" :key="index" class="[overflow-wrap:anywhere]">{{ step }}</li>
        </ol>
      </section>

      <div class="flex flex-wrap items-center gap-2">
        <Button v-if="!locked" class="max-md:h-11" :disabled="!dirty || saving || applying || busy" @click="save">
          <LoaderCircle v-if="saving" class="animate-spin" aria-hidden="true" />
          {{ t('software.configs.save') }}
        </Button>
        <Button v-if="dirty && !locked" variant="ghost" class="max-md:h-11" :disabled="saving" @click="discard">
          {{ t('software.configs.discard') }}
        </Button>
        <Button
          variant="outline"
          class="ms-auto max-md:h-11"
          :aria-expanded="showVersions"
          :disabled="view.versions.length === 0"
          @click="showVersions = !showVersions"
        >
          <History aria-hidden="true" />
          {{ t('software.configs.versions', { count: view.versions.length }) }}
        </Button>
      </div>

      <Card v-if="version" :title="t('software.configs.versionTitle')" :level="3" flush>
        <template #actions>
          <Button v-if="!locked" variant="outline" size="sm" :disabled="applying" @click="loadShownVersion">{{ t('software.configs.loadVersion') }}</Button>
          <Button variant="ghost" size="icon" :aria-label="t('common.action.close')" @click="version = null">
            <X aria-hidden="true" />
          </Button>
        </template>
        <p class="m-0 border-b px-4 py-2 text-xs text-muted-foreground md:px-[18px]">
          <RelativeTime :value="version.savedAt" /> · {{ formatBytes(version.size) }}
        </p>
        <pre tabindex="0" class="m-0 max-h-[50vh] overflow-auto px-4 py-3 font-mono text-xs whitespace-pre md:px-[18px]" dir="ltr">{{ version.content }}</pre>
      </Card>

      <Card v-if="showVersions && view.versions.length > 0" :title="t('software.configs.versionsTitle')" :level="3" flush>
        <ul class="m-0 list-none divide-y p-0">
          <li v-for="item in view.versions" :key="item.id" class="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-2.5 md:px-[18px]">
            <span class="flex min-w-0 flex-1 flex-col">
              <RelativeTime :value="item.savedAt" class="font-medium" />
              <span class="text-xs text-muted-foreground tabular-nums">{{ formatBytes(item.size) }}</span>
            </span>
            <Button variant="outline" size="sm" :disabled="versionLoading !== null" @click="viewVersion(item.id)">
              <LoaderCircle v-if="versionLoading === item.id" class="animate-spin" aria-hidden="true" />
              {{ t('software.configs.viewVersion') }}
            </Button>
          </li>
        </ul>
      </Card>
    </template>
  </template>
</template>
