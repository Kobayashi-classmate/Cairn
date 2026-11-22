<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { LogOut } from '@lucide/vue'
import { fetchSessions, revokeOtherSessions, revokeSession, type AccountSession } from '@/api/account'
import Card from '@/components/common/Card.vue'
import DataTable from '@/components/common/DataTable.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import { confirmAction } from '@/composables/useConfirmDialog'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useSessionStore } from '@/stores/session'

// The browsers signed in to your account: sign out one, or all but this one.
const { t } = useI18n()
const router = useRouter()
const session = useSessionStore()

const sessions = ref<AccountSession[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)
const busyId = ref<string | null>(null)

async function load() {
  loading.value = true
  try {
    sessions.value = await fetchSessions()
    loadError.value = null
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

onMounted(load)

const others = computed(() => sessions.value.filter((s) => !s.current).length)

const columns = computed<ColumnDef<AccountSession>[]>(() => [
  { id: 'device', header: t('settings.sessions.column.device'), accessorFn: (s) => s.userAgent, meta: { class: 'min-w-[240px]' } },
  { id: 'remote', header: t('settings.sessions.column.remote'), accessorFn: (s) => s.remote, meta: { class: 'w-[170px]' } },
  { id: 'created', header: t('settings.sessions.column.created'), accessorFn: (s) => s.createdAt, meta: { class: 'w-[150px]' } },
  { id: 'lastSeen', header: t('settings.sessions.column.lastSeen'), accessorFn: (s) => s.lastSeenAt, meta: { class: 'w-[150px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[120px] text-end' } },
])

async function revoke(entry: AccountSession) {
  busyId.value = entry.id
  try {
    await revokeSession(entry.id)
    if (entry.current) {
      // This browser's session is gone: same as signing out.
      session.sessionEnded()
      await router.replace({ name: 'login' })
      return
    }
    sessions.value = sessions.value.filter((s) => s.id !== entry.id)
    notifySuccess(t('settings.sessions.revoked'))
  } catch (err) {
    notifyError(err)
  } finally {
    busyId.value = null
  }
}

function askRevokeOthers() {
  void confirmAction({
    title: t('settings.sessions.confirmOthers.title'),
    description: t('settings.sessions.confirmOthers.content'),
    confirmLabel: t('settings.sessions.revokeOthers'),
    destructive: true,
    run: async () => {
      const count = await revokeOtherSessions()
      notifySuccess(t('settings.sessions.revokedOthers', { count }, count))
      await load()
    },
  })
}
</script>

<template>
  <Card :title="t('settings.sessions.title')" :description="t('settings.sessions.description')" :level="3" flush>
    <template #actions>
      <Button variant="outline" size="sm" class="max-md:h-11" :disabled="others === 0" @click="askRevokeOthers">
        <LogOut aria-hidden="true" />
        {{ t('settings.sessions.revokeOthers') }}
      </Button>
    </template>
    <DataTable
      flat
      :data="sessions"
      :columns="columns"
      :row-key="(s) => s.id"
      :label="t('settings.sessions.title')"
      :loading="loading"
      :error="loadError"
      :min-width="760"
      :skeleton-rows="2"
      :initial-sort="[{ id: 'lastSeen', desc: true }]"
      @retry="load"
    >
      <template #cell-device="{ row }">
        <span class="flex min-w-0 flex-wrap items-center gap-2">
          <span class="max-w-[420px] truncate" :title="row.userAgent || undefined" dir="ltr">
            {{ row.userAgent || t('settings.sessions.unknownDevice') }}
          </span>
          <StatusPill v-if="row.current" tone="success">{{ t('settings.sessions.current') }}</StatusPill>
        </span>
      </template>
      <template #cell-remote="{ row }"><span class="font-mono text-[12.5px]" dir="ltr">{{ row.remote }}</span></template>
      <template #cell-created="{ row }"><RelativeTime :value="row.createdAt" /></template>
      <template #cell-lastSeen="{ row }"><RelativeTime :value="row.lastSeenAt" /></template>
      <template #cell-actions="{ row }">
        <Button
          variant="outline"
          size="sm"
          :class="row.current ? 'max-md:h-11' : 'text-destructive hover:text-destructive max-md:h-11'"
          :disabled="busyId !== null"
          @click="revoke(row)"
        >
          {{ t('settings.sessions.revoke') }}
        </Button>
      </template>
    </DataTable>
  </Card>
</template>
