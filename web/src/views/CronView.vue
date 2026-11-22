<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus } from '@lucide/vue'
import type { CronTask } from '@/api/cron'
import Notice from '@/components/common/Notice.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { Button } from '@/components/ui/button'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { useCronStore } from '@/stores/cron'
import { useSessionStore } from '@/stores/session'
import TaskDrawer from './cron/TaskDrawer.vue'
import TaskSheet from './cron/TaskSheet.vue'
import TaskTable from './cron/TaskTable.vue'

// Scheduled tasks ("list" skeleton): systemd timers that run a script,
// fetch a URL or run a backup plan. A task's details and runs open in a
// drawer kept in the address; creating and changing use a form sheet.
const { t } = useI18n()
const store = useCronStore()
const session = useSessionStore()
const drawer = useDrawerRoute('task')

const canManage = computed(() => session.can('cron.manage'))
const list = computed(() => store.list)
const canCreate = computed(() => canManage.value && list.value?.systemd === true)

const showSheet = ref(false)
/** The sheet loads users and plans; it is mounted when first opened. */
const sheetUsed = ref(false)
watch(showSheet, (open) => {
  if (open) sheetUsed.value = true
})
const editing = ref<CronTask | null>(null)

onMounted(() => {
  store.watch()
  void store.load()
})
onBeforeUnmount(() => store.unwatch())

const status = computed(() => {
  const l = list.value
  if (!l) return ''
  const total = l.tasks.length
  const enabled = l.tasks.filter((task) => task.enabled).length
  const failed = l.tasks.filter((task) => task.result === 'failed' || task.result === 'timeout').length
  const parts = [t('cron.status.count', { count: total }, total)]
  if (total > 0) parts.push(t('cron.status.enabled', { count: enabled }, enabled))
  if (failed > 0) parts.push(t('cron.status.failed', { count: failed }, failed))
  return parts.join(' · ')
})

function openCreate() {
  editing.value = null
  showSheet.value = true
}

function openEdit(task: CronTask) {
  editing.value = task
  showSheet.value = true
}
</script>

<template>
  <div>
    <PageHeader :title="t('cron.title')" :status="status" :status-loading="!list && !store.loadError">
      <template v-if="canManage" #actions>
        <Button class="max-md:h-11" :disabled="!canCreate" @click="openCreate">
          <Plus aria-hidden="true" />
          {{ t('cron.task.createTitle') }}
        </Button>
      </template>
    </PageHeader>

    <div class="flex flex-col gap-4">
      <template v-if="list">
        <Notice v-if="!list.systemd" tone="warning">{{ t('errors.cron.systemd_required') }}</Notice>
        <Notice v-else-if="!list.statusAvailable" tone="info">{{ t('cron.statusUnavailable') }}</Notice>
      </template>
      <TaskTable
        :tasks="store.tasks"
        :loading="store.loading && !list"
        :error="store.loadError"
        :can-manage="canManage"
        :can-create="canCreate"
        @open="(id: string, section?: string) => drawer.open(id, section)"
        @create="openCreate"
        @edit="openEdit"
        @retry="store.load()"
      />
    </div>

    <TaskDrawer @edit="openEdit" />
    <TaskSheet v-if="sheetUsed" v-model:open="showSheet" :task="editing" />
  </div>
</template>
