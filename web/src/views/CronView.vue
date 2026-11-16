<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Scheduled tasks: systemd timers that run a script, fetch a URL or run a
// backup plan, with each task's run history and output.
import { computed, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NDataTable,
  NDropdown,
  NEmpty,
  NH2,
  NIcon,
  NSpace,
  NSpin,
  NTag,
  NText,
  type DataTableColumns,
  type DropdownOption,
} from 'naive-ui'
import { AddOutline, EllipsisHorizontal } from '@vicons/ionicons5'
import { fetchPlans } from '@/api/backups'
import { deleteTask, fetchTasks, runTask, type CronList, type CronTask } from '@/api/cron'
import type { Job } from '@/api/jobs'
import JobProgress from '@/components/JobProgress.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useJobRun } from '@/composables/useJobRun'
import { describeSchedule } from '@/cron/schedule'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'
import { resultType } from './cron/labels'
import RunsDrawer from './cron/RunsDrawer.vue'
import TaskDialog from './cron/TaskDialog.vue'

const { t } = useI18n()
const confirm = useConfirm()
const session = useSessionStore()

const list = ref<CronList | null>(null)
const loading = ref(false)
const loadError = ref<unknown>(null)
const showDialog = ref(false)
const editing = ref<CronTask | null>(null)
const history = ref<CronTask | null>(null)
/** Backup plan names by id, for backup tasks. */
const planNames = ref<Record<string, string>>({})

const canManage = computed(() => session.can('cron.manage'))
const canRoot = computed(() => session.can('cron.root'))

async function load() {
  loading.value = true
  try {
    list.value = await fetchTasks()
    loadError.value = null
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

onMounted(load)
onMounted(async () => {
  if (!session.can('backups.read')) return
  try {
    planNames.value = Object.fromEntries((await fetchPlans()).map((plan) => [plan.id, plan.name]))
  } catch {
    // Backup tasks then show without their plan's name.
  }
})

const { jobId, busy, run, follow, finished, dismiss } = useJobRun((_job: Job) => void load())

function openTask(task: CronTask | null) {
  editing.value = task
  showDialog.value = true
}

async function remove(task: CronTask) {
  const ok = await confirm({
    title: t('cron.task.deleteTitle', { name: task.name }),
    content: t('cron.task.deleteText'),
    confirmText: t('common.action.delete'),
    danger: true,
  })
  if (ok) void run(() => deleteTask(task.id))
}

/** Root tasks can only be changed with the root permission. */
function changeable(task: CronTask): boolean {
  return canManage.value && (task.type !== 'shell' || task.user !== 'root' || canRoot.value)
}

function options(task: CronTask): DropdownOption[] {
  const list: DropdownOption[] = [{ key: 'history', label: t('cron.action.history') }]
  if (canManage.value) list.unshift({ key: 'run', label: t('cron.action.run'), disabled: task.running })
  if (changeable(task)) {
    list.push(
      { key: 'edit', label: t('common.action.edit') },
      { type: 'divider', key: 'd' },
      { key: 'delete', label: t('common.action.delete') },
    )
  }
  return list
}

function onAction(key: string, task: CronTask) {
  if (key === 'run') void run(() => runTask(task.id))
  else if (key === 'history') history.value = task
  else if (key === 'edit') openTask(task)
  else if (key === 'delete') void remove(task)
}

function target(task: CronTask): string {
  if (task.type === 'shell') return t('cron.target.shell', { user: task.user ?? '' })
  if (task.type === 'url') return task.url ?? ''
  return t('cron.target.backup', { plan: planNames.value[task.plan ?? ''] ?? task.plan ?? '' })
}

function stateTag(task: CronTask) {
  if (task.state === 'applying') return h(NTag, { size: 'small', type: 'info', bordered: false }, () => t('cron.state.applying'))
  if (task.state === 'failed') {
    const reason = task.errorCode ? (errorText(task.errorCode) ?? task.errorCode) : ''
    return h(NTag, { size: 'small', type: 'error', bordered: false, title: reason }, () => t('cron.state.failed'))
  }
  if (!task.enabled) return h(NTag, { size: 'small', bordered: false }, () => t('cron.state.disabled'))
  return h(NTag, { size: 'small', type: 'success', bordered: false }, () => t('cron.state.active'))
}

const columns = computed<DataTableColumns<CronTask>>(() => [
  {
    key: 'name',
    title: t('common.field.name'),
    minWidth: 200,
    render: (task) =>
      h('div', { class: 'cell' }, [
        h('span', { class: 'cell__name' }, task.name),
        h(NText, { depth: 3, class: 'cell__sub', dir: 'ltr' }, () => target(task)),
      ]),
  },
  {
    key: 'type',
    title: t('common.field.type'),
    width: 110,
    render: (task) => h(NTag, { size: 'small', bordered: false }, () => t(`cron.type.${task.type}`)),
  },
  {
    key: 'schedule',
    title: t('common.field.schedule'),
    minWidth: 180,
    render: (task) =>
      h('div', { class: 'cell' }, [
        h('span', null, describeSchedule(task.schedule)),
        h(NText, { depth: 3, class: 'cell__sub mono', dir: 'ltr' }, () => task.schedule),
      ]),
  },
  { key: 'state', title: t('cron.column.state'), width: 110, render: stateTag },
  {
    key: 'lastRun',
    title: t('cron.column.lastRun'),
    width: 190,
    render: (task) => {
      if (task.running) return h(NTag, { size: 'small', type: 'info', bordered: false }, () => t('cron.result.running'))
      if (!task.lastRun) return '-'
      return h('div', { class: 'cell' }, [
        h('span', null, formatDateTime(task.lastRun)),
        task.result
          ? h(NTag, { size: 'tiny', type: resultType(task.result), bordered: false, class: 'cell__tag' }, () => t(`cron.result.${task.result}`))
          : null,
      ])
    },
  },
  {
    key: 'nextRun',
    title: t('cron.column.nextRun'),
    width: 170,
    render: (task) => (task.enabled && task.nextRun ? formatDateTime(task.nextRun) : '-'),
  },
  {
    key: 'actions',
    title: '',
    width: 56,
    render: (task) =>
      h(
        NDropdown,
        { trigger: 'click', options: options(task), onSelect: (key: string | number) => onAction(String(key), task) },
        () =>
          h(NButton, { size: 'small', quaternary: true, 'aria-label': t('cron.action.more', { name: task.name }) }, {
            icon: () => h(NIcon, null, () => h(EllipsisHorizontal)),
          }),
      ),
  },
])
</script>

<template>
  <div class="cron">
    <div class="cron__header">
      <NH2 class="cron__title">{{ t('cron.title') }}</NH2>
      <NSpace v-if="canManage" :size="8">
        <NButton type="primary" :disabled="!list?.systemd" @click="openTask(null)">
          <template #icon>
            <NIcon><AddOutline /></NIcon>
          </template>
          {{ t('cron.task.createTitle') }}
        </NButton>
      </NSpace>
    </div>
    <NText depth="3" tag="p" class="intro">{{ t('cron.intro') }}</NText>

    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NAlert v-if="list && !list.systemd" type="warning" class="block">{{ t('errors.cron.systemd_required') }}</NAlert>
    <NAlert v-if="list && list.systemd && !list.statusAvailable" type="info" class="block">{{ t('cron.statusUnavailable') }}</NAlert>

    <div v-if="jobId" class="job">
      <JobProgress :job-id="jobId" @finished="finished" />
      <NButton size="small" quaternary :disabled="busy" @click="dismiss">{{ t('sites.create.close') }}</NButton>
    </div>

    <NSpin v-if="!list && !loadError" class="wait" />
    <template v-else-if="list">
      <NEmpty v-if="list.tasks.length === 0" :description="t('cron.empty.title')" class="empty">
        <template #extra>
          <NText depth="3" tag="p" class="empty__text">{{ t('cron.empty.text') }}</NText>
        </template>
      </NEmpty>
      <NDataTable
        v-else
        :columns="columns"
        :data="list.tasks"
        :row-key="(task: CronTask) => task.id"
        :loading="loading"
        :scroll-x="980"
        size="small"
      />
    </template>

    <TaskDialog v-model:show="showDialog" :task="editing" @started="(job: Job) => { follow(job); void load() }" />
    <RunsDrawer :task="history" @close="history = null" />
  </div>
</template>

<style scoped>
.cron {
  max-inline-size: 1300px;
}

.cron__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 16px;
}

.cron__title {
  margin: 0;
}

.intro {
  margin-block: 4px 16px;
  font-size: 13px;
}

.block {
  margin-block-end: 12px;
}

.wait {
  display: flex;
  justify-content: center;
  padding-block: 64px;
}

.empty {
  padding-block: 48px;
}

.empty__text {
  max-inline-size: 520px;
  margin: 0 auto;
  text-align: center;
}

.job {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 4px;
  margin-block-end: 12px;
}

.job > :first-child {
  align-self: stretch;
}

:deep(.cell) {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-inline-size: 0;
}

:deep(.cell__sub) {
  font-size: 12px;
  overflow-wrap: anywhere;
}

:deep(.cell__tag) {
  align-self: flex-start;
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
