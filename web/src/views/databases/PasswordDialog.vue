<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Copy } from '@lucide/vue'
import { keepOpenOnCompanion, keepOpenOnEscape, trackOverlay } from '@/components/common/overlayGuard'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { copyText } from '@/lib/notify'

/** Shows a revealed password once, with a copy button; it is forgotten when closed. */
const password = defineModel<string | null>('password', { required: true })
defineProps<{ title: string }>()

const { t } = useI18n()
const open = computed(() => password.value !== null)
trackOverlay(() => open.value)

function onOpenChange(value: boolean) {
  if (!value) password.value = null
}
</script>

<template>
  <Dialog :open="open" @update:open="onOpenChange">
    <DialogContent class="sm:max-w-[460px]" @interact-outside="keepOpenOnCompanion" @escape-key-down="keepOpenOnEscape">
      <div class="flex flex-col gap-1.5 pe-6">
        <DialogTitle class="text-title [overflow-wrap:anywhere]">{{ title }}</DialogTitle>
        <DialogDescription class="m-0 text-detail text-muted-foreground">{{ t('databases.password.hint') }}</DialogDescription>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <code class="min-w-0 flex-[1_1_220px] rounded-md bg-muted px-3 py-2 font-mono text-[13px] select-all [overflow-wrap:anywhere]" dir="ltr">
          {{ password }}
        </code>
        <Button variant="outline" class="max-md:h-11" @click="password && copyText(password)">
          <Copy aria-hidden="true" />
          {{ t('common.action.copy') }}
        </Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
