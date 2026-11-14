<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NIcon } from 'naive-ui'
import { CopyOutline, DownloadOutline } from '@vicons/ionicons5'
import { useCopy } from '@/composables/useCopy'
import { useSessionStore } from '@/stores/session'
import { downloadText } from '@/utils/download'

const props = defineProps<{ codes: string[] }>()
const emit = defineEmits<{ done: [] }>()

const { t } = useI18n()
const copyText = useCopy()
const session = useSessionStore()

function fileText(): string {
  const header = t('settings.recoveryCodes.fileHeader', {
    username: session.user?.username ?? '',
    host: window.location.host,
  })
  return `${header}\n\n${props.codes.join('\n')}\n`
}

function copy() {
  return copyText(props.codes.join('\n'))
}

function download() {
  downloadText(`cairn-recovery-codes-${session.user?.username ?? 'user'}.txt`, fileText())
}
</script>

<template>
  <div class="recovery">
    <NAlert type="warning" :title="t('settings.recoveryCodes.warningTitle')">
      {{ t('settings.recoveryCodes.warning') }}
    </NAlert>
    <ul class="recovery__codes" dir="ltr">
      <li v-for="code in codes" :key="code">{{ code }}</li>
    </ul>
    <div class="recovery__actions">
      <NButton @click="copy">
        <template #icon>
          <NIcon><CopyOutline /></NIcon>
        </template>
        {{ t('common.action.copy') }}
      </NButton>
      <NButton @click="download">
        <template #icon>
          <NIcon><DownloadOutline /></NIcon>
        </template>
        {{ t('common.action.download') }}
      </NButton>
      <NButton type="primary" class="recovery__done" @click="emit('done')">
        {{ t('settings.recoveryCodes.saved') }}
      </NButton>
    </div>
  </div>
</template>

<style scoped>
.recovery {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.recovery__codes {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 8px 24px;
  margin: 0;
  padding: 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 15px;
  list-style: none;
}

.recovery__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.recovery__done {
  margin-inline-start: auto;
}
</style>
