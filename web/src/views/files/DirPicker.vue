<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Picks a directory inside a root by browsing; the chosen directory is the
// one being shown.
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NBreadcrumb, NBreadcrumbItem, NButton, NEmpty, NIcon, NScrollbar, NSpin } from 'naive-ui'
import { ArrowUpOutline, FolderOutline } from '@vicons/ionicons5'
import { isAbortError } from '@/api/client'
import { listDir, type FileEntry } from '@/api/files'
import { absolutePath, parentPath, pathSegments, rootLabel } from '@/files/paths'
import { errorMessage } from '@/i18n/errors'

const path = defineModel<string>('path', { required: true })
const props = defineProps<{ root: string }>()

const { t } = useI18n()
const dirs = ref<FileEntry[]>([])
const loading = ref(false)
const error = ref<unknown>(null)
let controller: AbortController | null = null

const segments = computed(() => pathSegments(path.value))

async function load() {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  error.value = null
  try {
    const list = await listDir(props.root, path.value, false, current.signal)
    dirs.value = list.entries.filter((entry) => entry.type === 'dir')
  } catch (err) {
    if (isAbortError(err)) return
    dirs.value = []
    error.value = err
  } finally {
    if (controller === current) loading.value = false
  }
}

watch([() => props.root, path], () => void load(), { immediate: true })
</script>

<template>
  <div class="dir-picker">
    <div class="dir-picker__head">
      <NButton size="small" quaternary :disabled="!path" :aria-label="t('files.action.up')" @click="path = parentPath(path)">
        <template #icon>
          <NIcon><ArrowUpOutline /></NIcon>
        </template>
      </NButton>
      <NBreadcrumb class="dir-picker__crumbs">
        <NBreadcrumbItem @click="path = ''">{{ rootLabel(root) }}</NBreadcrumbItem>
        <NBreadcrumbItem v-for="segment in segments" :key="segment.path" @click="path = segment.path">
          {{ segment.name }}
        </NBreadcrumbItem>
      </NBreadcrumb>
    </div>
    <div class="dir-picker__path mono" dir="ltr">{{ absolutePath(root, path) }}</div>
    <NSpin :show="loading">
      <NScrollbar class="dir-picker__list">
        <NAlert v-if="error" type="error">{{ errorMessage(error) }}</NAlert>
        <NEmpty v-else-if="!loading && dirs.length === 0" size="small" :description="t('files.picker.empty')" class="dir-picker__empty" />
        <button v-for="dir in dirs" :key="dir.path" type="button" class="dir-picker__item" @click="path = dir.path">
          <NIcon size="16"><FolderOutline /></NIcon>
          <span class="dir-picker__name">{{ dir.name }}</span>
        </button>
      </NScrollbar>
    </NSpin>
  </div>
</template>

<style scoped>
.dir-picker {
  display: flex;
  flex-direction: column;
  gap: 6px;
  inline-size: 100%;
}

.dir-picker__head {
  display: flex;
  align-items: center;
  gap: 4px;
  min-inline-size: 0;
}

.dir-picker__crumbs {
  min-inline-size: 0;
  overflow-wrap: anywhere;
}

.dir-picker__path {
  font-size: 12px;
  opacity: 0.75;
  overflow-wrap: anywhere;
}

.dir-picker__list {
  max-block-size: 240px;
  border: 1px solid rgba(128, 128, 128, 0.25);
  border-radius: 3px;
}

.dir-picker__empty {
  padding-block: 16px;
}

.dir-picker__item {
  display: flex;
  align-items: center;
  gap: 8px;
  inline-size: 100%;
  padding: 6px 10px;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: start;
  cursor: pointer;
}

.dir-picker__item:hover,
.dir-picker__item:focus-visible {
  background: rgba(128, 128, 128, 0.12);
}

.dir-picker__name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
