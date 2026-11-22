<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Download, ImageOff, Lock } from '@lucide/vue'
import { downloadUrl } from '@/api/files'
import { ensureElevated } from '@/auth/stepUp'
import { keepOpenOnCompanion, keepOpenOnEscape, trackOverlay } from '@/components/common/overlayGuard'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { absolutePath, baseName } from '@/files/paths'
import { linkNeedsStepUp, onDownloadClick } from './actions'

// An image the server serves inline (sandboxed, so an SVG runs nothing),
// in a dialog whose state is in the address (`?open=view:<path>`). Images
// outside the sites root load after a re-authentication, as downloads do.
const props = defineProps<{ root: string; path: string | null }>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const open = computed(() => props.path !== null)
trackOverlay(() => open.value)
const failed = ref(false)
const size = ref<{ width: number; height: number } | null>(null)
/** The image may load: from the sites root at once, from others once re-authenticated. */
const allowed = ref(false)
/** The re-authentication was declined; the image stays hidden until asked again. */
const declined = ref(false)
/**
 * The dialog has opened. The re-authentication is asked for only then: a
 * dialog opened after it would hide it from assistive technology and take
 * the keyboard focus.
 */
const shown = ref(false)

async function elevate() {
  declined.value = false
  try {
    await ensureElevated()
    allowed.value = true
  } catch {
    declined.value = true
  }
}

function onOpened() {
  shown.value = true
  if (props.path && !allowed.value) void elevate()
}

watch(open, (value) => {
  if (!value) shown.value = false
})

watch(
  () => [props.root, props.path] as const,
  ([root, path]) => {
    failed.value = false
    size.value = null
    declined.value = false
    allowed.value = !linkNeedsStepUp(root)
    if (path && !allowed.value && shown.value) void elevate()
  },
  { immediate: true },
)

const src = computed(() => (props.path && allowed.value ? downloadUrl(props.root, props.path, true) : ''))
const name = computed(() => (props.path ? baseName(props.path) : ''))

function onLoad(event: Event) {
  const image = event.target as HTMLImageElement
  size.value = { width: image.naturalWidth, height: image.naturalHeight }
}

function onOpenChange(value: boolean) {
  if (!value) emit('close')
}
</script>

<template>
  <Dialog :open="open" @update:open="onOpenChange">
    <DialogContent
      class="flex max-h-[calc(100dvh-2rem)] flex-col gap-4 sm:max-w-[min(960px,calc(100vw-2rem))]"
      @open-auto-focus="onOpened"
      @interact-outside="keepOpenOnCompanion"
      @escape-key-down="keepOpenOnEscape"
    >
      <div class="flex min-w-0 flex-col gap-1 pe-8">
        <DialogTitle class="truncate text-title" dir="auto">{{ name }}</DialogTitle>
        <DialogDescription class="m-0 flex flex-wrap gap-x-2 text-xs text-muted-foreground">
          <span class="min-w-0 font-mono [overflow-wrap:anywhere]" dir="ltr">{{ path ? absolutePath(root, path) : '' }}</span>
          <span v-if="size" class="whitespace-nowrap tabular-nums">· {{ t('files.preview.size', { width: size.width, height: size.height }) }}</span>
        </DialogDescription>
      </div>
      <div
        v-if="failed"
        class="flex flex-col items-center gap-2 rounded-md border bg-muted px-4 py-12 text-center text-muted-foreground"
        role="alert"
      >
        <ImageOff class="size-6" aria-hidden="true" />
        <p class="m-0">{{ t('files.preview.failed') }}</p>
      </div>
      <div v-else-if="declined" class="flex flex-col items-center gap-3 rounded-md border bg-muted px-4 py-12 text-center text-muted-foreground">
        <Lock class="size-6" aria-hidden="true" />
        <p class="m-0 font-medium text-foreground">{{ t('files.stepUp.image') }}</p>
        <p class="m-0 text-detail">{{ t('files.stepUp.text') }}</p>
        <Button variant="outline" size="sm" class="max-md:h-11" @click="elevate">{{ t('common.action.retry') }}</Button>
      </div>
      <div
        v-else
        class="flex min-h-0 flex-1 items-center justify-center overflow-auto rounded-md border bg-muted bg-[length:16px_16px] bg-[position:0_0,0_8px,8px_-8px,-8px_0] p-3 [background-image:linear-gradient(45deg,var(--track)_25%,transparent_25%),linear-gradient(-45deg,var(--track)_25%,transparent_25%),linear-gradient(45deg,transparent_75%,var(--track)_75%),linear-gradient(-45deg,transparent_75%,var(--track)_75%)]"
      >
        <img v-if="src" :src="src" :alt="name" class="max-h-[70vh] max-w-full object-contain" @load="onLoad" @error="failed = true" />
      </div>
      <div class="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
        <Button v-if="path" as="a" variant="outline" :href="downloadUrl(root, path)" :download="name" class="max-md:h-11" @click="onDownloadClick($event, root, path)" @auxclick="onDownloadClick($event, root, path)">
          <Download aria-hidden="true" />
          {{ t('files.action.download') }}
        </Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
