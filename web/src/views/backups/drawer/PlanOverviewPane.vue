<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { CalendarClock, Database, Folder, Globe } from '@lucide/vue'
import type { BackupPlan } from '@/api/backups'
import { targetLabel } from '@/backups/targets'
import Card from '@/components/common/Card.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { useNavRoutes } from '@/composables/useNavRoutes'
import { describeSchedule } from '@/cron/schedule'
import { errorText } from '@/i18n/errors'
import { formatBytes } from '@/i18n/format'
import { useBackupsStore } from '@/stores/backups'
import { useSessionStore } from '@/stores/session'

// A plan at a glance: what it backs up, where to, encryption, retention,
// the scheduled tasks that run it and how the last run went.
const props = defineProps<{ plan: BackupPlan }>()
const emit = defineEmits<{ schedule: []; go: [section: string] }>()

const { t } = useI18n()
const store = useBackupsStore()
const session = useSessionStore()
const navRoutes = useNavRoutes()

const TARGET_ICON = { site: Globe, database: Database, directory: Folder } as const

const tasks = computed(() => store.tasksOf(props.plan))
const canSchedule = computed(() => session.can('cron.manage'))
const cronLink = computed(() => navRoutes.value.has('cron') && session.can('cron.read'))
const last = computed(() => props.plan.lastRun)
const lastError = computed(() => {
  const err = last.value?.error
  return err ? (errorText(err.code, err.params ?? {}) ?? err.code) : ''
})
</script>

<template>
  <Card :title="t('backups.drawer.lastRun')" :level="3" flush>
    <template #actions>
      <button type="button" class="text-detail font-medium text-link hover:underline" @click="emit('go', 'runs')">
        {{ t('backups.action.runs') }}
      </button>
    </template>
    <KeyValueList v-if="last">
      <KeyValueRow :label="t('backups.runs.result')">
        <StatusDot :tone="last.ok ? 'success' : 'destructive'">{{ last.ok ? t('backups.runs.ok') : t('backups.runs.failed') }}</StatusDot>
      </KeyValueRow>
      <KeyValueRow :label="t('backups.runs.started')"><RelativeTime :value="last.started" /></KeyValueRow>
      <KeyValueRow v-if="last.ok" :label="t('files.column.size')">{{ formatBytes(last.size) }}</KeyValueRow>
      <KeyValueRow v-if="lastError" :label="t('backups.drawer.reason')"><span class="text-destructive">{{ lastError }}</span></KeyValueRow>
    </KeyValueList>
    <p v-else class="m-0 px-4 py-3 text-detail text-warning md:px-[18px]">{{ t('backups.plan.never') }}</p>
  </Card>

  <Card :title="t('backups.plan.targets')" :level="3" flush>
    <ul class="m-0 list-none divide-y p-0">
      <li v-for="target in plan.targets" :key="targetLabel(target)" class="flex items-center gap-2.5 px-4 py-2.5 md:px-[18px]">
        <component :is="TARGET_ICON[target.kind]" class="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span class="min-w-0 flex-1 [overflow-wrap:anywhere]" dir="auto">{{ targetLabel(target) }}</span>
        <StatusPill>{{ t(`backups.targetKind.${target.kind}`) }}</StatusPill>
      </li>
    </ul>
  </Card>

  <Card :title="t('backups.drawer.settings')" :level="3" flush>
    <KeyValueList>
      <KeyValueRow :label="t('backups.plan.storage')">
        {{ store.storageName(plan.storageId) ?? t('backups.plan.localOnly') }}
        <span v-if="plan.storageId && plan.keepLocal" class="text-muted-foreground">{{ t('backups.plan.alsoLocal') }}</span>
      </KeyValueRow>
      <KeyValueRow :label="t('backups.plan.encryption')">
        <StatusPill v-if="plan.recipients.length > 0" tone="success">
          {{ t('backups.plan.recipientCount', { count: plan.recipients.length }, plan.recipients.length) }}
        </StatusPill>
        <StatusPill v-else :tone="plan.storageId ? 'warning' : 'neutral'">{{ t('backups.plan.notEncrypted') }}</StatusPill>
      </KeyValueRow>
      <KeyValueRow :label="t('backups.plan.retention')">
        <span class="font-mono text-[12.5px]">{{ t('backups.retention.short', { ...plan.retention }) }}</span>
      </KeyValueRow>
    </KeyValueList>
    <p class="m-0 border-t px-4 py-2.5 text-xs text-muted-foreground md:px-[18px]">{{ t('backups.retention.explain', { ...plan.retention }) }}</p>
  </Card>

  <Card :title="t('backups.plan.scheduled')" :level="3" flush>
    <template v-if="canSchedule" #actions>
      <Button variant="outline" size="sm" @click="emit('schedule')">
        <CalendarClock aria-hidden="true" />
        {{ t('backups.action.schedule') }}
      </Button>
    </template>
    <ul v-if="tasks.length > 0" class="m-0 list-none divide-y p-0">
      <li v-for="task in tasks" :key="task.id" class="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5 md:px-[18px]">
        <span class="flex min-w-0 flex-[1_1_200px] flex-col">
          <RouterLink v-if="cronLink" :to="{ name: 'cron', query: { open: `task:${task.id}` } }" class="font-medium text-link hover:underline">
            {{ task.name }}
          </RouterLink>
          <span v-else class="font-medium">{{ task.name }}</span>
          <span class="text-xs text-muted-foreground">{{ describeSchedule(task.schedule) }}</span>
        </span>
        <StatusPill v-if="!task.enabled">{{ t('cron.state.disabled') }}</StatusPill>
        <span v-else-if="task.nextRun" class="text-xs text-muted-foreground">
          {{ t('backups.drawer.next') }} <RelativeTime :value="task.nextRun" />
        </span>
      </li>
    </ul>
    <p v-else class="m-0 px-4 py-3 text-detail text-muted-foreground md:px-[18px]">
      {{ plan.tasks.length > 0 ? t('backups.drawer.tasksUnknown', { count: plan.tasks.length }, plan.tasks.length) : t('backups.drawer.noTasks') }}
    </p>
  </Card>
</template>
