<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { NAlert, NProgress, NSpin } from 'naive-ui'
import type { Job } from '@/api/jobs'
import JobStateTag from '@/components/JobStateTag.vue'
import { useJobFollow } from '@/composables/useJobFollow'
import { errorText } from '@/i18n/errors'
import { formatPercent } from '@/i18n/format'
import { isFinalState, jobKindLabel } from '@/jobs/labels'

/** Inline progress of one job: state, progress bar, error and the latest log lines. */
const props = withDefaults(
  defineProps<{
    jobId: string
    /** How many log lines to show; the job page has the full log. */
    lines?: number
  }>(),
  { lines: 12 },
)
const emit = defineEmits<{ finished: [job: Job] }>()

const { t } = useI18n()
const { job, log, start } = useJobFollow()

watch(
  () => props.jobId,
  (id) => start(id),
  { immediate: true },
)

const final = computed(() => (job.value ? isFinalState(job.value.state) : false))

watch(final, (done) => {
  if (done && job.value) emit('finished', job.value)
})

const tail = computed(() => log.value.split('\n').filter(Boolean).slice(-props.lines).join('\n'))

const failure = computed(() => {
  const error = job.value?.error
  return error ? (errorText(error.code, error.params) ?? error.code) : ''
})

/** Technical details of a failure (`reason`, program `output` such as `nginx -t`), shown as is. */
const details = computed(() => {
  const params = job.value?.error?.params ?? {}
  return ['reason', 'output', 'detail']
    .map((key) => params[key])
    .filter((value): value is string => typeof value === 'string' && value !== '')
})
</script>

<template>
  <div class="job-progress">
    <div v-if="!job" class="job-progress__wait"><NSpin size="small" /></div>
    <template v-else>
      <div class="job-progress__head">
        <span class="job-progress__name">{{ jobKindLabel(job.kind, job.params) }}</span>
        <JobStateTag :state="job.state" />
        <RouterLink :to="{ name: 'job', params: { id: job.id } }" class="job-progress__link">
          {{ t('software.viewOutput') }}
        </RouterLink>
      </div>
      <NProgress
        v-if="job.progress !== null && !final"
        type="line"
        :percentage="Math.round(job.progress * 100)"
      >
        {{ formatPercent(job.progress * 100, 0) }}
      </NProgress>
      <NAlert v-if="final && failure" type="error">
        <div>{{ failure }}</div>
        <pre v-for="(detail, index) in details" :key="index" class="job-progress__output" dir="ltr">{{
          detail
        }}</pre>
      </NAlert>
      <pre v-if="tail" class="job-progress__log" dir="ltr">{{ tail }}</pre>
    </template>
  </div>
</template>

<style scoped>
.job-progress {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.job-progress__wait {
  padding-block: 8px;
}

.job-progress__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
}

.job-progress__name {
  font-weight: 500;
}

.job-progress__link {
  margin-inline-start: auto;
}

.job-progress__log,
.job-progress__output {
  max-block-size: 240px;
  margin: 0;
  padding: 8px 10px;
  overflow: auto;
  border-radius: 4px;
  background: rgb(128 128 128 / 12%);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.job-progress__output {
  margin-block-start: 8px;
}
</style>
