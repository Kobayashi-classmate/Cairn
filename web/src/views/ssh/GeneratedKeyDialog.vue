<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Copy, Download } from '@lucide/vue'
import type { GeneratedSshKey } from '@/api/ssh'
import Notice from '@/components/common/Notice.vue'
import { keepOpenOnCompanion, trackOverlay } from '@/components/common/overlayGuard'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { copyText } from '@/lib/notify'
import { downloadText } from '@/utils/download'

// A key pair made on the server, shown this once: the panel does not keep
// the private key. An alert dialog that neither a click outside nor Escape
// closes, so the key is not lost by accident; only "I have saved it" does,
// and the parent then drops the key.
const props = defineProps<{
  /** The new key pair, whose it is and whether a passphrase protects it; null when nothing is shown. */
  result: { user: string; encrypted: boolean; pair: GeneratedSshKey } | null
}>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const open = computed(() => props.result !== null)
trackOverlay(() => open.value)

const authorizedKeys = computed(() => {
  const user = props.result?.user ?? ''
  return user === 'root' ? '/root/.ssh/authorized_keys' : `~${user}/.ssh/authorized_keys`
})

function onOpenChange(value: boolean) {
  if (!value) emit('close')
}

/** Escape would throw the only copy of the key away. */
function keepOpen(event: KeyboardEvent) {
  event.preventDefault()
}

function download() {
  if (!props.result) return
  // A neutral type, so the browser saves the file under the exact name ssh expects.
  downloadText(props.result.pair.fileName, props.result.pair.privateKey, 'application/octet-stream')
}
</script>

<template>
  <AlertDialog :open="open" @update:open="onOpenChange">
    <AlertDialogContent
      v-if="result"
      class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-[600px]"
      @interact-outside="keepOpenOnCompanion"
      @escape-key-down="keepOpen"
    >
      <div class="flex min-w-0 flex-col gap-4">
        <div class="flex flex-col gap-2">
          <AlertDialogTitle class="text-title [overflow-wrap:anywhere]">
            {{ t('ssh.generate.created.title', { user: result.user }) }}
          </AlertDialogTitle>
          <AlertDialogDescription class="m-0 text-muted-foreground [overflow-wrap:anywhere]">
            {{ t('ssh.generate.created.intro', { file: authorizedKeys }) }}
          </AlertDialogDescription>
        </div>

        <Notice tone="warning">{{ t('ssh.generate.created.warning') }}</Notice>

        <div class="flex min-w-0 flex-col gap-1.5">
          <Label for="ssh-generated-private" class="text-detail font-medium">
            {{ t('ssh.generate.created.privateKey') }}
            <span class="font-mono font-normal text-muted-foreground" dir="ltr">{{ result.pair.fileName }}</span>
          </Label>
          <Textarea
            id="ssh-generated-private"
            :model-value="result.pair.privateKey"
            readonly
            rows="8"
            spellcheck="false"
            autocomplete="off"
            dir="ltr"
            aria-describedby="ssh-generated-private-hint"
            class="field-sizing-fixed h-44 resize-none font-mono text-xs [overflow-wrap:anywhere]"
          />
          <p id="ssh-generated-private-hint" class="m-0 text-xs text-muted-foreground">
            {{ result.encrypted ? t('ssh.generate.created.encrypted') : t('ssh.generate.created.unencrypted', { user: result.user }) }}
            {{ t('ssh.generate.created.save', { file: result.pair.fileName }) }}
          </p>
          <div class="flex flex-wrap gap-2">
            <Button
              variant="outline"
              class="max-md:h-11"
              :aria-label="t('ssh.generate.created.copyPrivate')"
              @click="copyText(result.pair.privateKey)"
            >
              <Copy aria-hidden="true" />
              {{ t('common.action.copy') }}
            </Button>
            <Button
              variant="outline"
              class="max-md:h-11"
              :aria-label="t('ssh.generate.created.downloadPrivate', { file: result.pair.fileName })"
              @click="download"
            >
              <Download aria-hidden="true" />
              {{ t('common.action.download') }}
            </Button>
          </div>
        </div>

        <div class="flex min-w-0 flex-col gap-1.5">
          <span class="text-detail font-medium">{{ t('ssh.generate.created.publicKey') }}</span>
          <div class="flex min-w-0 items-stretch gap-2">
            <code
              class="min-w-0 flex-1 rounded-md border bg-muted px-3 py-2 font-mono text-xs [overflow-wrap:anywhere] select-all"
              dir="ltr"
            >{{ result.pair.publicKey }}</code>
            <Button
              variant="outline"
              class="h-auto max-md:min-h-11"
              :aria-label="t('ssh.generate.created.copyPublic')"
              @click="copyText(result.pair.publicKey)"
            >
              <Copy aria-hidden="true" />
              {{ t('common.action.copy') }}
            </Button>
          </div>
        </div>

        <div class="flex justify-end">
          <Button class="max-md:h-11" @click="emit('close')">{{ t('ssh.generate.created.done') }}</Button>
        </div>
      </div>
    </AlertDialogContent>
  </AlertDialog>
</template>
