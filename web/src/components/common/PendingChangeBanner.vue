<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, type HTMLAttributes } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check, ChevronRight, CircleCheck, LoaderCircle, ShieldAlert, TriangleAlert, X } from '@lucide/vue'
import { cn } from '@/lib/utils'

// A change that could lock people out (interface spec, section 7 "pending
// changes"): the steps it goes through (applied, confirmed, saved), where
// it stands, the time left and the actions on the right ("Roll back").
// Used at the top of the firewall and SSH pages.
const props = withDefaults(
  defineProps<{
    title: string
    description?: string
    /** The steps, in order ("Applied", "Confirm", "Saved"). */
    steps: readonly string[]
    /** Index of the step in progress; earlier steps are done, `steps.length` means all are. */
    current: number
    /**
     * progress: under way on its own; waiting: needs someone to act;
     * warning: being undone; success: done.
     */
    tone?: 'progress' | 'waiting' | 'warning' | 'success'
    /** The time left in words ("Rolled back in 42 s unless confirmed"). */
    countdown?: string
    /** Share of the time left, 0–100, drawn as a thin bar. */
    timeLeft?: number | null
    dismissible?: boolean
    class?: HTMLAttributes['class']
  }>(),
  { description: undefined, tone: 'progress', countdown: undefined, timeLeft: null, class: undefined },
)

const emit = defineEmits<{ dismiss: [] }>()

const { t } = useI18n()

const TONES = {
  progress: { box: 'border-primary/30 bg-primary-soft', icon: LoaderCircle, iconClass: 'animate-spin text-primary', bar: 'bg-primary' },
  waiting: { box: 'border-warning/40 bg-warning-soft', icon: ShieldAlert, iconClass: 'text-warning', bar: 'bg-warning' },
  warning: { box: 'border-warning/40 bg-warning-soft', icon: TriangleAlert, iconClass: 'text-warning', bar: 'bg-warning' },
  success: { box: 'border-success/30 bg-success-soft', icon: CircleCheck, iconClass: 'text-success', bar: 'bg-success' },
} as const

const tone = computed(() => TONES[props.tone])

type StepState = 'done' | 'current' | 'next'

function stepState(index: number): StepState {
  if (index < props.current) return 'done'
  return index === props.current ? 'current' : 'next'
}

/** What a screen reader hears after each step. */
const STATE_TEXT: Record<StepState, string> = { done: 'common.pending.stepDone', current: 'common.pending.stepCurrent', next: 'common.pending.stepNext' }

const bar = computed(() => (props.timeLeft === null ? null : Math.max(0, Math.min(100, props.timeLeft))))
</script>

<template>
  <section :aria-label="title" :class="cn('flex flex-col gap-3 rounded-lg border px-4 py-3.5 md:px-[18px]', tone.box, props.class)">
    <div class="flex flex-wrap items-start gap-x-4 gap-y-3">
      <component :is="tone.icon" :class="cn('mt-0.5 size-[18px] shrink-0', tone.iconClass)" aria-hidden="true" />
      <div class="flex min-w-0 flex-[1_1_240px] flex-col gap-1">
        <p class="m-0 font-semibold" role="status">{{ title }}</p>
        <p v-if="description || $slots.description" class="m-0 text-detail text-muted-foreground [overflow-wrap:anywhere]">
          <slot name="description">{{ description }}</slot>
        </p>
        <ol class="m-0 mt-1.5 flex list-none flex-col gap-1.5 p-0 sm:flex-row sm:flex-wrap sm:items-center sm:gap-x-2">
          <li
            v-for="(step, index) in steps"
            :key="index"
            :aria-current="stepState(index) === 'current' ? 'step' : undefined"
            class="flex items-center gap-2"
          >
            <ChevronRight v-if="index > 0" class="hidden size-3.5 shrink-0 text-muted-foreground sm:block" aria-hidden="true" />
            <span
              :class="
                cn(
                  'inline-flex items-center gap-1.5 text-detail',
                  stepState(index) === 'done' && 'font-medium text-success',
                  stepState(index) === 'current' && 'font-semibold text-foreground',
                  stepState(index) === 'next' && 'text-muted-foreground',
                )
              "
            >
              <Check v-if="stepState(index) === 'done'" class="size-3.5 shrink-0" aria-hidden="true" />
              <LoaderCircle
                v-else-if="stepState(index) === 'current' && props.tone === 'progress'"
                class="size-3.5 shrink-0 animate-spin text-primary"
                aria-hidden="true"
              />
              <span
                v-else-if="stepState(index) === 'current'"
                :class="cn('size-2 shrink-0 rounded-full', props.tone === 'success' ? 'bg-success' : 'bg-warning')"
                aria-hidden="true"
              />
              <span v-else class="size-2 shrink-0 rounded-full border border-muted-foreground" aria-hidden="true" />
              {{ step }}
              <span class="sr-only">({{ t(STATE_TEXT[stepState(index)]) }})</span>
            </span>
          </li>
        </ol>
      </div>
      <div v-if="$slots.actions || dismissible" class="flex w-full flex-wrap items-center gap-2 sm:w-auto sm:shrink-0">
        <slot name="actions" />
        <button
          v-if="dismissible"
          type="button"
          class="inline-flex size-8 shrink-0 items-center justify-center rounded-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 max-md:size-11"
          :aria-label="t('common.action.dismiss')"
          :title="t('common.action.dismiss')"
          @click="emit('dismiss')"
        >
          <X class="size-4" aria-hidden="true" />
        </button>
      </div>
    </div>
    <div v-if="countdown || bar !== null" class="flex flex-col gap-1.5">
      <p v-if="countdown" class="m-0 text-xs text-muted-foreground tabular-nums">{{ countdown }}</p>
      <div v-if="bar !== null" class="h-1 overflow-hidden rounded-full bg-track" aria-hidden="true">
        <div :class="cn('h-full rounded-full transition-[width] duration-200', tone.bar)" :style="{ width: `${bar}%` }" />
      </div>
    </div>
  </section>
</template>
