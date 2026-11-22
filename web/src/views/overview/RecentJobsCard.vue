<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { ArrowRight, CircleCheck, CircleSlash, CircleX, Clock, ListChecks, LoaderCircle } from '@lucide/vue'
import { isAbortError } from '@/api/client'
import { fetchJobs, type Job } from '@/api/jobs'
import Card from '@/components/common/Card.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import SkeletonRows from '@/components/common/SkeletonRows.vue'
import { usePolling } from '@/composables/usePolling'
import { jobKindLabel, jobStateLabel } from '@/jobs/labels'
import { cn } from '@/lib/utils'

// The latest tasks with their outcome and when they ran.
const POLL_MS = 15_000

const { t } = useI18n()

const jobs = ref<Job[] | null>(null)
const error = ref<unknown>(null)

async function load(signal?: AbortSignal) {
  try {
    jobs.value = (await fetchJobs({ limit: 6 }, signal)).jobs
    error.value = null
  } catch (err) {
    if (!isAbortError(err)) error.value = err
  }
}

usePolling(load, POLL_MS)

function retry() {
  error.value = null
  void load()
}

function stateLook(state: string) {
  switch (state) {
    case 'succeeded':
      return { icon: CircleCheck, class: 'text-success' }
    case 'failed':
    case 'interrupted':
      return { icon: CircleX, class: 'text-destructive' }
    case 'running':
      return { icon: LoaderCircle, class: 'text-primary' }
    case 'canceled':
      return { icon: CircleSlash, class: 'text-muted-foreground' }
    default:
      return { icon: Clock, class: 'text-muted-foreground' }
  }
}

function when(job: Job): number {
  return job.finishedAt || job.startedAt || job.createdAt
}
</script>

<template>
  <Card :title="t('overview.jobs.title')" flush>
    <template #actions>
      <RouterLink :to="{ name: 'jobs' }" class="inline-flex items-center gap-1 text-detail font-medium text-link hover:underline tap-y">
        {{ t('overview.jobs.all') }}
        <ArrowRight class="size-3.5" aria-hidden="true" />
      </RouterLink>
    </template>
    <SkeletonRows v-if="!jobs && !error" :rows="4" />
    <ErrorState v-else-if="error && !jobs" :error="error" compact @retry="retry" />
    <EmptyState v-else-if="!jobs || jobs.length === 0" :icon="ListChecks" compact :title="t('overview.jobs.empty')" />
    <ul v-else class="m-0 list-none divide-y p-0">
      <li v-for="job in jobs" :key="job.id">
        <RouterLink
          :to="{ name: 'job', params: { id: job.id } }"
          class="flex min-h-11 items-center gap-3 px-4 py-[11px] text-foreground outline-none hover:bg-accent/50 focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset md:px-[18px]"
        >
          <component
            :is="stateLook(job.state).icon"
            :class="cn('size-4 shrink-0', stateLook(job.state).class)"
            aria-hidden="true"
          />
          <span class="min-w-0 flex-1 truncate">{{ jobKindLabel(job.kind, job.params) }}</span>
          <span :class="cn('shrink-0 text-detail font-medium', stateLook(job.state).class)">
            {{ jobStateLabel(job.state) }}
          </span>
          <span class="min-w-[72px] shrink-0 text-end text-xs text-muted-foreground">
            <RelativeTime v-if="when(job)" :value="when(job)" />
          </span>
        </RouterLink>
      </li>
    </ul>
  </Card>
</template>
