<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import type { DbEngine, DbUser } from '@/api/databases'
import { createCluster } from '@/api/databases'
import Notice from '@/components/common/Notice.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import { engineLabel } from '@/databases/labels'
import { errorText } from '@/i18n/errors'
import { useDatabasesStore } from '@/stores/databases'
import DatabaseSheet from './DatabaseSheet.vue'
import DatabaseTable from './DatabaseTable.vue'
import DumpTable from './DumpTable.vue'
import RedisKeys from './RedisKeys.vue'
import RedisSettings from './RedisSettings.vue'
import RestoreSheet from './RestoreSheet.vue'
import UserSheet from './UserSheet.vue'
import UserTable from './UserTable.vue'

// The selected server below the cards: its databases, users and dumps
// (MySQL/MariaDB, PostgreSQL), or settings, keys and snapshots (Redis). The
// sub-tab is kept in the address as ?view=.
const VIEW_QUERY = 'view'
const SQL_VIEWS = ['databases', 'users', 'dumps'] as const
const REDIS_VIEWS = ['settings', 'keys', 'snapshots'] as const
type View = (typeof SQL_VIEWS)[number] | (typeof REDIS_VIEWS)[number]

const props = defineProps<{ engine: DbEngine; canManage: boolean }>()

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const store = useDatabasesStore()

const isRedis = computed(() => props.engine.kind === 'redis')
const views = computed<readonly View[]>(() => (isRedis.value ? REDIS_VIEWS : SQL_VIEWS))

const view = computed<View>(() => {
  const value = route.query[VIEW_QUERY]
  return typeof value === 'string' && (views.value as readonly string[]).includes(value) ? (value as View) : views.value[0]!
})

function setView(value: View) {
  if (value === view.value) return
  const query = { ...route.query }
  if (value === views.value[0]) delete query[VIEW_QUERY]
  else query[VIEW_QUERY] = value
  void router.replace({ query })
}

const viewOptions = computed(() =>
  views.value.map((value) => {
    const count = value === 'databases' ? props.engine.databases.length : value === 'users' ? props.engine.users.length : undefined
    const label = t(`databases.tabs.${value}`)
    return { value, label: count === undefined ? label : t('databases.tabs.withCount', { label, count }) }
  }),
)

const engineError = computed(() => {
  const error = props.engine.error
  return error ? (errorText(error.code, error.params ?? {}) ?? error.code) : ''
})
const engineErrorDetail = computed(() => {
  const detail = props.engine.error?.params?.detail
  return typeof detail === 'string' ? detail : ''
})

const creatingCluster = ref(false)
async function cluster() {
  creatingCluster.value = true
  await store.runJob(() => createCluster(props.engine.id))
  creatingCluster.value = false
}

const showCreate = ref(false)
const showUser = ref(false)
const editingUser = ref<DbUser | null>(null)
const showRestore = ref(false)
const restoreDatabase = ref<string | undefined>(undefined)
const restoreFile = ref<string | undefined>(undefined)

function openUser(user: DbUser | null) {
  editingUser.value = user
  showUser.value = true
}

function openRestore(target: { database?: string; file?: string }) {
  restoreDatabase.value = target.database
  restoreFile.value = target.file
  showRestore.value = true
}
</script>

<template>
  <section class="flex flex-col gap-4" :aria-label="engineLabel(engine)">
    <Notice v-if="engineError" tone="destructive">
      {{ engineError }}
      <pre v-if="engineErrorDetail" class="mt-1.5 mb-0 font-mono text-xs font-normal whitespace-pre-wrap [overflow-wrap:anywhere]" dir="ltr">{{ engineErrorDetail }}</pre>
    </Notice>
    <Notice v-if="engine.noCluster" tone="warning">
      {{ t('databases.noCluster') }}
      <template v-if="canManage" #action>
        <button type="button" class="underline underline-offset-2 hover:no-underline disabled:opacity-50" :disabled="creatingCluster" @click="cluster">
          {{ t('databases.createCluster') }}
        </button>
      </template>
    </Notice>
    <Notice v-if="engine.remote" tone="info">{{ t('databases.remote.firewallNote', { port: engine.port ?? '' }) }}</Notice>

    <SegmentedControl
      :model-value="view"
      :options="viewOptions"
      :label="t('databases.tabs.label', { name: engineLabel(engine) })"
      class="self-start max-sm:w-full"
      @update:model-value="setView"
    />

    <template v-if="isRedis">
      <RedisSettings v-if="view === 'settings'" :engine="engine" :can-manage="canManage" />
      <RedisKeys v-else-if="view === 'keys'" :can-manage="canManage" />
      <DumpTable v-else :key="engine.id" :engine="engine" :can-manage="canManage" />
    </template>
    <template v-else>
      <DatabaseTable
        v-if="view === 'databases'"
        :engine="engine"
        :can-manage="canManage"
        :backups-known="store.status?.backups === true"
        @create="showCreate = true"
        @restore="(database: string) => openRestore({ database })"
      />
      <UserTable v-else-if="view === 'users'" :engine="engine" :can-manage="canManage" @create="openUser(null)" @edit="openUser" />
      <DumpTable v-else :key="engine.id" :engine="engine" :can-manage="canManage" @restore="(file: string) => openRestore({ file })" />
    </template>

    <template v-if="!isRedis && canManage">
      <DatabaseSheet v-model:open="showCreate" :engine="engine" />
      <UserSheet v-model:open="showUser" :engine="engine" :user="editingUser" />
      <RestoreSheet v-model:open="showRestore" :engine="engine" :database="restoreDatabase" :file="restoreFile" />
    </template>
  </section>
</template>
