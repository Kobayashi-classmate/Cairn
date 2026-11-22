<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowUp, ChevronRight, Folder } from '@lucide/vue'
import { isAbortError } from '@/api/client'
import { listDir, type FileEntry } from '@/api/files'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { absolutePath, parentPath, pathSegments, rootLabel } from '@/files/paths'
import { errorMessage } from '@/i18n/errors'

// Picks a folder inside a file manager root by browsing: the folder shown
// is the one chosen. Used by forms (backup folders, restore destinations).
const path = defineModel<string>('path', { required: true })
const props = defineProps<{ root: string; label?: string }>()

const { t } = useI18n()
const dirs = ref<FileEntry[]>([])
const loading = ref(false)
const error = ref<unknown>(null)
let controller: AbortController | null = null

const segments = computed(() => pathSegments(path.value))

async function load() {
  controller?.abort()
  const request = new AbortController()
  controller = request
  loading.value = true
  error.value = null
  try {
    const list = await listDir(props.root, path.value, false, request.signal)
    dirs.value = list.entries.filter((entry) => entry.type === 'dir')
  } catch (err) {
    if (isAbortError(err)) return
    dirs.value = []
    error.value = err
  } finally {
    if (controller === request) {
      controller = null
      loading.value = false
    }
  }
}

watch([() => props.root, path], () => void load(), { immediate: true })
onBeforeUnmount(() => controller?.abort())
</script>

<template>
  <div class="flex min-w-0 flex-col gap-1.5" role="group" :aria-label="label">
    <div class="flex min-w-0 items-center gap-1">
      <Button type="button" variant="ghost" size="icon-sm" class="max-md:size-11" :disabled="!path" :aria-label="t('files.action.up')" @click="path = parentPath(path)">
        <ArrowUp aria-hidden="true" />
      </Button>
      <nav class="flex min-w-0 flex-wrap items-center gap-0.5 text-detail" :aria-label="t('common.dirBrowser.path')">
        <button type="button" class="rounded-sm px-1 font-medium outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50" @click="path = ''">
          {{ rootLabel(root) }}
        </button>
        <template v-for="segment in segments" :key="segment.path">
          <ChevronRight class="size-3.5 text-muted-foreground rtl:rotate-180" aria-hidden="true" />
          <button
            type="button"
            class="rounded-sm px-1 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50"
            :aria-current="segment.path === path ? 'location' : undefined"
            @click="path = segment.path"
          >
            {{ segment.name }}
          </button>
        </template>
      </nav>
    </div>
    <span class="font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]" dir="ltr">{{ absolutePath(root, path) }}</span>
    <div class="max-h-56 min-h-20 overflow-y-auto rounded-md border bg-card" :aria-busy="loading || undefined">
      <p v-if="error" role="alert" class="m-0 px-3 py-3 text-detail text-destructive">{{ errorMessage(error) }}</p>
      <div v-else-if="loading && dirs.length === 0" class="flex flex-col gap-2 px-3 py-3" aria-hidden="true">
        <Skeleton v-for="row in 3" :key="row" class="h-4" :style="{ width: `${70 - row * 12}%` }" />
      </div>
      <p v-else-if="dirs.length === 0" class="m-0 px-3 py-3 text-detail text-muted-foreground">{{ t('files.picker.empty') }}</p>
      <ul v-else class="m-0 list-none p-1">
        <li v-for="dir in dirs" :key="dir.path">
          <button
            type="button"
            class="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-start text-detail outline-none hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring/50 max-md:min-h-11"
            @click="path = dir.path"
          >
            <Folder class="size-4 shrink-0 text-primary" aria-hidden="true" />
            <span class="truncate">{{ dir.name }}</span>
          </button>
        </li>
      </ul>
    </div>
  </div>
</template>
