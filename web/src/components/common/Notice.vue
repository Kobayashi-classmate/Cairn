<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, type HTMLAttributes } from 'vue'
import { useI18n } from 'vue-i18n'
import { CircleAlert, CircleCheck, Info, TriangleAlert, X } from '@lucide/vue'
import { cn } from '@/lib/utils'

// One-line notice (interface spec, section 7): an icon, one sentence and at
// most one action, optionally dismissible.
const props = withDefaults(
  defineProps<{
    tone?: 'info' | 'success' | 'warning' | 'destructive'
    dismissible?: boolean
    class?: HTMLAttributes['class']
  }>(),
  { tone: 'info', class: undefined },
)

const emit = defineEmits<{ dismiss: [] }>()

const { t } = useI18n()

const TONES = {
  info: { icon: Info, box: 'bg-primary-soft text-primary-soft-foreground' },
  success: { icon: CircleCheck, box: 'bg-success-soft text-success' },
  warning: { icon: TriangleAlert, box: 'bg-warning-soft text-warning' },
  destructive: { icon: CircleAlert, box: 'bg-destructive-soft text-destructive' },
} as const

const tone = computed(() => TONES[props.tone])
</script>

<template>
  <div
    :role="props.tone === 'destructive' ? 'alert' : 'status'"
    :class="cn('flex items-start gap-3 rounded-lg px-4 py-2.5 text-body', tone.box, props.class)"
  >
    <component :is="tone.icon" class="mt-[2.5px] size-4 shrink-0" aria-hidden="true" />
    <div class="flex min-w-0 flex-1 flex-wrap items-center gap-x-4 gap-y-1">
      <div class="min-w-0 flex-1 basis-60 font-medium">
        <slot />
      </div>
      <!-- On phones the actions are at least 44px tall to tap. -->
      <div
        v-if="$slots.action"
        class="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 font-medium max-md:[&>a]:inline-flex max-md:[&>a]:min-h-11 max-md:[&>a]:items-center max-md:[&>button]:min-h-11"
      >
        <slot name="action" />
      </div>
    </div>
    <button
      v-if="dismissible"
      type="button"
      class="-my-0.5 -me-1.5 inline-flex size-7 shrink-0 items-center justify-center rounded-sm opacity-80 outline-none hover:opacity-100 focus-visible:ring-2 focus-visible:ring-ring/50 max-md:-my-3 max-md:-me-3 max-md:size-11"
      :aria-label="t('common.action.dismiss')"
      @click="emit('dismiss')"
    >
      <X class="size-4" aria-hidden="true" />
    </button>
  </div>
</template>
