<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { CircleCheck, CircleSlash, CircleX, LoaderCircle, X } from '@lucide/vue'
import { isApiError } from '@/api/client'
import { cancelJob } from '@/api/jobs'
import { Button } from '@/components/ui/button'
import { useJobFollow } from '@/composables/useJobFollow'
import { errorText } from '@/i18n/errors'
import { formatPercent } from '@/i18n/format'
import { isFinalState, jobKindLabel, jobStateLabel } from '@/jobs/labels'
import { notifyError } from '@/lib/notify'
import { cn } from '@/lib/utils'
import { useJobDockStore, type DockEntry } from '@/stores/jobDock'

// One job in the dock: what it is, its current step (the last line of its
// log) or outcome, progress when the job reports it, "View log" and
// "Cancel" while it can be cancelled.
const props = defineProps<{ entry: DockEntry }>()

const { t } = useI18n()
const dock = useJobDockStore()
const { job, log, loadError, reconnecting, start } = useJobFollow()
const cancelling = ref(false)

onMounted(() => start(props.entry.id))

watch(job, (value) => {
  if (value) dock.update(props.entry.id, value)
})

watch(loadError, (err) => {
  // The job is gone or not visible to this user (signed in as someone else).
  if (isApiError(err) && (err.status === 404 || err.status === 403)) dock.lost(props.entry.id)
})

const current = computed(() => job.value ?? props.entry.job)
const final = computed(() => (current.value ? isFinalState(current.value.state) : false))
const title = computed(() => (current.value ? jobKindLabel(current.value.kind, current.value.params) : t('common.dock.loading')))

/** The last line the job wrote, as its current step. */
const step = computed(() => {
  const lines = log.value.split('\n')
  for (let i = lines.length - 1; i >= 0; i--) {
    const line = lines[i]?.trim()
    if (line) return line.length > 140 ? `${line.slice(0, 140)}…` : line
  }
  return ''
})

const failure = computed(() => {
  const error = current.value?.error
  return error ? (errorText(error.code, error.params) ?? error.code) : ''
})

const detail = computed(() => {
  const state = current.value?.state
  if (!state) return ''
  if (state === 'failed' && failure.value) return failure.value
  if (!final.value && step.value) return step.value
  return jobStateLabel(state)
})

const progress = computed(() => {
  const value = current.value?.progress
  return !final.value && typeof value === 'number' ? Math.min(100, Math.max(0, value * 100)) : null
})

const tone = computed(() => {
  switch (current.value?.state) {
    case 'succeeded':
      return { icon: CircleCheck, class: 'text-success' }
    case 'failed':
    case 'interrupted':
      return { icon: CircleX, class: 'text-destructive' }
    case 'canceled':
      return { icon: CircleSlash, class: 'text-muted-foreground' }
    default:
      return { icon: LoaderCircle, class: 'animate-spin text-primary' }
  }
})

async function cancel() {
  cancelling.value = true
  try {
    const updated = await cancelJob(props.entry.id)
    dock.update(props.entry.id, updated)
  } catch (err) {
    notifyError(err)
  } finally {
    cancelling.value = false
  }
}
</script>

<template>
  <article
    class="flex flex-col gap-2.5 rounded-lg border bg-popover px-4 py-3.5 text-popover-foreground shadow-overlay"
    :aria-label="title"
  >
    <div class="flex items-start gap-2.5">
      <component :is="tone.icon" :class="cn('mt-0.5 size-4 shrink-0', tone.class)" aria-hidden="true" />
      <div class="flex min-w-0 flex-1 flex-col gap-0.5">
        <span class="font-semibold [overflow-wrap:anywhere]">{{ title }}</span>
        <span
          v-if="detail"
          :class="cn('text-detail [overflow-wrap:anywhere]', current?.state === 'failed' ? 'text-destructive' : 'text-muted-foreground')"
          :dir="!final && step ? 'ltr' : undefined"
        >
          {{ detail }}
        </span>
        <span v-if="reconnecting && !final" class="text-xs text-muted-foreground">{{ t('common.dock.reconnecting') }}</span>
      </div>
      <Button
        variant="ghost"
        size="icon-sm"
        class="-me-1.5 -mt-0.5 max-md:size-11"
        :aria-label="final ? t('common.action.dismiss') : t('common.dock.hide')"
        :title="final ? t('common.action.dismiss') : t('common.dock.hide')"
        @click="dock.dismiss(entry.id)"
      >
        <X aria-hidden="true" />
      </Button>
    </div>

    <div v-if="progress !== null" class="flex items-center gap-3">
      <div
        role="progressbar"
        :aria-label="title"
        aria-valuemin="0"
        aria-valuemax="100"
        :aria-valuenow="Math.round(progress)"
        class="h-1.5 flex-1 overflow-hidden rounded-full bg-track"
      >
        <div class="h-full rounded-full bg-primary transition-[width] duration-200" :style="{ width: `${progress}%` }" />
      </div>
      <span class="text-xs text-muted-foreground tabular-nums">{{ formatPercent(progress, 0) }}</span>
    </div>

    <div class="flex flex-wrap items-center gap-2">
      <Button as-child variant="outline" size="sm" class="max-md:h-11">
        <RouterLink :to="{ name: 'job', params: { id: entry.id } }">{{ t('common.dock.viewLog') }}</RouterLink>
      </Button>
      <Button
        v-if="!final && current?.cancelable"
        variant="ghost"
        size="sm"
        class="text-muted-foreground max-md:h-11"
        :disabled="cancelling"
        @click="cancel"
      >
        {{ t('common.dock.cancel') }}
      </Button>
    </div>
  </article>
</template>
