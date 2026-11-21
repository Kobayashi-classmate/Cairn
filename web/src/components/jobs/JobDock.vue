<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useResizeObserver } from '@vueuse/core'
import { ChevronDown, ChevronUp } from '@lucide/vue'
import { openOverlays } from '@/components/common/overlayGuard'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { isFinalState } from '@/jobs/labels'
import { useJobDockStore } from '@/stores/jobDock'
import { useSessionStore } from '@/stores/session'
import JobDockCard from './JobDockCard.vue'

// The job dock in the bottom right corner (interface spec, section 7).
// Cards stay mounted while the dock is collapsed, so following goes on.
const { t } = useI18n()
const dock = useJobDockStore()
const session = useSessionStore()
const root = ref<HTMLElement | null>(null)

const running = computed(() => dock.entries.filter((entry) => !entry.job || !isFinalState(entry.job.state)).length)
const failed = computed(
  () => dock.entries.filter((entry) => entry.job?.state === 'failed' || entry.job?.state === 'interrupted').length,
)

const summary = computed(() => {
  const parts: string[] = []
  if (running.value > 0) parts.push(t('common.dock.running', { count: running.value }, running.value))
  if (failed.value > 0) parts.push(t('common.dock.failed', { count: failed.value }, failed.value))
  const rest = dock.entries.length - running.value - failed.value
  if (rest > 0) parts.push(t('common.dock.finished', { count: rest }, rest))
  return parts.join(' · ')
})

// While a sheet, drawer or dialog is open the dock moves out of the way of
// its footer: to the bottom left, or on phones (where sheets fill the
// screen) above the footer.
const aside = computed(() => openOverlays.value > 0)
watch(aside, (value) => {
  dock.aside = value
}, { immediate: true })

// Toasts sit above the dock instead of covering it.
useResizeObserver(root, (records) => {
  const box = records[0]?.borderBoxSize?.[0]
  dock.height = box ? Math.round(box.blockSize) : (root.value?.offsetHeight ?? 0)
})
watch(
  () => dock.entries.length,
  (count) => {
    if (count === 0) dock.height = 0
  },
)

// Another user signing in on this tab does not inherit the cards.
watch(
  () => session.user?.username,
  (now, before) => {
    if (before !== undefined && now !== before) dock.clear()
  },
)
</script>

<template>
  <div
    v-show="dock.entries.length > 0"
    ref="root"
    data-job-dock
    :class="
      cn(
        'fixed end-4 bottom-4 z-[60] flex w-[calc(100%-2rem)] flex-col gap-2 sm:w-[360px]',
        aside && 'max-sm:bottom-[84px] sm:start-4 sm:end-auto',
      )
    "
    :aria-label="t('common.dock.label')"
    role="region"
  >
    <div class="sr-only" role="status" aria-live="polite">{{ dock.announcement }}</div>

    <div
      v-if="dock.entries.length > 1 || dock.collapsed"
      class="flex items-center gap-2 rounded-lg border bg-popover py-1.5 ps-3.5 pe-1.5 text-detail shadow-overlay"
    >
      <span class="min-w-0 flex-1 truncate font-medium">{{ summary }}</span>
      <Button
        variant="ghost"
        size="sm"
        class="max-md:h-11"
        :aria-expanded="!dock.collapsed"
        @click="dock.collapsed = !dock.collapsed"
      >
        <ChevronUp v-if="dock.collapsed" aria-hidden="true" />
        <ChevronDown v-else aria-hidden="true" />
        {{ dock.collapsed ? t('common.dock.expand') : t('common.dock.collapse') }}
      </Button>
    </div>

    <div v-show="!dock.collapsed" class="flex max-h-[min(60vh,520px)] flex-col gap-2 overflow-y-auto">
      <JobDockCard v-for="entry in dock.entries" :key="entry.id" :entry="entry" />
    </div>
  </div>
</template>
