<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Copy, Download } from '@lucide/vue'
import Notice from '@/components/common/Notice.vue'
import { Button } from '@/components/ui/button'
import { copyText } from '@/lib/notify'
import { useSessionStore } from '@/stores/session'
import { downloadText } from '@/utils/download'

// Recovery codes, shown once after turning on two-step verification or
// generating new ones: copy, download, then confirm they are saved.
const props = defineProps<{ codes: string[] }>()
const emit = defineEmits<{ done: [] }>()

const { t } = useI18n()
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
  <div class="flex flex-col gap-4">
    <Notice tone="warning">
      <span class="block font-semibold">{{ t('settings.recoveryCodes.warningTitle') }}</span>
      <span class="block font-normal">{{ t('settings.recoveryCodes.warning') }}</span>
    </Notice>
    <ul
      class="m-0 grid list-none grid-cols-2 gap-x-6 gap-y-2 rounded-md border bg-muted px-4 py-3 font-mono text-sm tabular-nums sm:grid-cols-3 md:grid-cols-4"
      dir="ltr"
      :aria-label="t('settings.recoveryCodes.listLabel')"
    >
      <li v-for="code in codes" :key="code" class="select-all">{{ code }}</li>
    </ul>
    <div class="flex flex-wrap items-center gap-2">
      <Button variant="outline" class="max-md:h-11" @click="copy">
        <Copy aria-hidden="true" />
        {{ t('common.action.copy') }}
      </Button>
      <Button variant="outline" class="max-md:h-11" @click="download">
        <Download aria-hidden="true" />
        {{ t('common.action.download') }}
      </Button>
      <Button class="ms-auto max-md:h-11" @click="emit('done')">{{ t('settings.recoveryCodes.saved') }}</Button>
    </div>
  </div>
</template>
