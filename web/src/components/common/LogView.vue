<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch, type HTMLAttributes, type VNodeChild } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowDownToLine, Copy, WrapText } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { copyText } from '@/lib/notify'
import { cn } from '@/lib/utils'
import ErrorState from './ErrorState.vue'

// A log stream (interface spec, section 9 "LogView"): monospace lines, the
// newest at the bottom. While the reader is at the bottom it stays there as
// lines arrive; scrolled up, it stays put and offers "Jump to latest".
// Lines can wrap or scroll sideways, and the whole log can be copied. The
// lines themselves come from the default slot, which gets `{ wrap }`.
const props = withDefaults(
  defineProps<{
    /** Accessible name of the log. */
    label: string
    /** The text "Copy" puts on the clipboard; no Copy button without it. */
    copyText?: () => string
    /** Nothing to show yet. */
    loading?: boolean
    /** Why the log could not be read; shown in place of the lines when there are none. */
    error?: unknown
    /** No lines. */
    empty?: boolean
    emptyText?: string
    /** Classes of the scrolling box (its height). */
    boxClass?: HTMLAttributes['class']
    class?: HTMLAttributes['class']
  }>(),
  {
    copyText: undefined,
    error: undefined,
    emptyText: undefined,
    boxClass: 'max-h-[min(65vh,620px)]',
    class: undefined,
  },
)

const emit = defineEmits<{ retry: [] }>()

/** Long lines wrap instead of scrolling sideways. */
const wrap = defineModel<boolean>('wrap', { default: true })

defineSlots<{
  default?: (props: { wrap: boolean }) => VNodeChild
  /** Controls on the left of the log's own bar (filters, notes). */
  toolbar?: () => VNodeChild
}>()

const { t } = useI18n()
const box = ref<HTMLElement | null>(null)
const content = ref<HTMLElement | null>(null)
/** The reader is at (or near) the newest line. */
const pinned = ref(true)
/** The log is taller than its box. */
const scrollable = ref(false)
let observer: ResizeObserver | null = null

/** Within this many pixels of the end counts as being at the end. */
const SLACK = 24

function measure() {
  const el = box.value
  if (!el) return
  scrollable.value = el.scrollHeight > el.clientHeight + 1
}

function onScroll() {
  const el = box.value
  if (!el) return
  pinned.value = el.scrollHeight - el.scrollTop - el.clientHeight < SLACK
  measure()
}

/** Scrolls to the newest line and keeps following it. */
function toLatest() {
  const el = box.value
  if (!el) return
  el.scrollTop = el.scrollHeight
  pinned.value = true
}

function onResize() {
  measure()
  if (pinned.value) toLatest()
}

onMounted(() => {
  observer = new ResizeObserver(onResize)
  if (content.value) observer.observe(content.value)
  if (box.value) observer.observe(box.value)
  void nextTick(toLatest)
})

onBeforeUnmount(() => observer?.disconnect())

// The content element comes and goes with the empty and error states.
watch(content, (el, old) => {
  if (old) observer?.unobserve(old)
  if (el) {
    observer?.observe(el)
    void nextTick(toLatest)
  }
})

async function copy() {
  if (props.copyText) await copyText(props.copyText())
}

defineExpose({ toLatest })
</script>

<template>
  <div :class="cn('relative flex min-w-0 flex-col overflow-hidden rounded-md border bg-card', props.class)">
    <div class="flex min-h-10 flex-wrap items-center gap-2 border-b bg-muted/60 px-3 py-1.5">
      <div class="flex min-w-0 flex-1 flex-wrap items-center gap-2">
        <slot name="toolbar" />
        <p v-if="error && !empty" role="alert" class="m-0 flex flex-wrap items-center gap-x-2 text-xs text-destructive">
          {{ t('common.state.refreshFailed') }}
          <button type="button" class="font-medium underline underline-offset-2 hover:no-underline" @click="emit('retry')">
            {{ t('common.action.retry') }}
          </button>
        </p>
      </div>
      <Button
        variant="ghost"
        size="icon-sm"
        class="max-md:size-11"
        :aria-pressed="wrap"
        :aria-label="t('common.log.wrap')"
        :title="t('common.log.wrap')"
        :class="wrap ? 'bg-accent text-foreground' : 'text-muted-foreground'"
        @click="wrap = !wrap"
      >
        <WrapText aria-hidden="true" />
      </Button>
      <Button
        v-if="props.copyText"
        variant="ghost"
        size="icon-sm"
        class="text-muted-foreground max-md:size-11"
        :disabled="empty"
        :aria-label="t('common.log.copy')"
        :title="t('common.log.copy')"
        @click="copy"
      >
        <Copy aria-hidden="true" />
      </Button>
    </div>

    <ErrorState v-if="error && empty" :error="error" compact @retry="emit('retry')" />
    <div v-else-if="loading && empty" class="flex flex-col gap-2 px-4 py-4" aria-hidden="true">
      <Skeleton v-for="row in 6" :key="row" class="h-3.5" :style="{ width: `${92 - row * 7}%` }" />
    </div>
    <p v-else-if="empty" class="m-0 px-4 py-6 text-center text-detail text-muted-foreground">
      {{ emptyText ?? t('common.log.empty') }}
    </p>
    <div
      v-show="!empty"
      ref="box"
      :class="cn('min-h-0 overflow-auto font-mono text-xs leading-relaxed', boxClass)"
      role="log"
      :aria-label="label"
      tabindex="0"
      dir="ltr"
      @scroll.passive="onScroll"
    >
      <div v-if="!empty" ref="content" :class="wrap ? 'min-w-0' : 'w-max min-w-full'">
        <slot :wrap="wrap" />
      </div>
    </div>

    <Button
      v-if="!empty && scrollable && !pinned"
      variant="outline"
      size="sm"
      class="absolute end-4 bottom-3 shadow-overlay max-md:h-11"
      @click="toLatest"
    >
      <ArrowDownToLine aria-hidden="true" />
      {{ t('common.log.latest') }}
    </Button>
  </div>
</template>
