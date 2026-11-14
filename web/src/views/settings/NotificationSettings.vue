<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NEllipsis,
  NIcon,
  NSpace,
  NSwitch,
  NTag,
  NText,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { AddOutline, RefreshOutline } from '@vicons/ionicons5'
import { isApiError } from '@/api/client'
import {
  deleteChannel,
  fetchChannels,
  fetchDeliveries,
  testChannel,
  updateChannel,
  type Channel,
  type Delivery,
  type Level,
} from '@/api/notify'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { localeNativeName } from '@/i18n'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime, formatNumber } from '@/i18n/format'
import ChannelModal from '@/views/notify/ChannelModal.vue'

const { t, te } = useI18n()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()

const channels = ref<Channel[]>([])
const deliveries = ref<Delivery[]>([])
const loadingChannels = ref(false)
const loadingDeliveries = ref(false)
const loadError = ref<unknown>(null)
const busyId = ref<number | null>(null)

const showModal = ref(false)
const editing = ref<Channel | null>(null)

/** Last failed test message: translated error plus the server's technical reason. */
const testFailure = ref<{ name: string; error: unknown; reason: string } | null>(null)

async function loadChannels() {
  loadingChannels.value = true
  try {
    channels.value = await fetchChannels()
    loadError.value = null
  } catch (err) {
    loadError.value = err
  } finally {
    loadingChannels.value = false
  }
}

async function loadDeliveries() {
  loadingDeliveries.value = true
  try {
    deliveries.value = await fetchDeliveries()
  } catch (err) {
    showError(err)
  } finally {
    loadingDeliveries.value = false
  }
}

onMounted(() => {
  void loadChannels()
  void loadDeliveries()
})

function replace(channel: Channel) {
  const exists = channels.value.some((c) => c.id === channel.id)
  channels.value = exists
    ? channels.value.map((c) => (c.id === channel.id ? channel : c))
    : [...channels.value, channel]
}

function openCreate() {
  editing.value = null
  showModal.value = true
}

function openEdit(channel: Channel) {
  editing.value = channel
  showModal.value = true
}

function onSaved(channel: Channel) {
  replace(channel)
  message.success(t('notify.channel.saved', { name: channel.name }))
}

async function setEnabled(channel: Channel, enabled: boolean) {
  busyId.value = channel.id
  try {
    replace(await updateChannel(channel.id, { enabled }))
  } catch (err) {
    showError(err)
  } finally {
    busyId.value = null
  }
}

async function sendTest(channel: Channel) {
  busyId.value = channel.id
  testFailure.value = null
  try {
    await testChannel(channel.id)
    message.success(t('notify.test.sent', { name: channel.name }))
  } catch (err) {
    if (isApiError(err) && err.code === 'notify.delivery_failed') {
      const reason = err.params.reason
      testFailure.value = { name: channel.name, error: err, reason: typeof reason === 'string' ? reason : '' }
    } else {
      showError(err)
    }
  } finally {
    busyId.value = null
    void loadDeliveries()
  }
}

async function remove(channel: Channel) {
  const ok = await confirm({
    title: t('notify.channel.confirmDelete.title', { name: channel.name }),
    content: t('notify.channel.confirmDelete.content'),
    confirmText: t('common.action.delete'),
    danger: true,
  })
  if (!ok) return
  busyId.value = channel.id
  try {
    await deleteChannel(channel.id)
    channels.value = channels.value.filter((c) => c.id !== channel.id)
    message.success(t('notify.channel.deleted', { name: channel.name }))
  } catch (err) {
    showError(err)
  } finally {
    busyId.value = null
  }
}

function categoryLabel(prefix: string): string {
  const key = `notify.category.${prefix.replace(/\.$/, '')}.label`
  return te(key) ? t(key) : prefix
}

function eventLabel(event: string): string {
  const key = `notify.event.${event}`
  return te(key) ? t(key) : event
}

const LEVEL_TAG: Record<Level, 'info' | 'warning' | 'error'> = {
  info: 'info',
  warning: 'warning',
  critical: 'error',
}

function levelTag(level: Level) {
  const key = `notify.level.${level}`
  return h(NTag, { size: 'small', bordered: false, type: LEVEL_TAG[level] ?? 'default' }, () =>
    te(key) ? t(key) : level,
  )
}

const STATUS_TAG = { pending: 'default', sent: 'success', failed: 'error' } as const

function smallTag(text: string, type: 'default' | 'success' | 'error' | 'info' = 'default') {
  return h(NTag, { size: 'small', bordered: false, type }, () => text)
}

const channelColumns = computed<DataTableColumns<Channel>>(() => [
  {
    key: 'name',
    title: t('common.field.name'),
    minWidth: 180,
    render: (c) =>
      h('span', { class: 'name' }, [c.name, smallTag(t(`notify.type.${c.type}`), 'info')]),
  },
  {
    key: 'events',
    title: t('common.field.events'),
    minWidth: 200,
    render: (c) => h(NSpace, { size: 4 }, () => c.events.map((e) => smallTag(categoryLabel(e)))),
  },
  { key: 'minLevel', title: t('common.field.minLevel'), width: 130, render: (c) => levelTag(c.minLevel) },
  {
    key: 'locale',
    title: t('notify.channel.locale'),
    width: 120,
    render: (c) => h('span', { lang: c.locale }, localeNativeName(c.locale)),
  },
  {
    key: 'enabled',
    title: t('notify.channel.enabled'),
    width: 100,
    render: (c) =>
      h(NSwitch, {
        value: c.enabled,
        size: 'small',
        loading: busyId.value === c.id,
        disabled: busyId.value !== null && busyId.value !== c.id,
        'onUpdate:value': (value: boolean) => void setEnabled(c, value),
      }),
  },
  {
    key: 'actions',
    title: '',
    width: 260,
    render: (c) =>
      h(NSpace, { size: 4, wrap: false }, () => [
        h(
          NButton,
          {
            size: 'small',
            loading: busyId.value === c.id,
            disabled: busyId.value !== null,
            onClick: () => void sendTest(c),
          },
          () => t('notify.test.send'),
        ),
        h(NButton, { size: 'small', quaternary: true, onClick: () => openEdit(c) }, () =>
          t('common.action.edit'),
        ),
        h(
          NButton,
          {
            size: 'small',
            quaternary: true,
            type: 'error',
            disabled: busyId.value !== null,
            onClick: () => void remove(c),
          },
          () => t('common.action.delete'),
        ),
      ]),
  },
])

const deliveryColumns = computed<DataTableColumns<Delivery>>(() => [
  {
    key: 'createdAt',
    title: t('notify.delivery.created'),
    width: 170,
    render: (d) => formatDateTime(d.createdAt),
  },
  { key: 'channelName', title: t('notify.delivery.channel'), minWidth: 140 },
  { key: 'event', title: t('notify.delivery.event'), minWidth: 160, render: (d) => eventLabel(d.event) },
  { key: 'level', title: t('notify.delivery.level'), width: 110, render: (d) => levelTag(d.level) },
  {
    key: 'status',
    title: t('notify.delivery.status'),
    width: 110,
    render: (d) => smallTag(t(`notify.status.${d.status}`), STATUS_TAG[d.status] ?? 'default'),
  },
  {
    key: 'attempts',
    title: t('notify.delivery.attempts'),
    width: 90,
    render: (d) => formatNumber(d.attempts),
  },
  {
    key: 'sentAt',
    title: t('notify.delivery.sent'),
    width: 170,
    render: (d) => (d.sentAt > 0 ? formatDateTime(d.sentAt) : '-'),
  },
  {
    key: 'lastError',
    title: t('notify.delivery.lastError'),
    minWidth: 220,
    render: (d) =>
      d.lastError
        ? h(NEllipsis, { class: 'mono', style: 'max-width: 100%' }, () => d.lastError)
        : '',
  },
])
</script>

<template>
  <NCard :title="t('notify.channels.title')" size="small">
    <template #header-extra>
      <NButton size="small" type="primary" @click="openCreate">
        <template #icon>
          <NIcon><AddOutline /></NIcon>
        </template>
        {{ t('notify.channel.createTitle') }}
      </NButton>
    </template>
    <NText depth="3" tag="p" class="intro">{{ t('notify.channels.description') }}</NText>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NAlert
      v-if="testFailure"
      type="error"
      closable
      :title="t('notify.test.failed', { name: testFailure.name })"
      class="block"
      @close="testFailure = null"
    >
      <div>{{ errorMessage(testFailure.error) }}</div>
      <code v-if="testFailure.reason" class="mono reason" dir="ltr">{{ testFailure.reason }}</code>
    </NAlert>
    <NDataTable
      :columns="channelColumns"
      :data="channels"
      :loading="loadingChannels"
      :row-key="(c: Channel) => c.id"
      :scroll-x="1000"
      size="small"
    />
  </NCard>

  <NCard :title="t('notify.delivery.title')" size="small">
    <template #header-extra>
      <NButton size="small" quaternary :loading="loadingDeliveries" @click="loadDeliveries">
        <template #icon>
          <NIcon><RefreshOutline /></NIcon>
        </template>
        {{ t('common.action.refresh') }}
      </NButton>
    </template>
    <NDataTable
      :columns="deliveryColumns"
      :data="deliveries"
      :loading="loadingDeliveries"
      :row-key="(d: Delivery) => d.id"
      :scroll-x="1200"
      size="small"
    />
  </NCard>

  <ChannelModal v-model:show="showModal" :channel="editing" @saved="onSaved" />
</template>

<style scoped>
.intro {
  margin-block: 0 12px;
}

.block {
  margin-block-end: 12px;
}

.reason {
  display: block;
  margin-block-start: 6px;
  overflow-wrap: anywhere;
}

.mono,
:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
}

:deep(.name) {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
</style>
