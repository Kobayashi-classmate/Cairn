<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { CircleX, LayoutDashboard, ListChecks, ScrollText } from '@lucide/vue'
import { isApiError } from '@/api/client'
import { cancelJob, type Job } from '@/api/jobs'
import Card from '@/components/common/Card.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import LogView from '@/components/common/LogView.vue'
import Notice from '@/components/common/Notice.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import SettingsDrawer from '@/components/common/SettingsDrawer.vue'
import type { DrawerItem } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { useJobFollow } from '@/composables/useJobFollow'
import { useNow } from '@/composables/useNow'
import { errorText } from '@/i18n/errors'
import { formatPercent } from '@/i18n/format'
import { formatJobParam, isFinalState, jobActorLabel, jobKindLabel, jobStateLabel, sourceLabel } from '@/jobs/labels'
import { notifyError, notifySuccess } from '@/lib/notify'
import { cn } from '@/lib/utils'
import { jobDuration, jobErrorDetails, jobTone } from './labels'

// A job's drawer: ?open=job:<id>&section=overview|log. The overview has who
// started it, when, how long it ran, its parameters and its error; the log
// follows the job's output while it runs. Cancel sits in the header while
// the job can be cancelled.
type Section = 'overview' | 'log'

const emit = defineEmits<{ update: [job: Job] }>()

const { t } = useI18n()
const drawer = useDrawerRoute('job')
const now = useNow(1000)
const { job, log, trimmed, loadError, reconnecting, start, stop } = useJobFollow()
const canceling = ref(false)
const wrap = ref(true)

watch(
  () => drawer.id.value,
  (id) => {
    if (id) start(id)
    else stop()
  },
  { immediate: true },
)

// The list shows the same job: keep it current.
watch(job, (value) => {
  if (value) emit('update', value)
})

const items = computed<DrawerItem<Section>[]>(() => [
  { value: 'overview', label: t('jobs.drawer.overview'), icon: LayoutDashboard },
  { value: 'log', label: t('jobs.drawer.log'), icon: ScrollText },
])
const section = computed<Section>(() => items.value.find((item) => item.value === drawer.section.value)?.value ?? 'overview')

const current = computed(() => (job.value && job.value.id === drawer.id.value ? job.value : null))
const final = computed(() => (current.value ? isFinalState(current.value.state) : false))
const missing = computed(() => isApiError(loadError.value) && (loadError.value.status === 404 || loadError.value.status === 403))

const status = computed(() => (current.value ? { tone: jobTone(current.value.state), label: jobStateLabel(current.value.state) } : null))
const meta = computed(() => {
  const value = current.value
  if (!value) return ''
  return [jobActorLabel(value.username), sourceLabel(value.source)].join(' · ')
})

const duration = computed(() => (current.value ? jobDuration(current.value, now.value) : null))
const progress = computed(() => {
  const value = current.value
  return value && !final.value && typeof value.progress === 'number' ? Math.min(100, Math.max(0, value.progress * 100)) : null
})

/** The job's own parameters, shown as plain text. */
const params = computed(() =>
  Object.entries(current.value?.params ?? {}).map(([key, value]) => ({ key, value: formatJobParam(value) })),
)

const failure = computed(() => {
  const error = current.value?.error
  return error ? (errorText(error.code, error.params) ?? error.code) : ''
})
const details = computed(() => (current.value ? jobErrorDetails(current.value) : []))

async function cancel() {
  const value = current.value
  if (!value) return
  canceling.value = true
  try {
    const updated = await cancelJob(value.id)
    job.value = updated
    notifySuccess(t('jobs.detail.cancelRequested'))
  } catch (err) {
    notifyError(err)
  } finally {
    canceling.value = false
  }
}
</script>

<template>
  <SettingsDrawer
    :open="drawer.id.value !== null"
    :title="current ? jobKindLabel(current.kind, current.params) : t('jobs.detail.title')"
    :icon="ListChecks"
    :status="status"
    :meta="meta"
    :items="items"
    :section="section"
    :loading="!current && !loadError"
    :error="missing ? null : loadError && !current ? loadError : null"
    @close="drawer.close()"
    @update:section="drawer.setSection"
    @retry="drawer.id.value && start(drawer.id.value)"
  >
    <template v-if="current && current.cancelable && current.canCancel && !final" #actions>
      <Button variant="outline" size="sm" class="text-destructive hover:text-destructive max-md:h-11" :disabled="canceling" @click="cancel">
        <CircleX aria-hidden="true" />
        {{ t('jobs.detail.cancel') }}
      </Button>
    </template>

    <EmptyState v-if="missing" :title="t('jobs.drawer.unknown')" :text="t('jobs.drawer.unknownText')" />

    <template v-else-if="current">
      <Notice v-if="reconnecting && !final" tone="warning">{{ t('jobs.detail.reconnecting') }}</Notice>

      <template v-if="section === 'overview'">
        <Notice v-if="current.state === 'failed' || current.error" tone="destructive">
          <span class="flex flex-col gap-2">
            <span>{{ failure || jobStateLabel(current.state) }}</span>
            <code
              v-for="(detail, index) in details"
              :key="index"
              tabindex="0"
              class="block max-h-60 overflow-auto rounded-sm bg-card/60 px-2 py-1.5 font-mono text-xs font-normal whitespace-pre-wrap text-foreground [overflow-wrap:anywhere]"
              dir="ltr"
            >{{ detail }}</code>
          </span>
          <template #action>
            <button type="button" class="underline underline-offset-2 hover:no-underline" @click="drawer.setSection('log')">
              {{ t('jobs.drawer.openLog') }}
            </button>
          </template>
        </Notice>

        <div v-if="progress !== null" class="flex items-center gap-3">
          <div
            role="progressbar"
            :aria-label="t('jobs.drawer.progress')"
            aria-valuemin="0"
            aria-valuemax="100"
            :aria-valuenow="Math.round(progress)"
            class="h-1.5 flex-1 overflow-hidden rounded-full bg-track"
          >
            <div class="h-full rounded-full bg-primary transition-[width] duration-200" :style="{ width: `${progress}%` }" />
          </div>
          <span class="text-xs text-muted-foreground tabular-nums">{{ formatPercent(progress, 0) }}</span>
        </div>

        <Card :title="t('jobs.drawer.details')" :level="3" flush>
          <KeyValueList>
            <KeyValueRow :label="t('jobs.column.startedBy')">
              {{ jobActorLabel(current.username) }}
              <span class="text-muted-foreground">· {{ sourceLabel(current.source) }}</span>
            </KeyValueRow>
            <KeyValueRow :label="t('jobs.detail.created')">
              <RelativeTime :value="current.createdAt" />
            </KeyValueRow>
            <KeyValueRow :label="t('jobs.column.started')">
              <RelativeTime v-if="current.startedAt > 0" :value="current.startedAt" />
              <span v-else class="text-muted-foreground">{{ t('jobs.notStarted') }}</span>
            </KeyValueRow>
            <KeyValueRow v-if="current.finishedAt > 0" :label="t('jobs.detail.finished')">
              <RelativeTime :value="current.finishedAt" />
            </KeyValueRow>
            <KeyValueRow v-if="duration" :label="t('jobs.column.duration')">
              <span class="tabular-nums">{{ duration }}</span>
            </KeyValueRow>
            <KeyValueRow :label="t('jobs.drawer.id')" mono>{{ current.id }}</KeyValueRow>
          </KeyValueList>
        </Card>

        <Card v-if="params.length > 0" :title="t('jobs.drawer.params')" :level="3" flush>
          <KeyValueList>
            <KeyValueRow v-for="param in params" :key="param.key" :label="param.key" mono>
              <span dir="ltr">{{ param.value }}</span>
            </KeyValueRow>
          </KeyValueList>
        </Card>
      </template>

      <LogView
        v-else
        v-model:wrap="wrap"
        :label="t('jobs.drawer.log')"
        :empty="log === ''"
        :empty-text="final ? t('jobs.detail.noLog') : t('jobs.drawer.waitingLog')"
        :copy-text="() => log"
        box-class="max-h-[calc(100dvh-260px)] min-h-48"
      >
        <template v-if="trimmed || !final" #toolbar>
          <span v-if="!final" class="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
            <span class="size-1.5 animate-pulse rounded-full bg-primary" aria-hidden="true" />
            {{ t('jobs.drawer.following') }}
          </span>
          <span v-if="trimmed" class="text-xs text-muted-foreground">{{ t('jobs.detail.trimmed') }}</span>
        </template>
        <template #default="{ wrap: wrapping }">
          <pre :class="cn('m-0 px-3 py-2 font-mono text-xs leading-relaxed', wrapping ? 'whitespace-pre-wrap [overflow-wrap:anywhere]' : 'whitespace-pre')">{{ log }}</pre>
        </template>
      </LogView>
    </template>
  </SettingsDrawer>
</template>
