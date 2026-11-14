<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { I18nT, useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NIcon,
  NSpace,
  NTag,
  NText,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { AddOutline } from '@vicons/ionicons5'
import { fetchTokens, revokeToken, type ApiToken } from '@/api/tokens'
import { permissionLabel } from '@/auth/permissions'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { useNow } from '@/composables/useNow'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'
import CreateTokenModal from '@/views/tokens/CreateTokenModal.vue'

const { t } = useI18n()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()
const now = useNow(60_000)

const tokens = ref<ApiToken[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)
const busyId = ref<number | null>(null)
const showCreate = ref(false)

async function load() {
  loading.value = true
  try {
    tokens.value = await fetchTokens()
    loadError.value = null
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

onMounted(load)

function onCreated(token: ApiToken) {
  tokens.value = [token, ...tokens.value]
}

async function revoke(token: ApiToken) {
  const ok = await confirm({
    title: t('tokens.confirmRevoke.title', { name: token.name }),
    content: t('tokens.confirmRevoke.content'),
    confirmText: t('tokens.revoke'),
    danger: true,
  })
  if (!ok) return
  busyId.value = token.id
  try {
    await revokeToken(token.id)
    tokens.value = tokens.value.filter((item) => item.id !== token.id)
    message.success(t('tokens.revoked', { name: token.name }))
  } catch (err) {
    showError(err)
  } finally {
    busyId.value = null
  }
}

function tag(text: string, type: 'default' | 'error' | 'info' = 'default') {
  return h(NTag, { size: 'small', bordered: false, type }, () => text)
}

const columns = computed<DataTableColumns<ApiToken>>(() => [
  { key: 'name', title: t('common.field.name'), minWidth: 160 },
  {
    key: 'prefix',
    title: t('tokens.column.prefix'),
    width: 170,
    render: (token) => h('code', { class: 'mono', dir: 'ltr' }, `${token.prefix}…`),
  },
  {
    key: 'permissions',
    title: t('common.field.permissions'),
    minWidth: 220,
    render: (token) =>
      h(NSpace, { size: 4 }, () => token.permissions.map((p) => tag(permissionLabel(p), 'info'))),
  },
  {
    key: 'allowedIps',
    title: t('common.field.allowedIps'),
    minWidth: 160,
    render: (token) =>
      token.allowedIps.length > 0
        ? h('span', { class: 'mono', dir: 'ltr' }, token.allowedIps.join(', '))
        : t('tokens.anyAddress'),
  },
  {
    key: 'expiresAt',
    title: t('tokens.column.expires'),
    width: 190,
    render: (token) => {
      if (token.expiresAt === 0) return t('tokens.expiry.never')
      const date = formatDateTime(token.expiresAt)
      return token.expiresAt <= now.value
        ? h(NSpace, { size: 4, align: 'center' }, () => [date, tag(t('tokens.expired'), 'error')])
        : date
    },
  },
  {
    key: 'lastUsedAt',
    title: t('tokens.column.lastUsed'),
    width: 170,
    render: (token) => (token.lastUsedAt > 0 ? formatDateTime(token.lastUsedAt) : t('tokens.neverUsed')),
  },
  {
    key: 'createdAt',
    title: t('tokens.column.created'),
    width: 170,
    render: (token) => formatDateTime(token.createdAt),
  },
  {
    key: 'actions',
    title: '',
    width: 110,
    render: (token) =>
      h(
        NButton,
        {
          size: 'small',
          type: 'error',
          ghost: true,
          loading: busyId.value === token.id,
          disabled: busyId.value !== null && busyId.value !== token.id,
          onClick: () => void revoke(token),
        },
        () => t('tokens.revoke'),
      ),
  },
])
</script>

<template>
  <NCard :title="t('tokens.title')" size="small">
    <template #header-extra>
      <NButton size="small" type="primary" @click="showCreate = true">
        <template #icon>
          <NIcon><AddOutline /></NIcon>
        </template>
        {{ t('tokens.create.title') }}
      </NButton>
    </template>
    <NAlert type="info" :show-icon="false" class="block">
      <I18nT keypath="tokens.help.usage" tag="p" class="help">
        <template #header>
          <code class="mono" dir="ltr">Authorization: Bearer &lt;token&gt;</code>
        </template>
      </I18nT>
      <p class="help">{{ t('tokens.help.stepUp') }}</p>
      <p class="help">{{ t('tokens.help.audit') }}</p>
    </NAlert>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NDataTable
      :columns="columns"
      :data="tokens"
      :loading="loading"
      :row-key="(token: ApiToken) => token.id"
      :scroll-x="1200"
      size="small"
    >
      <template #empty>
        <NText depth="3">{{ t('tokens.empty') }}</NText>
      </template>
    </NDataTable>
  </NCard>

  <CreateTokenModal v-model:show="showCreate" @created="onCreated" />
</template>

<style scoped>
.block {
  margin-block-end: 12px;
}

.help {
  margin: 0 0 4px;
}

.mono,
:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
}
</style>
