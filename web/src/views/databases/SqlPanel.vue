<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NButton,
  NCheckbox,
  NDataTable,
  NEmpty,
  NIcon,
  NModal,
  NSpace,
  NTabPane,
  NTabs,
  NTag,
  NText,
  type DataTableColumns,
} from 'naive-ui'
import { AddOutline } from '@vicons/ionicons5'
import { isStepUpRequired } from '@/api/client'
import {
  createDump,
  dropDatabase,
  dropDbUser,
  revealDbPassword,
  type DbDatabase,
  type DbEngine,
  type DbUser,
} from '@/api/databases'
import type { Job } from '@/api/jobs'
import JobProgress from '@/components/JobProgress.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { useJobRun } from '@/composables/useJobRun'
import { formatBytes, formatDateTime } from '@/i18n/format'
import DatabaseDialog from './DatabaseDialog.vue'
import DumpsPanel from './DumpsPanel.vue'
import PasswordDialog from './PasswordDialog.vue'
import UserDialog from './UserDialog.vue'

const props = defineProps<{ engine: DbEngine; canManage: boolean; version: number }>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const confirm = useConfirm()
const showError = useErrorToast()
const { jobId, run, follow, finished, dismiss } = useJobRun((_job: Job) => emit('changed'))

const tab = ref<'databases' | 'users' | 'dumps'>('databases')
const showCreateDb = ref(false)
const showUser = ref(false)
const editingUser = ref<DbUser | null>(null)
const dropping = ref<DbDatabase | null>(null)
const dropOwner = ref(false)
const revealed = ref<string | null>(null)
const revealedUser = ref('')

/** "Not created by the panel" and "missing on the server" marks. */
function marks(item: { managed: boolean; missing?: boolean }) {
  return [
    item.missing ? h(NTag, { size: 'small', type: 'error', bordered: false }, () => t('databases.missing')) : null,
    !item.managed ? h(NTag, { size: 'small', bordered: false }, () => t('databases.unmanaged')) : null,
  ]
}

function askDrop(db: DbDatabase) {
  dropping.value = db
  dropOwner.value = false
}

function confirmDrop() {
  const db = dropping.value
  if (!db) return
  const withOwner = dropOwner.value && !!db.owner
  dropping.value = null
  // Step-up protected: the client asks for the password and retries.
  void run(() => dropDatabase(props.engine.id, db.name, withOwner))
}

function dump(db: DbDatabase) {
  void run(() => createDump(props.engine.id, db.name))
}

function openUser(user: DbUser | null) {
  editingUser.value = user
  showUser.value = true
}

async function dropUser(user: DbUser) {
  const ok = await confirm({
    title: t('databases.user.dropTitle', { name: user.name }),
    content: t('databases.user.dropContent'),
    confirmText: t('common.action.delete'),
    danger: true,
  })
  if (ok) void run(() => dropDbUser(props.engine.id, user.name))
}

async function reveal(user: DbUser) {
  try {
    revealedUser.value = user.name
    revealed.value = await revealDbPassword(props.engine.id, user.name)
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  }
}

const dbColumns = computed<DataTableColumns<DbDatabase>>(() => [
  {
    key: 'name',
    title: t('common.field.name'),
    minWidth: 180,
    render: (db) => h(NSpace, { size: 6, align: 'center', wrap: false }, () => [h('span', { class: 'mono' }, db.name), ...marks(db)]),
  },
  { key: 'sizeBytes', title: t('databases.size'), width: 110, render: (db) => formatBytes(db.sizeBytes) },
  { key: 'owner', title: t('databases.owner'), width: 150, render: (db) => db.owner ?? '-' },
  { key: 'createdAt', title: t('sites.overview.created'), width: 170, render: (db) => (db.createdAt ? formatDateTime(db.createdAt) : '-') },
  {
    key: 'actions',
    title: '',
    width: 190,
    render: (db) =>
      props.canManage && !db.missing
        ? h(NSpace, { size: 4, wrap: false }, () => [
            h(NButton, { size: 'small', onClick: () => dump(db) }, () => t('databases.dumps.create')),
            h(NButton, { size: 'small', quaternary: true, type: 'error', onClick: () => askDrop(db) }, () =>
              t('common.action.delete'),
            ),
          ])
        : null,
  },
])

const userColumns = computed<DataTableColumns<DbUser>>(() => [
  {
    key: 'name',
    title: t('common.field.name'),
    minWidth: 160,
    render: (u) => h(NSpace, { size: 6, align: 'center', wrap: false }, () => [h('span', { class: 'mono' }, u.name), ...marks(u)]),
  },
  {
    key: 'hosts',
    title: t('common.field.hosts'),
    minWidth: 160,
    render: (u) =>
      h(NSpace, { size: 4 }, () =>
        (u.hosts.length ? u.hosts : ['localhost']).map((host) =>
          h(NTag, { size: 'small', bordered: false }, () => (host === '%' ? t('databases.anyHost') : host)),
        ),
      ),
  },
  {
    key: 'grants',
    title: t('common.field.grants'),
    minWidth: 200,
    render: (u) =>
      h(NSpace, { size: 4 }, () =>
        u.grants.map((g) =>
          h(NTag, { size: 'small', bordered: false, type: g.level === 'all' ? 'info' : 'default' }, () =>
            t('databases.grant.tag', { database: g.database, level: t(`databases.grant.${g.level}`) }),
          ),
        ),
      ),
  },
  {
    key: 'actions',
    title: '',
    width: 250,
    render: (u) =>
      props.canManage && !u.missing
        ? h(NSpace, { size: 4, wrap: false }, () => [
            u.hasPassword
              ? h(NButton, { size: 'small', onClick: () => void reveal(u) }, () => t('databases.password.show'))
              : null,
            h(NButton, { size: 'small', quaternary: true, onClick: () => openUser(u) }, () => t('common.action.edit')),
            h(NButton, { size: 'small', quaternary: true, type: 'error', onClick: () => void dropUser(u) }, () =>
              t('common.action.delete'),
            ),
          ])
        : null,
  },
])
</script>

<template>
  <div>
    <div v-if="jobId" class="job">
      <JobProgress :job-id="jobId" @finished="finished" />
      <NButton size="small" quaternary @click="dismiss">{{ t('sites.create.close') }}</NButton>
    </div>

    <NTabs v-model:value="tab" type="segment" size="small" class="tabs">
      <NTabPane name="databases" :tab="t('databases.tabs.databases')">
        <div v-if="canManage" class="toolbar">
          <NButton type="primary" :disabled="!engine.running" @click="showCreateDb = true">
            <template #icon>
              <NIcon><AddOutline /></NIcon>
            </template>
            {{ t('databases.db.createTitle') }}
          </NButton>
        </div>
        <NDataTable
          v-if="engine.databases.length > 0"
          :columns="dbColumns"
          :data="engine.databases"
          :row-key="(db: DbDatabase) => db.name"
          :scroll-x="800"
          size="small"
        />
        <NEmpty v-else :description="t('databases.db.empty')" class="empty" />
      </NTabPane>

      <NTabPane name="users" :tab="t('databases.tabs.users')">
        <div v-if="canManage" class="toolbar">
          <NButton type="primary" :disabled="!engine.running" @click="openUser(null)">
            <template #icon>
              <NIcon><AddOutline /></NIcon>
            </template>
            {{ t('databases.user.createTitle') }}
          </NButton>
        </div>
        <NText v-if="engine.kind === 'postgresql'" depth="3" tag="p" class="intro">{{ t('databases.hints.pgHosts') }}</NText>
        <NDataTable
          v-if="engine.users.length > 0"
          :columns="userColumns"
          :data="engine.users"
          :row-key="(u: DbUser) => u.name"
          :scroll-x="850"
          size="small"
        />
        <NEmpty v-else :description="t('databases.user.empty')" class="empty" />
      </NTabPane>

      <NTabPane name="dumps" :tab="t('databases.tabs.dumps')">
        <DumpsPanel :engine="engine" :can-manage="canManage" :version="version" @changed="emit('changed')" />
      </NTabPane>
    </NTabs>

    <DatabaseDialog v-model:show="showCreateDb" :engine="engine" @started="follow" />
    <UserDialog v-model:show="showUser" :engine="engine" :user="editingUser" @started="follow" />
    <PasswordDialog v-model:password="revealed" :title="t('databases.password.title', { name: revealedUser })" />

    <NModal
      :show="dropping !== null"
      preset="card"
      :title="dropping ? t('databases.db.dropTitle', { name: dropping.name }) : ''"
      class="drop-dialog"
      @update:show="(open: boolean) => !open && (dropping = null)"
    >
      <NText tag="p" class="intro">{{ t('databases.db.dropContent') }}</NText>
      <NCheckbox v-if="dropping?.owner" v-model:checked="dropOwner">
        {{ t('databases.db.dropOwner', { user: dropping.owner }) }}
      </NCheckbox>
      <div class="actions">
        <NButton @click="dropping = null">{{ t('common.action.cancel') }}</NButton>
        <NButton type="error" @click="confirmDrop">{{ t('common.action.delete') }}</NButton>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.tabs {
  margin-block-start: 4px;
}

.toolbar {
  margin-block: 12px;
}

.intro {
  margin-block: 0 12px;
  font-size: 13px;
}

.empty {
  padding-block: 24px;
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

.drop-dialog {
  inline-size: min(480px, calc(100vw - 32px));
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
}
</style>
