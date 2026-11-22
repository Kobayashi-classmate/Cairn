<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Copy } from '@lucide/vue'
import Notice from '@/components/common/Notice.vue'
import { keepOpenOnCompanion, keepOpenOnEscape, trackOverlay } from '@/components/common/overlayGuard'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { copyText } from '@/lib/notify'

// The secret of a new API token, shown this once. An alert dialog: clicking
// outside does not close it, so the token is not lost by accident; only
// "Done" (or Escape) does, and the secret is then forgotten.
const props = defineProps<{
  /** The token name and secret; null when nothing is shown. */
  token: { name: string; secret: string } | null
}>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const open = computed(() => props.token !== null)
trackOverlay(() => open.value)

function onOpenChange(value: boolean) {
  if (!value) emit('close')
}
</script>

<template>
  <AlertDialog :open="open" @update:open="onOpenChange">
    <AlertDialogContent
      v-if="token"
      class="sm:max-w-[520px]"
      @interact-outside="keepOpenOnCompanion"
      @escape-key-down="keepOpenOnEscape"
    >
      <div class="flex flex-col gap-4">
        <div class="flex flex-col gap-2">
          <AlertDialogTitle class="text-title [overflow-wrap:anywhere]">
            {{ t('tokens.created.title', { name: token.name }) }}
          </AlertDialogTitle>
          <AlertDialogDescription class="m-0 text-muted-foreground">{{ t('tokens.created.intro') }}</AlertDialogDescription>
        </div>
        <div class="flex min-w-0 items-stretch gap-2">
          <code class="min-w-0 flex-1 rounded-md border bg-muted px-3 py-2 font-mono text-[13px] [overflow-wrap:anywhere] select-all" dir="ltr">
            {{ token.secret }}
          </code>
          <Button variant="outline" class="h-auto max-md:min-h-11" @click="copyText(token.secret)">
            <Copy aria-hidden="true" />
            {{ t('common.action.copy') }}
          </Button>
        </div>
        <Notice tone="warning">{{ t('tokens.created.warning') }}</Notice>
        <div class="flex justify-end">
          <Button class="max-md:h-11" @click="emit('close')">{{ t('tokens.created.done') }}</Button>
        </div>
      </div>
    </AlertDialogContent>
  </AlertDialog>
</template>
