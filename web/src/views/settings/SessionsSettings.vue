<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NEllipsis,
  NTag,
  NText,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import {
  fetchSessions,
  revokeOtherSessions,
  revokeSession,
  type AccountSession,
} from '@/api/account'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const router = useRouter()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()
const session = useSessionStore()

const sessions = ref<AccountSession[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)
const busyId = ref<string | null>(null)
const revokingOthers = ref(false)

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

const hasOthers = computed(() => sessions.value.some((s) => !s.current))

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
    message.success(t('settings.sessions.revoked'))
  } catch (err) {
    showError(err)
  } finally {
    busyId.value = null
  }
}

async function revokeOthers() {
  const ok = await confirm({
    title: t('settings.sessions.confirmOthers.title'),
    content: t('settings.sessions.confirmOthers.content'),
    confirmText: t('settings.sessions.revokeOthers'),
    danger: true,
  })
  if (!ok) return
  revokingOthers.value = true
  try {
    const count = await revokeOtherSessions()
    message.success(t('settings.sessions.revokedOthers', { count }, count))
    await load()
  } catch (err) {
    showError(err)
  } finally {
    revokingOthers.value = false
  }
}

const columns = computed<DataTableColumns<AccountSession>>(() => [
  {
    key: 'userAgent',
    title: t('settings.sessions.column.device'),
    minWidth: 220,
    render: (row) =>
      h('div', { class: 'device' }, [
        h(
          NEllipsis,
          { style: 'max-width: 100%' },
          () => row.userAgent || t('settings.sessions.unknownDevice'),
        ),
        row.current
          ? h(NTag, { type: 'success', size: 'small', bordered: false }, () =>
              t('settings.sessions.current'),
            )
          : null,
      ]),
  },
  {
    key: 'remote',
    title: t('settings.sessions.column.remote'),
    width: 160,
    render: (row) => h('span', { dir: 'ltr' }, row.remote),
  },
  {
    key: 'createdAt',
    title: t('settings.sessions.column.created'),
    width: 180,
    render: (row) => formatDateTime(row.createdAt),
  },
  {
    key: 'lastSeenAt',
    title: t('settings.sessions.column.lastSeen'),
    width: 180,
    render: (row) => formatDateTime(row.lastSeenAt),
  },
  {
    key: 'actions',
    title: '',
    width: 120,
    render: (row) =>
      h(
        NButton,
        {
          size: 'small',
          type: row.current ? 'default' : 'error',
          ghost: !row.current,
          loading: busyId.value === row.id,
          disabled: busyId.value !== null && busyId.value !== row.id,
          onClick: () => void revoke(row),
        },
        () => t('settings.sessions.revoke'),
      ),
  },
])
</script>

<template>
  <NCard :title="t('settings.sessions.title')" size="small">
    <template #header-extra>
      <NButton
        size="small"
        :disabled="!hasOthers"
        :loading="revokingOthers"
        @click="revokeOthers"
      >
        {{ t('settings.sessions.revokeOthers') }}
      </NButton>
    </template>
    <NText depth="3" tag="p" class="intro">{{ t('settings.sessions.description') }}</NText>
    <NAlert v-if="loadError" type="error" class="intro">{{ errorMessage(loadError) }}</NAlert>
    <NDataTable
      :columns="columns"
      :data="sessions"
      :loading="loading"
      :row-key="(row: AccountSession) => row.id"
      :scroll-x="860"
      size="small"
    />
  </NCard>
</template>

<style scoped>
.intro {
  margin-block: 0 12px;
}

:deep(.device) {
  display: flex;
  align-items: center;
  gap: 8px;
  min-inline-size: 0;
}
</style>
