<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { isAbortError } from '@/api/client'
import { checkSchedule, TIMEOUT_DEFAULT, type CronTask, type ScheduleInfo } from '@/api/cron'
import Card from '@/components/common/Card.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import Notice from '@/components/common/Notice.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { useNavRoutes } from '@/composables/useNavRoutes'
import { describeSchedule } from '@/cron/schedule'
import { formatDateTime, formatMinutes } from '@/i18n/format'
import { useCronStore } from '@/stores/cron'
import { useSessionStore } from '@/stores/session'
import { resultText, resultTone, taskFailure, taskState } from '../labels'

// A task at a glance: what it runs and as whom, when (with the next runs as
// systemd reads the schedule), its limits and how the last run went.
const props = defineProps<{ task: CronTask }>()
const emit = defineEmits<{ go: [section: string] }>()

const { t } = useI18n()
const store = useCronStore()
const session = useSessionStore()
const navRoutes = useNavRoutes()

const info = ref<ScheduleInfo | null>(null)
let controller: AbortController | null = null

async function loadSchedule() {
  controller?.abort()
  const request = new AbortController()
  controller = request
  try {
    info.value = await checkSchedule(props.task.schedule, request.signal)
  } catch (err) {
    if (!isAbortError(err)) info.value = null
  }
}

watch(() => props.task.schedule, () => void loadSchedule(), { immediate: true })
onBeforeUnmount(() => controller?.abort())

const state = computed(() => taskState(props.task))
const failure = computed(() => taskFailure(props.task))
const planLink = computed(() => props.task.type === 'backup' && navRoutes.value.has('backups') && session.can('backups.read'))
const planName = computed(() => store.planNames[props.task.plan ?? ''] ?? props.task.plan ?? '')
</script>

<template>
  <Notice v-if="task.state === 'failed' && failure" tone="destructive">{{ failure }}</Notice>
  <Notice v-else-if="task.state === 'applying'" tone="info">{{ t('cron.drawer.applying') }}</Notice>

  <Card :title="t('cron.drawer.lastRun')" :level="3" flush>
    <template #actions>
      <button type="button" class="text-detail font-medium text-link hover:underline" @click="emit('go', 'runs')">{{ t('cron.action.history') }}</button>
    </template>
    <KeyValueList>
      <KeyValueRow :label="t('cron.column.lastRun')">
        <StatusDot v-if="task.running" tone="progress">{{ resultText('running') }}</StatusDot>
        <template v-else-if="task.lastRun">
          <StatusDot v-if="task.result" :tone="resultTone(task.result)">{{ resultText(task.result) }}</StatusDot>
          <RelativeTime :value="task.lastRun" class="text-muted-foreground" />
        </template>
        <span v-else class="text-muted-foreground">{{ t('cron.never') }}</span>
      </KeyValueRow>
      <KeyValueRow :label="t('cron.column.nextRun')">
        <RelativeTime v-if="task.enabled && task.nextRun" :value="task.nextRun" />
        <span v-else class="text-muted-foreground">—</span>
      </KeyValueRow>
      <KeyValueRow :label="t('cron.column.state')">
        <StatusPill :tone="state.tone">{{ state.text }}</StatusPill>
      </KeyValueRow>
    </KeyValueList>
  </Card>

  <Card :title="t('cron.drawer.what')" :level="3" flush>
    <KeyValueList>
      <KeyValueRow :label="t('common.field.type')">{{ t(`cron.type.${task.type}`) }}</KeyValueRow>
      <KeyValueRow v-if="task.type === 'shell'" :label="t('cron.task.user')" mono>{{ task.user }}</KeyValueRow>
      <KeyValueRow v-if="task.type === 'url'" :label="t('common.field.url')" mono>
        <span dir="ltr" class="[overflow-wrap:anywhere]">{{ task.url }}</span>
      </KeyValueRow>
      <KeyValueRow v-if="task.type === 'backup'" :label="t('common.field.plan')">
        <RouterLink v-if="planLink" :to="{ name: 'backups', query: { open: `plan:${task.plan}` } }" class="text-link hover:underline">{{ planName }}</RouterLink>
        <span v-else>{{ planName }}</span>
      </KeyValueRow>
      <KeyValueRow :label="t('cron.task.timeout')">{{ formatMinutes(task.timeoutMin || TIMEOUT_DEFAULT) }}</KeyValueRow>
    </KeyValueList>
    <pre
      v-if="task.type === 'shell' && task.script"
      class="m-0 max-h-72 overflow-auto border-t bg-muted/50 px-4 py-3 font-mono text-xs whitespace-pre md:px-[18px]"
      dir="ltr"
      :aria-label="t('common.field.script')"
    >{{ task.script }}</pre>
  </Card>

  <Card :title="t('common.field.schedule')" :level="3" flush>
    <KeyValueList>
      <KeyValueRow :label="t('cron.drawer.when')">{{ describeSchedule(task.schedule) }}</KeyValueRow>
      <KeyValueRow :label="t('cron.drawer.expression')" mono>{{ info?.normalized ?? task.schedule }}</KeyValueRow>
      <KeyValueRow v-if="info && info.next.length > 0 && task.enabled" :label="t('cron.drawer.nextRuns')">
        <span class="flex flex-col tabular-nums sm:items-end">
          <span v-for="time in info.next" :key="time">{{ formatDateTime(time) }}</span>
        </span>
      </KeyValueRow>
    </KeyValueList>
  </Card>
</template>
