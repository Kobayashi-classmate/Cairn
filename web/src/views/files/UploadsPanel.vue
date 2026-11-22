<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Upload } from '@lucide/vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { absolutePath, joinPath } from '@/files/paths'
import { errorMessage } from '@/i18n/errors'
import { formatBytes, formatPercent } from '@/i18n/format'
import type { Tone } from '@/lib/tone'
import { useUploadsStore, type UploadItem, type UploadState } from '@/stores/uploads'

// The uploads of this browser tab, under the file table's toolbar: one row
// each with its progress, and Cancel, Retry or Replace where they apply.
const { t } = useI18n()
const uploads = useUploadsStore()

const finished = computed(() => uploads.items.some((item) => ['done', 'failed', 'canceled'].includes(item.state)))
const running = computed(() => uploads.items.filter((item) => ['queued', 'uploading', 'placing'].includes(item.state)).length)

const TONES: Record<UploadState, Tone> = {
  queued: 'neutral',
  uploading: 'progress',
  placing: 'progress',
  done: 'success',
  failed: 'destructive',
  canceled: 'neutral',
}

function percent(item: UploadItem): number {
  return item.size > 0 ? Math.min(100, (item.sent / item.size) * 100) : item.state === 'done' ? 100 : 0
}

/** The server already has a file of this name: offer to replace it. */
function exists(item: UploadItem): boolean {
  return item.state === 'failed' && item.error?.code === 'file.exists'
}
</script>

<template>
  <section v-if="uploads.items.length > 0" :aria-label="t('files.upload.title')" class="border-b bg-muted/40">
    <header class="flex flex-wrap items-center gap-2 px-4 pt-2.5 pb-1">
      <Upload class="size-4 text-muted-foreground" aria-hidden="true" />
      <h2 class="m-0 text-detail font-semibold">{{ t('files.upload.title') }}</h2>
      <span v-if="running > 0" class="text-xs text-muted-foreground" role="status">
        {{ t('files.upload.running', { count: running }, running) }}
      </span>
      <Button v-if="finished" variant="ghost" size="sm" class="ms-auto max-md:h-11" @click="uploads.clearFinished()">
        {{ t('files.upload.clear') }}
      </Button>
    </header>
    <ul class="m-0 flex max-h-56 list-none flex-col overflow-y-auto px-4 pb-2.5">
      <li v-for="item in uploads.items" :key="item.key" class="flex flex-col gap-1 border-t py-2 first:border-t-0">
        <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
          <span class="min-w-0 flex-[1_1_160px] truncate font-medium" :title="absolutePath(item.root, joinPath(item.dir, item.name))">
            {{ item.name }}
          </span>
          <span class="text-xs whitespace-nowrap text-muted-foreground tabular-nums">
            <template v-if="item.state === 'uploading'">{{ formatBytes(item.sent) }} / </template>{{ formatBytes(item.size) }}
          </span>
          <StatusPill :tone="TONES[item.state]">{{ t(`files.upload.state.${item.state}`) }}</StatusPill>
          <div class="ms-auto flex items-center gap-1">
            <Button
              v-if="item.state === 'queued' || item.state === 'uploading'"
              variant="ghost"
              size="sm"
              class="max-md:h-11"
              @click="uploads.cancel(item.key)"
            >
              {{ t('common.action.cancel') }}
            </Button>
            <template v-if="item.state === 'failed' && uploads.canRetry(item.key)">
              <Button v-if="exists(item)" variant="outline" size="sm" class="max-md:h-11" @click="uploads.retry(item.key, true)">
                {{ t('files.upload.replace') }}
              </Button>
              <Button v-else variant="outline" size="sm" class="max-md:h-11" @click="uploads.retry(item.key)">
                {{ t('files.upload.retry') }}
              </Button>
            </template>
          </div>
        </div>
        <div
          v-if="item.state === 'uploading' || item.state === 'placing'"
          class="h-1 overflow-hidden rounded-full bg-track"
          role="progressbar"
          :aria-label="item.name"
          aria-valuemin="0"
          aria-valuemax="100"
          :aria-valuenow="Math.round(percent(item))"
          :aria-valuetext="formatPercent(percent(item), 0)"
        >
          <div
            class="h-full rounded-full bg-primary transition-[width] duration-200"
            :class="item.state === 'placing' && 'animate-pulse'"
            :style="{ width: `${percent(item)}%` }"
          />
        </div>
        <p v-if="item.error" class="m-0 text-xs text-destructive">{{ errorMessage(item.error) }}</p>
      </li>
    </ul>
  </section>
</template>
