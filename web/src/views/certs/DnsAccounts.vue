<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NDataTable,
  NEmpty,
  NIcon,
  NInput,
  NModal,
  NSpace,
  NText,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { AddOutline } from '@vicons/ionicons5'
import { isApiError } from '@/api/client'
import {
  deleteDnsAccount,
  fetchDnsAccounts,
  fetchDnsProviders,
  testDnsAccount,
  type DnsAccount,
  type DnsProvider,
} from '@/api/dns'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'
import { normalizeHost } from '@/sites/validate'
import { useSessionStore } from '@/stores/session'
import DnsAccountDialog from './DnsAccountDialog.vue'

const { t } = useI18n()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()
const session = useSessionStore()

const providers = ref<DnsProvider[]>([])
const accounts = ref<DnsAccount[]>([])
const loading = ref(true)
const loadError = ref<unknown>(null)
const showDialog = ref(false)
const editing = ref<DnsAccount | null>(null)

/** The account being tested and the zone asked for. */
const testing = ref<DnsAccount | null>(null)
const zone = ref('')
const testBusy = ref(false)
const testError = ref<unknown>(null)

const canManage = computed(() => session.can('certs.manage'))

async function load() {
  try {
    const [list, provs] = await Promise.all([fetchDnsAccounts(), fetchDnsProviders()])
    accounts.value = list
    providers.value = provs
    loadError.value = null
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

onMounted(load)

/** Opens the form for a new account (also used from the issue dialog). */
function openCreate() {
  editing.value = null
  showDialog.value = true
}

defineExpose({ openCreate })

function providerName(id: string): string {
  return providers.value.find((p) => p.id === id)?.name ?? id
}

function openTest(account: DnsAccount) {
  testing.value = account
  zone.value = ''
  testError.value = null
}

async function runTest() {
  const account = testing.value
  const name = normalizeHost(zone.value)
  if (!account || !name) return
  testBusy.value = true
  testError.value = null
  try {
    const records = await testDnsAccount(account.id, name)
    message.success(t('dns.test.ok', { count: records, zone: name }, records))
    testing.value = null
  } catch (err) {
    testError.value = err
  } finally {
    testBusy.value = false
  }
}

const testReason = computed(() =>
  isApiError(testError.value) && typeof testError.value.params.reason === 'string' ? testError.value.params.reason : '',
)

async function remove(account: DnsAccount) {
  const ok = await confirm({
    title: t('dns.delete.title', { name: account.name }),
    content: t('dns.delete.content'),
    confirmText: t('common.action.delete'),
    danger: true,
  })
  if (!ok) return
  try {
    await deleteDnsAccount(account.id)
    message.success(t('dns.delete.done', { name: account.name }))
    await load()
  } catch (err) {
    showError(err)
  }
}

const columns = computed<DataTableColumns<DnsAccount>>(() => [
  { key: 'name', title: t('common.field.name'), minWidth: 160 },
  { key: 'provider', title: t('dns.provider'), minWidth: 160, render: (a) => providerName(a.provider) },
  {
    key: 'usedBy',
    title: t('certs.column.usedBy'),
    width: 160,
    render: (a) => t('dns.usedBy', { count: a.usedBy.length }, a.usedBy.length),
  },
  { key: 'updatedAt', title: t('sites.overview.updated'), width: 180, render: (a) => formatDateTime(a.updatedAt) },
  {
    key: 'actions',
    title: '',
    width: 240,
    render: (a) =>
      canManage.value
        ? h(NSpace, { size: 4, wrap: false }, () => [
            h(NButton, { size: 'small', onClick: () => openTest(a) }, () => t('dns.test.button')),
            h(NButton, { size: 'small', quaternary: true, onClick: () => ((editing.value = a), (showDialog.value = true)) }, () =>
              t('common.action.edit'),
            ),
            h(NButton, { size: 'small', quaternary: true, type: 'error', onClick: () => void remove(a) }, () =>
              t('common.action.delete'),
            ),
          ])
        : null,
  },
])
</script>

<template>
  <div>
    <NText depth="3" tag="p" class="intro">{{ t('dns.intro') }}</NText>
    <div v-if="canManage" class="toolbar">
      <NButton type="primary" @click="openCreate">
        <template #icon>
          <NIcon><AddOutline /></NIcon>
        </template>
        {{ t('dns.dialog.createTitle') }}
      </NButton>
    </div>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NDataTable
      v-if="loading || accounts.length > 0"
      :columns="columns"
      :data="accounts"
      :loading="loading"
      :row-key="(a: DnsAccount) => a.id"
      :scroll-x="800"
      size="small"
    />
    <NEmpty v-else :description="t('dns.empty')" class="empty" />

    <DnsAccountDialog v-model:show="showDialog" :providers="providers" :account="editing" @saved="load" />

    <NModal
      :show="testing !== null"
      preset="card"
      :title="testing ? t('dns.test.title', { name: testing.name }) : ''"
      class="test-dialog"
      @update:show="(open: boolean) => !open && (testing = null)"
    >
      <NText depth="3" tag="p" class="intro">{{ t('dns.test.intro') }}</NText>
      <NInput
        v-model:value="zone"
        :placeholder="t('dns.test.zonePlaceholder')"
        :input-props="{ autocomplete: 'off', spellcheck: false, 'aria-label': t('common.field.zone') }"
        @keyup.enter="runTest"
      />
      <NAlert v-if="testError" type="error" class="block">
        <div>{{ errorMessage(testError) }}</div>
        <pre v-if="testReason" class="reason" dir="ltr">{{ testReason }}</pre>
      </NAlert>
      <div class="actions">
        <NButton :disabled="testBusy" @click="testing = null">{{ t('common.action.cancel') }}</NButton>
        <NButton type="primary" :loading="testBusy" :disabled="!zone.trim()" @click="runTest">
          {{ t('dns.test.button') }}
        </NButton>
      </div>
    </NModal>
  </div>
</template>

<style scoped>
.intro {
  margin-block: 0 12px;
}

.toolbar {
  margin-block-end: 12px;
}

.block {
  margin-block: 12px;
}

.empty {
  padding-block: 32px;
}

.test-dialog {
  inline-size: min(460px, calc(100vw - 32px));
}

.reason {
  margin: 8px 0 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
