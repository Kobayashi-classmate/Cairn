<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Progress of the uploads of this browser tab.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NCard, NProgress, NTag, NText } from 'naive-ui'
import { absolutePath, joinPath } from '@/files/paths'
import { errorMessage } from '@/i18n/errors'
import { formatBytes } from '@/i18n/format'
import { useUploadsStore, type UploadItem } from '@/stores/uploads'

const { t } = useI18n()
const uploads = useUploadsStore()

const finished = computed(() => uploads.items.some((item) => ['done', 'failed', 'canceled'].includes(item.state)))

function percent(item: UploadItem): number {
  return item.size > 0 ? Math.min(100, Math.round((item.sent / item.size) * 100)) : item.state === 'done' ? 100 : 0
}

function stateType(item: UploadItem): 'default' | 'info' | 'success' | 'error' | 'warning' {
  switch (item.state) {
    case 'done':
      return 'success'
    case 'failed':
      return 'error'
    case 'canceled':
      return 'warning'
    case 'uploading':
    case 'placing':
      return 'info'
    default:
      return 'default'
  }
}

/** The server already has a file of this name: offer to replace it. */
function exists(item: UploadItem): boolean {
  return item.state === 'failed' && item.error?.code === 'file.exists'
}
</script>

<template>
  <NCard v-if="uploads.items.length > 0" size="small" :title="t('files.upload.title')" class="uploads">
    <template #header-extra>
      <NButton v-if="finished" size="tiny" quaternary @click="uploads.clearFinished()">{{ t('files.upload.clear') }}</NButton>
    </template>
    <ul class="uploads__list">
      <li v-for="item in uploads.items" :key="item.key" class="uploads__item">
        <div class="uploads__row">
          <span class="uploads__name" :title="absolutePath(item.root, joinPath(item.dir, item.name))">{{ item.name }}</span>
          <NText depth="3" class="uploads__size">{{ formatBytes(item.size) }}</NText>
          <NTag size="small" :type="stateType(item)" :bordered="false">{{ t(`files.upload.state.${item.state}`) }}</NTag>
          <span class="uploads__spacer" />
          <NButton
            v-if="item.state === 'queued' || item.state === 'uploading'"
            size="tiny"
            quaternary
            @click="uploads.cancel(item.key)"
          >
            {{ t('common.action.cancel') }}
          </NButton>
          <template v-if="item.state === 'failed' && uploads.canRetry(item.key)">
            <NButton v-if="exists(item)" size="tiny" quaternary type="warning" @click="uploads.retry(item.key, true)">
              {{ t('files.upload.replace') }}
            </NButton>
            <NButton v-else size="tiny" quaternary @click="uploads.retry(item.key)">{{ t('files.upload.retry') }}</NButton>
          </template>
        </div>
        <NProgress
          v-if="item.state === 'uploading' || item.state === 'placing'"
          type="line"
          :percentage="percent(item)"
          :processing="item.state === 'placing'"
          :show-indicator="false"
          :height="4"
        />
        <NText v-if="item.error" type="error" class="uploads__error">{{ errorMessage(item.error) }}</NText>
      </li>
    </ul>
  </NCard>
</template>

<style scoped>
.uploads {
  margin-block-end: 12px;
}

.uploads__list {
  margin: 0;
  padding: 0;
  list-style: none;
  max-block-size: 240px;
  overflow-y: auto;
}

.uploads__item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding-block: 4px;
}

.uploads__row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-inline-size: 0;
}

.uploads__name {
  min-inline-size: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.uploads__size {
  font-size: 12px;
  white-space: nowrap;
}

.uploads__spacer {
  flex: 1;
}

.uploads__error {
  font-size: 12px;
}
</style>
