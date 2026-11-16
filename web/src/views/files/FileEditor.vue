<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Opens a text file in the editor and saves it. Saving names the version
// that was read, so changes someone else made meanwhile are not lost: the
// user reloads or overwrites them knowingly.
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NDrawer, NDrawerContent, NSpace, NSpin, NTag, NText, useMessage } from 'naive-ui'
import { isApiError, isStepUpRequired } from '@/api/client'
import { FILE_READ_MAX, downloadUrl, readFile, saveFile } from '@/api/files'
import { useConfirm } from '@/composables/useConfirm'
import { absolutePath, baseName } from '@/files/paths'
import { errorMessage } from '@/i18n/errors'
import { formatBytes } from '@/i18n/format'

// CodeMirror is only loaded when a file is opened.
const CodeEditor = defineAsyncComponent(() => import('./CodeEditor.vue'))

const props = defineProps<{
  root: string
  /** File to edit; null closes the editor. */
  path: string | null
  /** Line to jump to (search results). */
  line?: number
  canManage: boolean
}>()
const emit = defineEmits<{ close: []; saved: [] }>()

const { t } = useI18n()
const message = useMessage()
const confirm = useConfirm()

const editor = ref<{ getValue: () => string; focus: () => void } | null>(null)
const content = ref('')
const etag = ref('')
const loading = ref(false)
const loadError = ref<unknown>(null)
const saveError = ref<unknown>(null)
const dirty = ref(false)
const saving = ref(false)
/** Someone changed the file since it was read. */
const conflict = ref(false)
let loadToken = 0

const open = computed(() => props.path !== null)
const name = computed(() => (props.path ? baseName(props.path) : ''))
/** The server cannot show this file as text; offer the download instead. */
const notText = computed(() => isApiError(loadError.value) && ['file.binary', 'file.too_large'].includes(loadError.value.code))

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
    etag.value = ''
    dirty.value = false
    if (path !== null) void load()
  },
  { immediate: true },
)

function byteLength(text: string): number {
  return new TextEncoder().encode(text).length
}

async function save(overwrite = false) {
  if (!props.canManage || saving.value || props.path === null || !editor.value) return
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
    // The version the next save names.
    etag.value = saved.etag
    // Typing while the save ran leaves the file modified.
    dirty.value = editor.value?.getValue() !== text
    conflict.value = false
    message.success(t('files.editor.saved', { name: baseName(path) }))
    emit('saved')
  } catch (err) {
    if (isApiError(err) && err.code === 'file.changed') conflict.value = true
    else if (!isStepUpRequired(err)) saveError.value = err
  } finally {
    saving.value = false
  }
}

async function reload() {
  if (dirty.value) {
    const ok = await confirm({
      title: t('files.editor.reloadTitle'),
      content: t('files.editor.discardText'),
      confirmText: t('files.editor.reload'),
      danger: true,
    })
    if (!ok) return
  }
  await load()
}

async function close() {
  if (dirty.value) {
    const ok = await confirm({
      title: t('files.editor.discardTitle', { name: name.value }),
      content: t('files.editor.discardText'),
      confirmText: t('files.editor.discard'),
      danger: true,
    })
    if (!ok) return
  }
  emit('close')
}

const saveErrorText = computed(() => (typeof saveError.value === 'string' ? saveError.value : errorMessage(saveError.value)))
</script>

<template>
  <NDrawer
    :show="open"
    placement="right"
    width="min(1200px, 100vw)"
    :mask-closable="false"
    :close-on-esc="false"
    @update:show="(value: boolean) => !value && close()"
  >
    <NDrawerContent :native-scrollbar="false">
      <template #header>
        <div class="file-editor__bar">
          <div class="file-editor__head">
            <div class="file-editor__title">
              <span class="file-editor__name">{{ name }}</span>
              <NTag v-if="dirty" size="small" type="warning" :bordered="false">{{ t('files.editor.modified') }}</NTag>
              <NTag v-if="!canManage" size="small" :bordered="false">{{ t('files.editor.readOnly') }}</NTag>
            </div>
            <NText depth="3" class="file-editor__path" dir="ltr">{{ path ? absolutePath(root, path) : '' }}</NText>
          </div>
          <NSpace :size="6" :wrap="false">
            <NButton v-if="path" tag="a" :href="downloadUrl(root, path)" size="small" quaternary>
              {{ t('files.action.download') }}
            </NButton>
            <NButton
              v-if="canManage"
              type="primary"
              size="small"
              :loading="saving"
              :disabled="!dirty || loading || !!loadError"
              @click="save()"
            >
              {{ t('common.action.save') }}
            </NButton>
            <NButton size="small" @click="close">{{ t('sites.create.close') }}</NButton>
          </NSpace>
        </div>
      </template>

      <NAlert v-if="conflict" type="warning" class="block">
        {{ t('errors.file.changed') }}
        <div class="block__actions">
          <NButton size="small" @click="reload">{{ t('files.editor.reload') }}</NButton>
          <NButton size="small" type="warning" :loading="saving" @click="save(true)">{{ t('files.editor.overwrite') }}</NButton>
        </div>
      </NAlert>
      <NAlert v-if="saveError" type="error" class="block">{{ saveErrorText }}</NAlert>

      <NSpin v-if="loading" class="file-editor__wait" />
      <NAlert v-else-if="loadError" :type="notText ? 'info' : 'error'" class="block">
        {{ errorMessage(loadError) }}
        <div v-if="notText && path" class="block__actions">
          <NButton tag="a" :href="downloadUrl(root, path)" size="small">{{ t('files.action.download') }}</NButton>
        </div>
      </NAlert>
      <div v-else-if="path" class="file-editor__editor">
        <CodeEditor
          ref="editor"
          :value="content"
          :path="path"
          :line="line"
          :read-only="!canManage"
          @changed="dirty = true"
          @save="save()"
        />
      </div>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.file-editor__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  inline-size: 100%;
}

.file-editor__head {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-inline-size: 0;
}

.file-editor__title {
  display: flex;
  align-items: center;
  gap: 8px;
  min-inline-size: 0;
}

.file-editor__name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.file-editor__path {
  font-size: 12px;
  font-weight: normal;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  overflow-wrap: anywhere;
}

.file-editor__wait {
  display: flex;
  justify-content: center;
  padding-block: 48px;
}

.file-editor__editor {
  block-size: calc(100vh - 120px);
  min-block-size: 240px;
}

.block {
  margin-block-end: 12px;
}

.block__actions {
  display: flex;
  gap: 8px;
  margin-block-start: 8px;
}
</style>
