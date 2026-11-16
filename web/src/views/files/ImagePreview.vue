<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Shows an image the server serves inline (sandboxed, so an SVG runs nothing).
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NModal } from 'naive-ui'
import { downloadUrl } from '@/api/files'
import { absolutePath, baseName } from '@/files/paths'

const props = defineProps<{ root: string; path: string | null }>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const failed = ref(false)

watch(
  () => props.path,
  () => {
    failed.value = false
  },
)

const src = computed(() => (props.path ? downloadUrl(props.root, props.path, true) : ''))
</script>

<template>
  <NModal
    :show="path !== null"
    preset="card"
    :title="path ? baseName(path) : ''"
    class="image-preview"
    @update:show="(open: boolean) => !open && emit('close')"
  >
    <div class="image-preview__path" dir="ltr">{{ path ? absolutePath(root, path) : '' }}</div>
    <NAlert v-if="failed" type="error">{{ t('files.preview.failed') }}</NAlert>
    <div v-else class="image-preview__frame">
      <img :src="src" :alt="path ? baseName(path) : ''" class="image-preview__image" @error="failed = true" />
    </div>
    <div class="actions">
      <NButton v-if="path" tag="a" :href="downloadUrl(root, path)">{{ t('files.action.download') }}</NButton>
      <NButton @click="emit('close')">{{ t('sites.create.close') }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.image-preview {
  inline-size: min(960px, calc(100vw - 32px));
}

.image-preview__path {
  margin-block-end: 12px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  opacity: 0.75;
  overflow-wrap: anywhere;
}

.image-preview__frame {
  display: flex;
  justify-content: center;
  padding: 12px;
  background-color: rgba(128, 128, 128, 0.12);
  background-image:
    linear-gradient(45deg, rgba(128, 128, 128, 0.15) 25%, transparent 25%),
    linear-gradient(-45deg, rgba(128, 128, 128, 0.15) 25%, transparent 25%),
    linear-gradient(45deg, transparent 75%, rgba(128, 128, 128, 0.15) 75%),
    linear-gradient(-45deg, transparent 75%, rgba(128, 128, 128, 0.15) 75%);
  background-size: 16px 16px;
  background-position: 0 0, 0 8px, 8px -8px, -8px 0;
}

.image-preview__image {
  max-inline-size: 100%;
  max-block-size: 70vh;
  object-fit: contain;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
