<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Backups: plans (what, where to, encryption, retention) and the
// S3-compatible storage they copy to. Plans run on demand or from a
// scheduled task.
import { computed, h, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NCheckbox,
  NDataTable,
  NDropdown,
  NEmpty,
  NH2,
  NIcon,
  NModal,
  NSpace,
  NTabPane,
  NTabs,
  NTag,
  NText,
  useMessage,
  type DataTableColumns,
  type DropdownOption,
} from 'naive-ui'
import { AddOutline, EllipsisHorizontal } from '@vicons/ionicons5'
import { isStepUpRequired } from '@/api/client'
import {
  deletePlan,
  deleteStorage,
  fetchPlans,
  fetchStorages,
  runPlan,
  testStorage,
  type BackupPlan,
  type StorageView,
} from '@/api/backups'
import type { TaskRequest } from '@/api/cron'
import type { Job } from '@/api/jobs'
import { targetLabel } from '@/backups/targets'
import JobProgress from '@/components/JobProgress.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { useJobRun } from '@/composables/useJobRun'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'
import FilesDrawer from './backups/FilesDrawer.vue'
import PlanDialog from './backups/PlanDialog.vue'
import PlanRunsDrawer from './backups/PlanRunsDrawer.vue'
import StorageDialog from './backups/StorageDialog.vue'
import TaskDialog from './cron/TaskDialog.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()
const session = useSessionStore()

const canManage = computed(() => session.can('backups.manage'))
const canSchedule = computed(() => session.can('cron.manage'))

const tab = ref<string>(route.query.tab === 'storage' ? 'storage' : 'plans')
watch(tab, (value) => void router.replace({ query: value === 'plans' ? {} : { tab: value } }))

const plans = ref<BackupPlan[]>([])
const storages = ref<StorageView[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)

async function load() {
  loading.value = true
  try {
    const [planList, storageList] = await Promise.all([fetchPlans(), fetchStorages()])
    plans.value = planList
    storages.value = storageList
    loadError.value = null
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

onMounted(load)

const { jobId, busy, run, follow, finished, dismiss } = useJobRun((_job: Job) => void load())

// Plans ---------------------------------------------------------------------------

const showPlan = ref(false)
const editingPlan = ref<BackupPlan | null>(null)
const filesOf = ref<BackupPlan | null>(null)
const runsOf = ref<BackupPlan | null>(null)
const removing = ref<BackupPlan | null>(null)
const removeFiles = ref(false)
const showTask = ref(false)
const taskInitial = ref<Partial<TaskRequest>>({})

function openPlan(plan: BackupPlan | null) {
  editingPlan.value = plan
  showPlan.value = true
}

function schedule(plan: BackupPlan) {
  taskInitial.value = { type: 'backup', plan: plan.id, name: t('backups.plan.taskName', { name: plan.name }), schedule: '*-*-* 03:00:00' }
  showTask.value = true
}

async function confirmRemovePlan() {
  const plan = removing.value
  if (!plan) return
  const files = removeFiles.value
  removing.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    await deletePlan(plan.id, files)
    message.success(t('backups.plan.deleted', { name: plan.name }))
    void load()
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  }
}

function storageName(id?: string): string {
  if (!id) return t('backups.plan.localOnly')
  return storages.value.find((storage) => storage.id === id)?.name ?? id
}

function planOptions(): DropdownOption[] {
  const list: DropdownOption[] = [
    { key: 'files', label: t('backups.action.files') },
    { key: 'runs', label: t('backups.action.runs') },
  ]
  if (canManage.value) list.unshift({ key: 'run', label: t('backups.action.run') })
  if (canSchedule.value) list.push({ key: 'schedule', label: t('backups.action.schedule') })
  if (canManage.value) {
    list.push({ key: 'edit', label: t('common.action.edit') }, { type: 'divider', key: 'd' }, { key: 'delete', label: t('common.action.delete') })
  }
  return list
}

function onPlanAction(key: string, plan: BackupPlan) {
  switch (key) {
    case 'run':
      void run(() => runPlan(plan.id))
      break
    case 'files':
      filesOf.value = plan
      break
    case 'runs':
      runsOf.value = plan
      break
    case 'schedule':
      schedule(plan)
      break
    case 'edit':
      openPlan(plan)
      break
    case 'delete':
      removeFiles.value = false
      removing.value = plan
      break
  }
}

function moreButton(label: string) {
  return h(NButton, { size: 'small', quaternary: true, 'aria-label': label }, { icon: () => h(NIcon, null, () => h(EllipsisHorizontal)) })
}

const planColumns = computed<DataTableColumns<BackupPlan>>(() => [
  {
    key: 'name',
    title: t('common.field.name'),
    minWidth: 220,
    render: (plan) =>
      h('div', { class: 'cell' }, [
        h('span', null, plan.name),
        h(NText, { depth: 3, class: 'cell__sub' }, () =>
          plan.targets.length <= 2
            ? plan.targets.map(targetLabel).join(', ')
            : t('backups.plan.targetsMore', { first: plan.targets.slice(0, 2).map(targetLabel).join(', '), count: plan.targets.length - 2 }),
        ),
      ]),
  },
  {
    key: 'storage',
    title: t('backups.plan.storage'),
    minWidth: 160,
    render: (plan) =>
      h('div', { class: 'cell' }, [
        h('span', null, storageName(plan.storageId)),
        plan.storageId && plan.keepLocal ? h(NText, { depth: 3, class: 'cell__sub' }, () => t('backups.plan.alsoLocal')) : null,
      ]),
  },
  {
    key: 'encryption',
    title: t('backups.plan.encryption'),
    width: 130,
    render: (plan) =>
      plan.recipients.length > 0
        ? h(NTag, { size: 'small', type: 'success', bordered: false }, () => t('backups.plan.encrypted'))
        : h(NTag, { size: 'small', type: plan.storageId ? 'warning' : 'default', bordered: false }, () => t('backups.plan.notEncrypted')),
  },
  {
    key: 'retention',
    title: t('backups.plan.retention'),
    width: 150,
    render: (plan) => h('span', { class: 'mono' }, t('backups.retention.short', { ...plan.retention })),
  },
  {
    key: 'schedule',
    title: t('backups.plan.scheduled'),
    width: 120,
    render: (plan) =>
      plan.tasks.length > 0
        ? h(NTag, { size: 'small', type: 'info', bordered: false }, () => t('backups.plan.tasks', { count: plan.tasks.length }, plan.tasks.length))
        : h(NText, { depth: 3 }, () => t('backups.plan.manualOnly')),
  },
  {
    key: 'lastRun',
    title: t('backups.plan.lastRun'),
    width: 200,
    render: (plan) => {
      const last = plan.lastRun
      if (!last) return '-'
      const reason = last.error ? (errorText(last.error.code, last.error.params ?? {}) ?? last.error.code) : ''
      return h('div', { class: 'cell' }, [
        h('span', null, formatDateTime(last.started)),
        h(
          NTag,
          { size: 'tiny', type: last.ok ? 'success' : 'error', bordered: false, class: 'cell__tag', title: reason },
          () => (last.ok ? t('backups.runs.ok') : t('backups.runs.failed')),
        ),
      ])
    },
  },
  {
    key: 'actions',
    title: '',
    width: 56,
    render: (plan) =>
      h(
        NDropdown,
        { trigger: 'click', options: planOptions(), onSelect: (key: string | number) => onPlanAction(String(key), plan) },
        () => moreButton(t('backups.action.more', { name: plan.name })),
      ),
  },
])

// Storage -------------------------------------------------------------------------

const showStorage = ref(false)
const editingStorage = ref<StorageView | null>(null)
const testing = ref<string | null>(null)

function openStorage(storage: StorageView | null) {
  editingStorage.value = storage
  showStorage.value = true
}

async function test(storage: StorageView) {
  testing.value = storage.id
  try {
    await testStorage(storage.id)
    message.success(t('backups.storage.testOk'))
  } catch (err) {
    showError(err)
  } finally {
    testing.value = null
  }
}

async function removeStorage(storage: StorageView) {
  const ok = await confirm({
    title: t('backups.storage.deleteTitle', { name: storage.name }),
    content: t('backups.storage.deleteText'),
    confirmText: t('common.action.delete'),
    danger: true,
  })
  if (!ok) return
  try {
    await deleteStorage(storage.id)
    void load()
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  }
}

const storageColumns = computed<DataTableColumns<StorageView>>(() => [
  { key: 'name', title: t('common.field.name'), minWidth: 160 },
  {
    key: 'bucket',
    title: t('common.field.bucket'),
    minWidth: 260,
    render: (storage) =>
      h('div', { class: 'cell' }, [
        h('span', { class: 'mono', dir: 'ltr' }, storage.prefix ? `${storage.bucket}/${storage.prefix}` : storage.bucket),
        h(NText, { depth: 3, class: 'cell__sub mono', dir: 'ltr' }, () => `${storage.insecure ? 'http' : 'https'}://${storage.endpoint}`),
      ]),
  },
  { key: 'region', title: t('common.field.region'), width: 140, render: (storage) => storage.region || '-' },
  {
    key: 'flags',
    title: '',
    width: 150,
    render: (storage) =>
      h(NSpace, { size: 4 }, () => [
        storage.pathStyle ? h(NTag, { size: 'small', bordered: false }, () => t('backups.storage.pathStyleTag')) : null,
        storage.insecure ? h(NTag, { size: 'small', type: 'warning', bordered: false }, () => t('backups.storage.insecureTag')) : null,
      ]),
  },
  {
    key: 'actions',
    title: '',
    width: 220,
    render: (storage) =>
      canManage.value
        ? h(NSpace, { size: 4, wrap: false }, () => [
            h(NButton, { size: 'small', loading: testing.value === storage.id, onClick: () => void test(storage) }, () => t('backups.storage.test')),
            h(NButton, { size: 'small', quaternary: true, onClick: () => openStorage(storage) }, () => t('common.action.edit')),
            h(NButton, { size: 'small', quaternary: true, type: 'error', onClick: () => void removeStorage(storage) }, () =>
              t('common.action.delete'),
            ),
          ])
        : null,
  },
])
</script>

<template>
  <div class="backups">
    <NH2 class="backups__title">{{ t('backups.title') }}</NH2>
    <NText depth="3" tag="p" class="intro">{{ t('backups.intro') }}</NText>

    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <div v-if="jobId" class="job">
      <JobProgress :job-id="jobId" @finished="finished" />
      <NButton size="small" quaternary :disabled="busy" @click="dismiss">{{ t('sites.create.close') }}</NButton>
    </div>

    <NTabs v-model:value="tab" type="line">
      <NTabPane name="plans" :tab="t('backups.tabs.plans')">
        <div v-if="canManage" class="toolbar">
          <NButton type="primary" @click="openPlan(null)">
            <template #icon>
              <NIcon><AddOutline /></NIcon>
            </template>
            {{ t('backups.plan.createTitle') }}
          </NButton>
        </div>
        <NEmpty v-if="!loading && plans.length === 0" :description="t('backups.plan.empty')" class="empty">
          <template #extra>
            <NText depth="3" tag="p" class="empty__text">{{ t('backups.plan.emptyText') }}</NText>
          </template>
        </NEmpty>
        <NDataTable
          v-else
          :columns="planColumns"
          :data="plans"
          :row-key="(plan: BackupPlan) => plan.id"
          :loading="loading"
          :scroll-x="1100"
          size="small"
        />
      </NTabPane>

      <NTabPane name="storage" :tab="t('backups.tabs.storage')">
        <NText depth="3" tag="p" class="intro">{{ t('backups.storage.intro') }}</NText>
        <div v-if="canManage" class="toolbar">
          <NButton type="primary" @click="openStorage(null)">
            <template #icon>
              <NIcon><AddOutline /></NIcon>
            </template>
            {{ t('backups.storage.createTitle') }}
          </NButton>
        </div>
        <NEmpty v-if="!loading && storages.length === 0" :description="t('backups.storage.empty')" class="empty" />
        <NDataTable
          v-else
          :columns="storageColumns"
          :data="storages"
          :row-key="(storage: StorageView) => storage.id"
          :loading="loading"
          :scroll-x="900"
          size="small"
        />
      </NTabPane>
    </NTabs>

    <PlanDialog v-model:show="showPlan" :plan="editingPlan" :storages="storages" @saved="load" />
    <StorageDialog v-model:show="showStorage" :storage="editingStorage" @saved="load" />
    <TaskDialog v-model:show="showTask" :task="null" :initial="taskInitial" @started="(job: Job) => follow(job)" />
    <FilesDrawer :plan="filesOf" @close="filesOf = null" />
    <PlanRunsDrawer :plan="runsOf" @close="runsOf = null" />

    <NModal
      :show="removing !== null"
      preset="card"
      :title="removing ? t('backups.plan.deleteTitle', { name: removing.name }) : ''"
      class="delete-dialog"
      @update:show="(open: boolean) => !open && (removing = null)"
    >
      <NText tag="p" class="intro">{{ t('backups.plan.deleteText') }}</NText>
      <NAlert v-if="removing && removing.tasks.length > 0" type="warning" class="block">{{ t('errors.backup.plan_in_use') }}</NAlert>
      <NCheckbox v-model:checked="removeFiles">{{ t('backups.plan.deleteFiles') }}</NCheckbox>
      <div class="actions">
        <NButton @click="removing = null">{{ t('common.action.cancel') }}</NButton>
        <NButton type="error" :disabled="!!removing && removing.tasks.length > 0" @click="confirmRemovePlan">
          {{ t('common.action.delete') }}
        </NButton>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.backups {
  max-inline-size: 1300px;
}

.backups__title {
  margin: 0;
}

.intro {
  margin-block: 4px 12px;
  font-size: 13px;
}

.block {
  margin-block-end: 12px;
}

.toolbar {
  margin-block: 12px;
}

.empty {
  padding-block: 40px;
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

.delete-dialog {
  inline-size: min(480px, calc(100vw - 32px));
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
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
  font-size: 12px;
}
</style>
