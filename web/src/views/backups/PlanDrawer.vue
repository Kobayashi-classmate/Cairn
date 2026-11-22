<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Archive, History, LayoutDashboard, Pencil, Play, Trash2 } from '@lucide/vue'
import type { BackupPlan } from '@/api/backups'
import EmptyState from '@/components/common/EmptyState.vue'
import SettingsDrawer from '@/components/common/SettingsDrawer.vue'
import type { DrawerItem } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { useBackupsStore } from '@/stores/backups'
import { useSessionStore } from '@/stores/session'
import { askDeletePlan, canHandlePlan, planRunning, runPlanNow, scheduleText, targetsSummary } from './actions'
import PlanFilesPane from './drawer/PlanFilesPane.vue'
import PlanOverviewPane from './drawer/PlanOverviewPane.vue'
import PlanRunsPane from './drawer/PlanRunsPane.vue'

// A backup plan's drawer: ?open=plan:<id>&section=overview|runs|files.
// Run now and Edit sit in the header; Delete is apart at the bottom.
type Section = 'overview' | 'runs' | 'files'

const emit = defineEmits<{ edit: [plan: BackupPlan]; schedule: [plan: BackupPlan] }>()

const { t } = useI18n()
const store = useBackupsStore()
const session = useSessionStore()
const drawer = useDrawerRoute('plan')

const canManage = computed(() => session.can('backups.manage'))
const plan = computed(() => (drawer.id.value ? store.plan(drawer.id.value) : null))

const items = computed<DrawerItem<Section>[]>(() => [
  { value: 'overview', label: t('backups.drawer.overview'), icon: LayoutDashboard },
  { value: 'runs', label: t('backups.action.runs'), icon: History },
  { value: 'files', label: t('backups.action.files'), icon: Archive },
])
const dangerItems = computed(() =>
  canManage.value && plan.value && canHandlePlan(plan.value) ? [{ value: 'delete', label: t('backups.plan.delete'), icon: Trash2 }] : [],
)
const section = computed<Section>(() => items.value.find((item) => item.value === drawer.section.value)?.value ?? 'overview')

const status = computed(() => {
  const p = plan.value
  if (!p) return null
  if (!p.lastRun) return { tone: 'warning' as const, label: t('backups.plan.never') }
  return p.lastRun.ok
    ? { tone: 'success' as const, label: t('backups.runs.ok') }
    : { tone: 'destructive' as const, label: t('backups.runs.failed') }
})

const meta = computed(() => {
  const p = plan.value
  if (!p) return ''
  return [targetsSummary(p), store.storageName(p.storageId) ?? t('backups.plan.localOnly'), scheduleText(store.tasksOf(p))].join(' · ')
})

const loading = computed(() => drawer.id.value !== null && !store.plans && !store.loadError)
const error = computed(() => (store.plans ? null : store.loadError))

function run() {
  if (plan.value) void runPlanNow(plan.value)
}

function edit() {
  if (plan.value) emit('edit', plan.value)
}

function schedule() {
  if (plan.value) emit('schedule', plan.value)
}

async function onDanger() {
  if (plan.value && (await askDeletePlan(plan.value))) drawer.close()
}
</script>

<template>
  <SettingsDrawer
    :open="drawer.id.value !== null"
    :title="plan?.name ?? drawer.id.value ?? ''"
    :icon="Archive"
    :status="status"
    :meta="meta"
    :items="items"
    :danger-items="plan ? dangerItems : []"
    :section="section"
    :loading="loading"
    :error="error"
    @close="drawer.close()"
    @update:section="drawer.setSection"
    @danger="onDanger"
    @retry="store.load()"
  >
    <template v-if="plan && canManage" #actions>
      <Button v-if="canHandlePlan(plan)" variant="outline" size="sm" class="max-md:h-11" @click="edit">
        <Pencil aria-hidden="true" />
        {{ t('common.action.edit') }}
      </Button>
      <Button size="sm" class="max-md:h-11" :disabled="!plan || planRunning(plan.id)" @click="run">
        <Play aria-hidden="true" />
        {{ t('backups.action.run') }}
      </Button>
    </template>

    <template v-if="plan">
      <PlanOverviewPane v-if="section === 'overview'" :plan="plan" @go="drawer.setSection" @schedule="schedule" />
      <PlanRunsPane v-else-if="section === 'runs'" :key="plan.id" :plan="plan" />
      <PlanFilesPane v-else :key="plan.id" :plan="plan" :can-manage="canManage" />
    </template>
    <EmptyState v-else-if="store.plans" :title="t('backups.drawer.unknown')" :text="t('backups.drawer.unknownText')" />
  </SettingsDrawer>
</template>
