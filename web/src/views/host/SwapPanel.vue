<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { LoaderCircle, Trash2 } from '@lucide/vue'
import { SWAP_MAX_MB, SWAP_MIN_MB, setSwap, setSwappiness, type HostView, type SwapDevice } from '@/api/host'
import Card from '@/components/common/Card.vue'
import DataTable from '@/components/common/DataTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Notice from '@/components/common/Notice.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { confirmAction } from '@/composables/useConfirmDialog'
import { formatBytes, formatPercent } from '@/i18n/format'
import { launchJob } from '@/jobs/dock'
import { notifyError, notifySuccess } from '@/lib/notify'
import { TONE_DOT, usageTone } from '@/lib/tone'
import { cn } from '@/lib/utils'

// Swap: what the system has, the panel's swap file (a job) and swappiness.
const props = defineProps<{ view: HostView; canManage: boolean }>()
const emit = defineEmits<{ changed: [] }>()

const MIB = 1024 * 1024

const { t } = useI18n()
const sizeMb = ref('2048')
const swappiness = ref(60)
const savingSwappiness = ref(false)
/** A swap file job runs. */
const working = ref(false)

const swap = computed(() => props.view.swap)
const managed = computed(() => swap.value.devices.find((device) => device.managed) ?? null)
const usedPercent = computed(() => (swap.value.total > 0 ? (swap.value.used / swap.value.total) * 100 : 0))

watch(
  () => props.view.swap,
  (value) => {
    swappiness.value = value.swappiness
    const file = value.devices.find((device) => device.managed)
    if (file) sizeMb.value = String(Math.round(file.size / MIB))
  },
  { immediate: true },
)

const size = computed(() => (/^\d{1,6}$/.test(sizeMb.value.trim()) ? Number(sizeMb.value.trim()) : null))
const sizeValid = computed(() => size.value !== null && size.value >= SWAP_MIN_MB && size.value <= SWAP_MAX_MB)
const sizeError = computed(() =>
  sizeMb.value.trim() && !sizeValid.value ? t('host.swap.sizeInvalid', { min: SWAP_MIN_MB, max: SWAP_MAX_MB }) : null,
)
const sameSize = computed(() => !!managed.value && size.value === Math.round(managed.value.size / MIB))

async function runSwapJob(target: number, rethrow: boolean) {
  working.value = true
  try {
    // Step-up protected: the client asks for the password and retries.
    const job = await launchJob(() => setSwap(target), {
      rethrow,
      onFinished: () => {
        working.value = false
        emit('changed')
      },
    })
    if (!job) working.value = false
  } catch (err) {
    working.value = false
    throw err
  }
}

function apply() {
  if (!sizeValid.value || size.value === null) return
  void runSwapJob(size.value, false)
}

function remove() {
  void confirmAction({
    title: t('host.swap.removeTitle'),
    description: t('host.swap.removeText'),
    confirmLabel: t('host.swap.remove'),
    destructive: true,
    run: () => runSwapJob(0, true),
  })
}

const swappinessValid = computed(() => Number.isInteger(swappiness.value) && swappiness.value >= 0 && swappiness.value <= 100)

async function saveSwappiness() {
  if (!swappinessValid.value) return
  savingSwappiness.value = true
  try {
    await setSwappiness(swappiness.value)
    notifySuccess(t('host.general.saved'))
    emit('changed')
  } catch (err) {
    notifyError(err)
  } finally {
    savingSwappiness.value = false
  }
}

const columns = computed<ColumnDef<SwapDevice>[]>(() => [
  { id: 'path', header: t('common.field.path'), accessorFn: (device) => device.path, meta: { class: 'min-w-[200px]' } },
  { id: 'kind', header: t('common.field.type'), accessorFn: (device) => device.kind, meta: { class: 'w-[120px]' } },
  { id: 'size', header: t('common.field.size'), accessorFn: (device) => device.size, meta: { class: 'w-[120px] text-end' } },
  { id: 'used', header: t('host.swap.used'), accessorFn: (device) => device.used, meta: { class: 'w-[120px] text-end' } },
])
</script>

<template>
  <div class="grid items-start gap-4 lg:grid-cols-2">
    <Card :title="t('host.swap.title')" class="lg:col-span-2" flush>
      <div class="flex flex-col gap-2 px-4 py-4 md:px-[18px]">
        <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
          <span class="font-medium tabular-nums">{{ t('host.swap.usage', { used: formatBytes(swap.used), total: formatBytes(swap.total) }) }}</span>
          <span v-if="swap.total > 0" class="text-detail text-muted-foreground tabular-nums">{{ formatPercent(usedPercent, 0) }}</span>
        </div>
        <div
          role="progressbar"
          :aria-label="t('host.swap.title')"
          aria-valuemin="0"
          aria-valuemax="100"
          :aria-valuenow="Math.round(usedPercent)"
          class="h-1.5 overflow-hidden rounded-full bg-track"
        >
          <div :class="cn('h-full rounded-full', TONE_DOT[usageTone(usedPercent)])" :style="{ width: `${usedPercent}%` }" />
        </div>
      </div>
      <div class="border-t">
        <DataTable
          v-if="swap.devices.length > 0"
          :data="swap.devices"
          :columns="columns"
          :row-key="(device) => device.path"
          :label="t('host.swap.devices')"
          :min-width="560"
          flat
        >
          <template #cell-path="{ row }">
            <span class="inline-flex flex-wrap items-center gap-2">
              <span class="font-mono text-[12.5px]" dir="ltr">{{ row.path }}</span>
              <StatusPill v-if="row.managed" tone="progress">{{ t('host.swap.managed') }}</StatusPill>
            </span>
          </template>
          <template #cell-kind="{ row }">{{ t(`host.swap.kind.${row.kind}`) }}</template>
          <template #cell-size="{ row }"><span class="tabular-nums">{{ formatBytes(row.size) }}</span></template>
          <template #cell-used="{ row }"><span class="tabular-nums">{{ formatBytes(row.used) }}</span></template>
        </DataTable>
        <EmptyState v-else compact :title="t('host.swap.none')" />
      </div>
    </Card>

    <Card :title="t('host.swap.fileTitle')" :description="managed ? t('host.swap.fileHas', { size: formatBytes(managed.size) }) : t('host.swap.fileNone')">
      <Notice v-if="!swap.supported" tone="warning">{{ t(`host.swap.unsupported.${swap.reason || 'other'}`) }}</Notice>
      <form v-else class="flex flex-col gap-1.5" novalidate @submit.prevent="apply">
        <label for="swap-size" class="text-detail font-medium">{{ t('common.field.sizeMb') }}</label>
        <div class="flex flex-wrap items-center gap-2">
          <div class="flex items-center gap-2">
            <Input
              id="swap-size"
              v-model="sizeMb"
              inputmode="numeric"
              autocomplete="off"
              class="w-32 font-mono"
              aria-describedby="swap-size-hint"
              :aria-invalid="sizeError ? true : undefined"
              :disabled="!canManage || working"
            />
            <span class="text-detail text-muted-foreground">{{ t('host.swap.mib') }}</span>
          </div>
          <template v-if="canManage">
            <Button type="submit" class="max-md:h-11" :disabled="!sizeValid || sameSize || working">
              <LoaderCircle v-if="working" class="animate-spin" aria-hidden="true" />
              {{ managed ? t('host.swap.resize') : t('host.swap.create') }}
            </Button>
            <Button
              v-if="managed"
              type="button"
              variant="ghost"
              class="text-destructive hover:bg-destructive-soft hover:text-destructive max-md:h-11"
              :disabled="working"
              @click="remove"
            >
              <Trash2 aria-hidden="true" />
              {{ t('host.swap.remove') }}
            </Button>
          </template>
        </div>
        <p v-if="sizeError" class="m-0 text-xs font-medium text-destructive">{{ sizeError }}</p>
        <p id="swap-size-hint" class="m-0 text-xs text-muted-foreground">{{ t('host.swap.sizeHint', { min: SWAP_MIN_MB, max: SWAP_MAX_MB }) }}</p>
      </form>
    </Card>

    <Card :title="t('host.swap.swappiness')" :description="t('host.swap.swappinessHint')">
      <form class="flex flex-wrap items-center gap-3" novalidate @submit.prevent="saveSwappiness">
        <label for="swap-swappiness" class="sr-only">{{ t('host.swap.swappiness') }}</label>
        <input
          v-model.number="swappiness"
          type="range"
          min="0"
          max="100"
          step="1"
          class="h-9 min-w-0 flex-[1_1_160px] accent-primary max-md:h-11"
          :aria-label="t('host.swap.swappiness')"
          :disabled="!canManage || savingSwappiness"
        />
        <Input
          id="swap-swappiness"
          :model-value="swappiness"
          type="number"
          min="0"
          max="100"
          class="w-20 font-mono"
          :disabled="!canManage || savingSwappiness"
          @update:model-value="(value: string | number) => (swappiness = value === '' ? Number.NaN : Number(value))"
        />
        <Button v-if="canManage" type="submit" class="max-md:h-11" :disabled="!swappinessValid || swappiness === swap.swappiness || savingSwappiness">
          <LoaderCircle v-if="savingSwappiness" class="animate-spin" aria-hidden="true" />
          {{ t('host.swap.saveSwappiness') }}
        </Button>
      </form>
    </Card>
  </div>
</template>
