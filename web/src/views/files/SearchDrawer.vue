<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Finds entries below the current directory by name or by content.
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCheckbox, NDrawer, NDrawerContent, NEmpty, NIcon, NInput, NSpace, NSpin, NText } from 'naive-ui'
import { DocumentOutline, FolderOutline, SearchOutline } from '@vicons/ionicons5'
import { isAbortError } from '@/api/client'
import { searchFiles, type FileMatch } from '@/api/files'
import { absolutePath } from '@/files/paths'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ root: string; dir: string }>()
const emit = defineEmits<{ pick: [match: FileMatch] }>()

const { t } = useI18n()
const query = ref('')
const content = ref(false)
const caseSensitive = ref(false)
const matches = ref<FileMatch[]>([])
const truncated = ref(false)
const searched = ref(false)
const loading = ref(false)
const error = ref<unknown>(null)
let controller: AbortController | null = null

// A new place to search in starts afresh.
watch(
  () => [props.root, props.dir] as const,
  () => {
    controller?.abort()
    matches.value = []
    searched.value = false
    error.value = null
  },
)

async function search() {
  const q = query.value
  if (!q.trim() || q.length > 256) return
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  error.value = null
  try {
    const result = await searchFiles(props.root, props.dir, q, { content: content.value, caseSensitive: caseSensitive.value }, current.signal)
    matches.value = result.matches ?? []
    truncated.value = !!result.truncated
    searched.value = true
  } catch (err) {
    if (!isAbortError(err)) error.value = err
  } finally {
    if (controller === current) loading.value = false
  }
}
</script>

<template>
  <NDrawer v-model:show="show" placement="right" width="min(520px, 100vw)">
    <NDrawerContent :title="t('files.search.title')" closable :native-scrollbar="false">
      <NText depth="3" tag="p" class="where" dir="ltr">{{ absolutePath(root, dir) }}</NText>
      <form class="form" @submit.prevent="search">
        <NInput
          v-model:value="query"
          :placeholder="t('files.search.placeholder')"
          :maxlength="256"
          clearable
          :input-props="{ autocomplete: 'off', spellcheck: false, 'aria-label': t('files.search.placeholder') }"
        />
        <NButton type="primary" attr-type="submit" :loading="loading" :disabled="!query.trim()">
          <template #icon>
            <NIcon><SearchOutline /></NIcon>
          </template>
          {{ t('files.search.submit') }}
        </NButton>
      </form>
      <NSpace :size="16" class="options">
        <NCheckbox v-model:checked="content">{{ t('files.search.content') }}</NCheckbox>
        <NCheckbox v-model:checked="caseSensitive">{{ t('files.search.case') }}</NCheckbox>
      </NSpace>
      <NText v-if="content" depth="3" tag="p" class="hint">{{ t('files.search.contentHint') }}</NText>

      <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
      <NSpin :show="loading">
        <NAlert v-if="truncated" type="info" class="block">{{ t('files.search.truncated') }}</NAlert>
        <NEmpty v-if="searched && matches.length === 0" :description="t('files.search.none')" class="empty" />
        <ul class="results">
          <li v-for="(match, index) in matches" :key="`${match.path}:${match.line ?? 0}:${index}`">
            <button type="button" class="result" @click="emit('pick', match)">
              <NIcon size="16" class="result__icon">
                <FolderOutline v-if="match.type === 'dir'" />
                <DocumentOutline v-else />
              </NIcon>
              <span class="result__body">
                <span class="result__path" dir="ltr">
                  {{ match.path }}<template v-if="match.line">:{{ match.line }}</template>
                </span>
                <span v-if="match.text" class="result__text" dir="ltr">{{ match.text }}</span>
              </span>
            </button>
          </li>
        </ul>
      </NSpin>
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.where {
  margin-block: 0 8px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  overflow-wrap: anywhere;
}

.form {
  display: flex;
  gap: 8px;
}

.options {
  margin-block: 8px;
}

.hint {
  margin-block: 0 8px;
  font-size: 12px;
}

.block {
  margin-block: 8px;
}

.empty {
  padding-block: 24px;
}

.results {
  margin: 0;
  padding: 0;
  list-style: none;
}

.result {
  display: flex;
  gap: 8px;
  inline-size: 100%;
  padding: 6px 4px;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: start;
  cursor: pointer;
}

.result:hover,
.result:focus-visible {
  background: rgba(128, 128, 128, 0.12);
}

.result__icon {
  flex: none;
  margin-block-start: 2px;
}

.result__body {
  display: flex;
  flex-direction: column;
  min-inline-size: 0;
}

.result__path {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
  overflow-wrap: anywhere;
}

.result__text {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  opacity: 0.75;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
