<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Archive, Globe, History, LayoutDashboard, Pencil, Play, SquareTerminal, Timer, Trash2 } from '@lucide/vue'
import type { CronTask } from '@/api/cron'
import EmptyState from '@/components/common/EmptyState.vue'
import SettingsDrawer from '@/components/common/SettingsDrawer.vue'
import type { DrawerItem } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { describeSchedule } from '@/cron/schedule'
import { useCronStore } from '@/stores/cron'
import { useSessionStore } from '@/stores/session'
import { askDeleteTask, canChange, runTaskNow, taskTarget } from './actions'
import TaskOverviewPane from './drawer/TaskOverviewPane.vue'
import TaskRunsPane from './drawer/TaskRunsPane.vue'
import { taskState } from './labels'

// A scheduled task's drawer: ?open=task:<id>&section=overview|runs. Run now
// and Edit sit in the header; Delete is apart at the bottom.
type Section = 'overview' | 'runs'

const emit = defineEmits<{ edit: [task: CronTask] }>()

const { t } = useI18n()
const store = useCronStore()
const session = useSessionStore()
const drawer = useDrawerRoute('task')

const task = computed(() => (drawer.id.value ? store.task(drawer.id.value) : null))
const canManage = computed(() => session.can('cron.manage'))
const changeable = computed(() => (task.value ? canChange(task.value) : false))
const running = ref(false)

const items = computed<DrawerItem<Section>[]>(() => [
  { value: 'overview', label: t('cron.drawer.overview'), icon: LayoutDashboard },
  { value: 'runs', label: t('cron.action.history'), icon: History },
])
const dangerItems = computed(() => (changeable.value ? [{ value: 'delete', label: t('cron.task.delete'), icon: Trash2 }] : []))
const section = computed<Section>(() => items.value.find((item) => item.value === drawer.section.value)?.value ?? 'overview')

const TYPE_ICON = { shell: SquareTerminal, url: Globe, backup: Archive } as const

const status = computed(() => {
  const current = task.value
  if (!current) return null
  const state = taskState(current)
  return { tone: state.tone, label: state.text }
})

const meta = computed(() => {
  const current = task.value
  if (!current) return ''
  return [taskTarget(current, store.planNames), describeSchedule(current.schedule)].join(' · ')
})

const loading = computed(() => drawer.id.value !== null && !store.list && !store.loadError)
const error = computed(() => (store.list ? null : store.loadError))

async function run() {
  if (!task.value) return
  running.value = true
  await runTaskNow(task.value)
  running.value = false
}

function edit() {
  if (task.value) emit('edit', task.value)
}

async function onDanger() {
  if (task.value && (await askDeleteTask(task.value))) drawer.close()
}
</script>

<template>
  <SettingsDrawer
    :open="drawer.id.value !== null"
    :title="task?.name ?? drawer.id.value ?? ''"
    :icon="task ? TYPE_ICON[task.type] : Timer"
    :status="status"
    :meta="meta"
    :items="items"
    :danger-items="dangerItems"
    :section="section"
    :loading="loading"
    :error="error"
    @close="drawer.close()"
    @update:section="drawer.setSection"
    @danger="onDanger"
    @retry="store.load()"
  >
    <template v-if="task" #actions>
      <Button v-if="changeable" variant="outline" size="sm" class="max-md:h-11" @click="edit">
        <Pencil aria-hidden="true" />
        {{ t('common.action.edit') }}
      </Button>
      <Button v-if="canManage" size="sm" class="max-md:h-11" :disabled="running || task.running || task.state === 'applying'" @click="run">
        <Play aria-hidden="true" />
        {{ t('cron.action.run') }}
      </Button>
    </template>

    <template v-if="task">
      <TaskOverviewPane v-if="section === 'overview'" :task="task" @go="drawer.setSection" />
      <TaskRunsPane v-else :key="task.id" :task="task" />
    </template>
    <EmptyState v-else-if="store.list" :title="t('cron.drawer.unknown')" :text="t('cron.drawer.unknownText')" />
  </SettingsDrawer>
</template>
