<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { LoaderCircle, Pencil, Plus, RotateCw, Send, Trash2 } from '@lucide/vue'
import { isApiError } from '@/api/client'
import {
  EVENT_CATEGORIES,
  LEVELS,
  deleteChannel,
  fetchChannels,
  fetchDeliveries,
  testChannel,
  updateChannel,
  type Channel,
  type Delivery,
} from '@/api/notify'
import Card from '@/components/common/Card.vue'
import DataTable from '@/components/common/DataTable.vue'
import Notice from '@/components/common/Notice.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import RowActions from '@/components/common/RowActions.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { confirmAction } from '@/composables/useConfirmDialog'
import { localeNativeName } from '@/i18n'
import { errorMessage } from '@/i18n/errors'
import { formatNumber } from '@/i18n/format'
import type { Tone } from '@/lib/tone'
import { notifyError, notifySuccess } from '@/lib/notify'
import ChannelSheet from '@/views/notify/ChannelSheet.vue'
import { categoryText, eventText, levelText, levelTone } from '@/views/notify/labels'

// Where the panel sends its notifications: email and webhook channels with
// the events each receives, a test message per channel, and the recent
// deliveries with their errors.
const { t } = useI18n()

const channels = ref<Channel[]>([])
const deliveries = ref<Delivery[]>([])
const loadingChannels = ref(false)
const loadingDeliveries = ref(false)
const channelsError = ref<unknown>(null)
const deliveriesError = ref<unknown>(null)
/** Channels whose switch or test is on its way. */
const busy = ref(new Set<number>())
const testingId = ref<number | null>(null)

const showSheet = ref(false)
const editing = ref<Channel | null>(null)

/** Last failed test message: translated error plus the server's technical reason. */
const testFailure = ref<{ name: string; error: unknown; reason: string } | null>(null)

async function loadChannels() {
  loadingChannels.value = true
  try {
    channels.value = await fetchChannels()
    channelsError.value = null
  } catch (err) {
    channelsError.value = err
  } finally {
    loadingChannels.value = false
  }
}

async function loadDeliveries() {
  loadingDeliveries.value = true
  try {
    deliveries.value = await fetchDeliveries()
    deliveriesError.value = null
  } catch (err) {
    deliveriesError.value = err
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
  channels.value = exists ? channels.value.map((c) => (c.id === channel.id ? channel : c)) : [...channels.value, channel]
  if (editing.value?.id === channel.id) editing.value = channel
}

function setBusy(id: number, on: boolean) {
  const next = new Set(busy.value)
  if (on) next.add(id)
  else next.delete(id)
  busy.value = next
}

function openCreate() {
  editing.value = null
  showSheet.value = true
}

function openEdit(channel: Channel) {
  editing.value = channel
  showSheet.value = true
}

function onSaved(channel: Channel) {
  replace(channel)
  notifySuccess(t('notify.channel.saved', { name: channel.name }))
}

async function setEnabled(channel: Channel, enabled: boolean) {
  setBusy(channel.id, true)
  try {
    replace(await updateChannel(channel.id, { enabled }))
  } catch (err) {
    notifyError(err)
  } finally {
    setBusy(channel.id, false)
  }
}

async function sendTest(channel: Channel) {
  testingId.value = channel.id
  testFailure.value = null
  try {
    await testChannel(channel.id)
    notifySuccess(t('notify.test.sent', { name: channel.name }))
  } catch (err) {
    if (isApiError(err) && err.code === 'notify.delivery_failed') {
      const reason = err.params.reason
      testFailure.value = { name: channel.name, error: err, reason: typeof reason === 'string' ? reason : '' }
      // The failure is shown on the page, under the sheet if it is open.
      if (showSheet.value) notifyError(err)
    } else {
      notifyError(err)
    }
  } finally {
    testingId.value = null
    void loadDeliveries()
  }
}

function askDelete(channel: Channel) {
  void confirmAction({
    title: t('notify.channel.confirmDelete.title', { name: channel.name }),
    description: t('notify.channel.confirmDelete.content'),
    confirmLabel: t('notify.channel.delete'),
    destructive: true,
    run: async () => {
      await deleteChannel(channel.id)
      channels.value = channels.value.filter((c) => c.id !== channel.id)
      notifySuccess(t('notify.channel.deleted', { name: channel.name }))
    },
  })
}

function allEvents(channel: Channel): boolean {
  return EVENT_CATEGORIES.every((prefix) => channel.events.includes(prefix))
}

function channelTarget(channel: Channel): string {
  return channel.type === 'email' ? channel.config.to.join(', ') : channel.config.url
}

function actions(channel: Channel): RowAction[] {
  return [
    { key: 'edit', label: t('common.action.edit'), icon: Pencil, onSelect: () => openEdit(channel) },
    { key: 'delete', label: t('notify.channel.delete'), icon: Trash2, destructive: true, onSelect: () => askDelete(channel) },
  ]
}

const STATUS_TONE: Record<Delivery['status'], Tone> = { pending: 'progress', sent: 'success', failed: 'destructive' }

const channelColumns = computed<ColumnDef<Channel>[]>(() => [
  { id: 'name', header: t('common.field.name'), accessorFn: (c) => c.name.toLowerCase(), meta: { class: 'min-w-[200px]' } },
  { id: 'events', header: t('common.field.events'), enableSorting: false, meta: { class: 'min-w-[170px]' } },
  { id: 'minLevel', header: t('common.field.minLevel'), accessorFn: (c) => LEVELS.indexOf(c.minLevel), meta: { class: 'w-[120px]' } },
  { id: 'enabled', header: t('notify.channel.enabled'), enableSorting: false, meta: { class: 'w-[80px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[160px] text-end' } },
])

const deliveryColumns = computed<ColumnDef<Delivery>[]>(() => [
  { id: 'createdAt', header: t('notify.delivery.created'), accessorFn: (d) => d.createdAt, meta: { class: 'w-[130px]' } },
  { id: 'channel', header: t('notify.delivery.channel'), accessorFn: (d) => d.channelName.toLowerCase(), meta: { class: 'min-w-[120px]' } },
  { id: 'event', header: t('notify.delivery.event'), accessorFn: (d) => eventText(d.event), meta: { class: 'min-w-[150px]' } },
  { id: 'level', header: t('notify.delivery.level'), accessorFn: (d) => LEVELS.indexOf(d.level), meta: { class: 'w-[100px]' } },
  { id: 'status', header: t('notify.delivery.status'), accessorFn: (d) => d.status, meta: { class: 'w-[140px]' } },
  { id: 'lastError', header: t('notify.delivery.lastError'), enableSorting: false, meta: { class: 'min-w-[180px]' } },
])
</script>

<template>
  <Notice v-if="testFailure" tone="destructive" dismissible @dismiss="testFailure = null">
    {{ t('notify.test.failed', { name: testFailure.name }) }}: {{ errorMessage(testFailure.error) }}
    <code v-if="testFailure.reason" class="mt-1 block font-mono text-xs font-normal [overflow-wrap:anywhere]" dir="ltr">
      {{ testFailure.reason }}
    </code>
  </Notice>

  <Card :title="t('notify.channels.title')" :description="t('notify.channels.description')" :level="3" flush>
    <template #actions>
      <Button size="sm" class="max-md:h-11" @click="openCreate">
        <Plus aria-hidden="true" />
        {{ t('notify.channel.createTitle') }}
      </Button>
    </template>
    <DataTable
      flat
      :data="channels"
      :columns="channelColumns"
      :row-key="(c) => String(c.id)"
      :label="t('notify.channels.title')"
      :loading="loadingChannels"
      :error="channelsError"
      :empty-title="t('notify.channels.empty')"
      :empty-text="t('notify.channels.emptyText')"
      :min-width="760"
      :skeleton-rows="2"
      :initial-sort="[{ id: 'name', desc: false }]"
      @retry="loadChannels"
    >
      <template #cell-name="{ row }">
        <span class="flex min-w-0 flex-col">
          <span class="flex flex-wrap items-center gap-1.5">
            <button
              type="button"
              class="truncate rounded-sm text-start font-semibold underline decoration-border underline-offset-[3px] tap-y outline-none hover:decoration-current focus-visible:ring-2 focus-visible:ring-ring/50"
              dir="auto"
              @click="openEdit(row)"
            >
              {{ row.name }}
            </button>
            <StatusPill>{{ t(`notify.type.${row.type}`) }}</StatusPill>
          </span>
          <span class="truncate font-mono text-xs text-muted-foreground" dir="ltr" :title="channelTarget(row)">
            {{ channelTarget(row) }}
          </span>
        </span>
      </template>
      <template #cell-events="{ row }">
        <span v-if="allEvents(row)" class="text-muted-foreground">{{ t('notify.channels.allEvents') }}</span>
        <span v-else class="flex flex-wrap gap-1">
          <StatusPill v-for="prefix in row.events" :key="prefix">{{ categoryText(prefix) }}</StatusPill>
        </span>
      </template>
      <template #cell-minLevel="{ row }">
        <span class="flex flex-col items-start gap-0.5">
          <StatusPill :tone="levelTone(row.minLevel)">{{ levelText(row.minLevel) }}</StatusPill>
          <span class="text-xs text-muted-foreground" :title="t('notify.channel.locale')">
            <span :lang="row.locale">{{ localeNativeName(row.locale) }}</span>
          </span>
        </span>
      </template>
      <template #cell-enabled="{ row }">
        <Switch
          :model-value="row.enabled"
          :disabled="busy.has(row.id)"
          :aria-label="t('notify.channel.enableNamed', { name: row.name })"
          @update:model-value="(value: boolean) => setEnabled(row, value)"
        />
      </template>
      <template #cell-actions="{ row }">
        <div class="flex justify-end gap-2">
          <Button
            variant="outline"
            size="sm"
            class="max-md:h-11"
            :disabled="testingId !== null"
            @click="sendTest(row)"
          >
            <LoaderCircle v-if="testingId === row.id" class="animate-spin" aria-hidden="true" />
            <Send v-else aria-hidden="true" />
            {{ t('notify.test.send') }}
          </Button>
          <RowActions :actions="actions(row)" :label="t('notify.channel.more', { name: row.name })" />
        </div>
      </template>
      <template #empty>
        <Button @click="openCreate">
          <Plus aria-hidden="true" />
          {{ t('notify.channel.createTitle') }}
        </Button>
      </template>
    </DataTable>
  </Card>

  <Card :title="t('notify.delivery.title')" :description="t('notify.delivery.description')" :level="3" flush>
    <template #actions>
      <Button variant="outline" size="sm" class="max-md:h-11" :disabled="loadingDeliveries" @click="loadDeliveries">
        <LoaderCircle v-if="loadingDeliveries" class="animate-spin" aria-hidden="true" />
        <RotateCw v-else aria-hidden="true" />
        {{ t('common.action.refresh') }}
      </Button>
    </template>
    <DataTable
      flat
      :data="deliveries"
      :columns="deliveryColumns"
      :row-key="(d) => String(d.id)"
      :label="t('notify.delivery.title')"
      :loading="loadingDeliveries"
      :error="deliveriesError"
      :empty-title="t('notify.delivery.empty')"
      :min-width="820"
      :skeleton-rows="3"
      :initial-sort="[{ id: 'createdAt', desc: true }]"
      @retry="loadDeliveries"
    >
      <template #cell-createdAt="{ row }"><RelativeTime :value="row.createdAt" /></template>
      <template #cell-channel="{ row }"><span dir="auto">{{ row.channelName }}</span></template>
      <template #cell-event="{ row }">{{ eventText(row.event) }}</template>
      <template #cell-level="{ row }"><StatusPill :tone="levelTone(row.level)">{{ levelText(row.level) }}</StatusPill></template>
      <template #cell-status="{ row }">
        <span class="flex flex-col gap-0.5">
          <StatusDot :tone="STATUS_TONE[row.status] ?? 'neutral'">{{ t(`notify.status.${row.status}`) }}</StatusDot>
          <span class="text-xs text-muted-foreground">
            <template v-if="row.sentAt > 0"><RelativeTime :value="row.sentAt" /> · </template>
            {{ t('notify.delivery.attemptCount', { count: formatNumber(row.attempts) }, row.attempts) }}
          </span>
        </span>
      </template>
      <template #cell-lastError="{ row }">
        <span v-if="row.lastError" class="line-clamp-2 font-mono text-xs [overflow-wrap:anywhere]" dir="ltr" :title="row.lastError">
          {{ row.lastError }}
        </span>
        <span v-else class="text-muted-foreground">—</span>
      </template>
    </DataTable>
  </Card>

  <ChannelSheet
    v-model:open="showSheet"
    :channel="editing"
    :testing="testingId !== null && testingId === editing?.id"
    @saved="onSaved"
    @test="sendTest"
  />
</template>
