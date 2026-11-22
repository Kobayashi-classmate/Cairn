<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus } from '@lucide/vue'
import type { BackupPlan, StorageView } from '@/api/backups'
import type { TaskRequest } from '@/api/cron'
import PageHeader from '@/components/common/PageHeader.vue'
import type { PageTab } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { usePageTab } from '@/composables/usePageTab'
import { useBackupsStore } from '@/stores/backups'
import { useSessionStore } from '@/stores/session'
import PlanDrawer from './backups/PlanDrawer.vue'
import PlanSheet from './backups/PlanSheet.vue'
import PlanTable from './backups/PlanTable.vue'
import StorageSheet from './backups/StorageSheet.vue'
import StorageTable from './backups/StorageTable.vue'
import TaskSheet from './cron/TaskSheet.vue'

// Backups ("list" skeleton): plans and the S3-compatible storage they copy
// to, in two tabs. A plan's details, runs and backup files open in a drawer
// kept in the address; plans, storage and schedules are edited in form
// sheets.
type Tab = 'plans' | 'storage'
const TABS: readonly Tab[] = ['plans', 'storage']

const { t } = useI18n()
const store = useBackupsStore()
const session = useSessionStore()
const drawer = useDrawerRoute('plan')
const { tab, tabLocation } = usePageTab(TABS, 'plans')

const canManage = computed(() => session.can('backups.manage'))
const canSchedule = computed(() => session.can('cron.manage'))

onMounted(() => void store.load())

const tabs = computed<PageTab<Tab>[]>(() => [
  { value: 'plans', label: t('backups.tabs.plans'), count: store.plans?.length },
  { value: 'storage', label: t('backups.tabs.storage'), count: store.storages?.length },
])

const status = computed(() => {
  const plans = store.plans
  if (!plans) return ''
  const parts = [t('backups.status.plans', { count: plans.length }, plans.length)]
  const failed = plans.filter((plan) => plan.lastRun && !plan.lastRun.ok).length
  const never = plans.filter((plan) => !plan.lastRun).length
  if (failed > 0) parts.push(t('backups.status.failed', { count: failed }, failed))
  if (never > 0) parts.push(t('backups.status.never', { count: never }, never))
  if (store.storages) parts.push(t('backups.status.storages', { count: store.storages.length }, store.storages.length))
  return parts.join(' · ')
})

const showPlan = ref(false)
const editingPlan = ref<BackupPlan | null>(null)
const showStorage = ref(false)
const editingStorage = ref<StorageView | null>(null)
const showTask = ref(false)
const taskUsed = ref(false)
watch(showTask, (open) => {
  if (open) taskUsed.value = true
})
const taskInitial = ref<Partial<TaskRequest>>({})

function openPlan(plan: BackupPlan | null) {
  editingPlan.value = plan
  showPlan.value = true
}

function openStorage(storage: StorageView | null) {
  editingStorage.value = storage
  showStorage.value = true
}

function schedule(plan: BackupPlan) {
  taskInitial.value = { type: 'backup', plan: plan.id, name: t('backups.plan.taskName', { name: plan.name }), schedule: '*-*-* 03:00:00' }
  showTask.value = true
}

/** A new plan opens in its drawer, ready to run or schedule. */
function onPlanSaved(plan: BackupPlan) {
  if (editingPlan.value) return
  // The list reloads meanwhile; the drawer needs the plan right away.
  if (store.plans && !store.plans.some((p) => p.id === plan.id)) store.plans = [...store.plans, plan]
  drawer.open(plan.id)
}
</script>

<template>
  <div>
    <PageHeader
      :title="t('backups.title')"
      :status="status"
      :status-loading="!store.plans && !store.loadError"
      :tabs="tabs"
      :tab="tab"
      :tab-location="tabLocation"
    >
      <template v-if="canManage" #actions>
        <Button v-if="tab === 'storage'" class="max-md:h-11" @click="openStorage(null)">
          <Plus aria-hidden="true" />
          {{ t('backups.storage.createTitle') }}
        </Button>
        <Button v-else class="max-md:h-11" @click="openPlan(null)">
          <Plus aria-hidden="true" />
          {{ t('backups.plan.createTitle') }}
        </Button>
      </template>
    </PageHeader>

    <div class="flex flex-col gap-4">
      <PlanTable
        v-if="tab === 'plans'"
        :plans="store.plans ?? []"
        :loading="store.loading && !store.plans"
        :error="store.loadError"
        :can-manage="canManage"
        :can-schedule="canSchedule"
        @open="(id: string, section?: string) => drawer.open(id, section)"
        @create="openPlan(null)"
        @edit="openPlan"
        @schedule="schedule"
        @retry="store.load()"
      />
      <StorageTable
        v-else
        :storages="store.storages ?? []"
        :loading="store.loading && !store.storages"
        :error="store.loadError"
        :can-manage="canManage"
        @create="openStorage(null)"
        @edit="openStorage"
        @retry="store.load()"
      />
    </div>

    <PlanDrawer @edit="openPlan" @schedule="schedule" />
    <template v-if="canManage">
      <PlanSheet v-model:open="showPlan" :plan="editingPlan" @saved="onPlanSaved" />
      <StorageSheet v-model:open="showStorage" :storage="editingStorage" />
    </template>
    <TaskSheet v-if="canSchedule && taskUsed" v-model:open="showTask" :task="null" :initial="taskInitial" @finished="store.load()" />
  </div>
</template>
