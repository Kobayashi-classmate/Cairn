<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Swap: what the system has, the panel's swap file and swappiness.
import { computed, h, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NDataTable, NInputNumber, NProgress, NSlider, NTag, NText, useMessage, type DataTableColumns } from 'naive-ui'
import { SWAP_MAX_MB, SWAP_MIN_MB, setSwap, setSwappiness, type HostView, type SwapDevice } from '@/api/host'
import type { Job } from '@/api/jobs'
import JobProgress from '@/components/JobProgress.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { useJobRun } from '@/composables/useJobRun'
import { formatBytes } from '@/i18n/format'

const props = defineProps<{ view: HostView; canManage: boolean }>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()
const sizeMb = ref<number | null>(2048)
const swappiness = ref(60)
const savingSwappiness = ref(false)

const swap = computed(() => props.view.swap)
const managed = computed(() => swap.value.devices.find((device) => device.managed) ?? null)
const usedPercent = computed(() => (swap.value.total > 0 ? (swap.value.used / swap.value.total) * 100 : 0))

watch(
  () => props.view.swap,
  (value) => {
    swappiness.value = value.swappiness
    const file = value.devices.find((device) => device.managed)
    if (file) sizeMb.value = Math.round(file.size / (1024 * 1024))
  },
  { immediate: true },
)

const { jobId, busy, run, finished, dismiss } = useJobRun((_job: Job) => emit('changed'))

const sizeValid = computed(() => sizeMb.value !== null && sizeMb.value >= SWAP_MIN_MB && sizeMb.value <= SWAP_MAX_MB)

function apply() {
  if (!sizeValid.value || sizeMb.value === null) return
  const size = sizeMb.value
  // Step-up protected: the client asks for the password and retries.
  void run(() => setSwap(size))
}

async function remove() {
  const ok = await confirm({
    title: t('host.swap.removeTitle'),
    content: t('host.swap.removeText'),
    confirmText: t('common.action.delete'),
    danger: true,
  })
  if (ok) void run(() => setSwap(0))
}

async function saveSwappiness() {
  savingSwappiness.value = true
  try {
    await setSwappiness(swappiness.value)
    message.success(t('host.general.saved'))
    emit('changed')
  } catch (err) {
    showError(err)
  } finally {
    savingSwappiness.value = false
  }
}

const columns = computed<DataTableColumns<SwapDevice>>(() => [
  {
    key: 'path',
    title: t('common.field.path'),
    minWidth: 200,
    render: (device) =>
      h('span', { class: 'device' }, [
        h('span', { class: 'mono' }, device.path),
        device.managed ? h(NTag, { size: 'tiny', type: 'info', bordered: false }, () => t('host.swap.managed')) : null,
      ]),
  },
  { key: 'kind', title: t('common.field.type'), width: 110, render: (device) => t(`host.swap.kind.${device.kind}`) },
  { key: 'size', title: t('files.column.size'), width: 110, render: (device) => formatBytes(device.size) },
  { key: 'used', title: t('host.swap.used'), width: 110, render: (device) => formatBytes(device.used) },
])
</script>

<template>
  <div class="cards">
    <NCard size="small" :title="t('host.swap.title')">
      <div class="usage">
        <NText>{{ t('host.swap.usage', { used: formatBytes(swap.used), total: formatBytes(swap.total) }) }}</NText>
        <NProgress type="line" :percentage="usedPercent" :show-indicator="false" :height="6" />
      </div>
      <NDataTable v-if="swap.devices.length > 0" :columns="columns" :data="swap.devices" :scroll-x="540" size="small" class="devices" />
      <NText v-else depth="3" tag="p" class="hint">{{ t('host.swap.none') }}</NText>
    </NCard>

    <NCard size="small" :title="t('host.swap.fileTitle')">
      <NAlert v-if="!swap.supported" type="warning" class="block">
        {{ t(`host.swap.unsupported.${swap.reason || 'other'}`) }}
      </NAlert>
      <template v-else>
        <NText depth="3" tag="p" class="hint">{{ managed ? t('host.swap.fileHas', { size: formatBytes(managed.size) }) : t('host.swap.fileNone') }}</NText>
        <div class="inline">
          <NInputNumber v-model:value="sizeMb" :min="SWAP_MIN_MB" :max="SWAP_MAX_MB" :step="512" :precision="0" :disabled="!canManage || busy">
            <template #suffix>{{ t('host.swap.mib') }}</template>
          </NInputNumber>
          <NButton v-if="canManage" type="primary" :disabled="!sizeValid || busy" @click="apply">
            {{ managed ? t('host.swap.resize') : t('host.swap.create') }}
          </NButton>
          <NButton v-if="canManage && managed" type="error" quaternary :disabled="busy" @click="remove">{{ t('host.swap.remove') }}</NButton>
        </div>
      </template>
      <div v-if="jobId" class="job">
        <JobProgress :job-id="jobId" @finished="finished" />
        <NButton size="small" quaternary :disabled="busy" @click="dismiss">{{ t('sites.create.close') }}</NButton>
      </div>
    </NCard>

    <NCard size="small" :title="t('host.swap.swappiness')">
      <NText depth="3" tag="p" class="hint">{{ t('host.swap.swappinessHint') }}</NText>
      <div class="inline">
        <NSlider v-model:value="swappiness" :min="0" :max="100" :step="1" :disabled="!canManage" class="slider" />
        <NInputNumber v-model:value="swappiness" :min="0" :max="100" :precision="0" :disabled="!canManage" class="number" />
        <NButton
          v-if="canManage"
          type="primary"
          :loading="savingSwappiness"
          :disabled="swappiness === swap.swappiness"
          @click="saveSwappiness"
        >
          {{ t('common.action.save') }}
        </NButton>
      </div>
    </NCard>
  </div>
</template>

<style scoped>
.cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(340px, 1fr));
  gap: 12px;
}

.usage {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-block-end: 12px;
}

.devices {
  margin-block-start: 8px;
}

.hint {
  margin: 0 0 10px;
  font-size: 12px;
}

.inline {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.slider {
  flex: 1 1 160px;
}

.number {
  inline-size: 100px;
}

.block {
  margin-block-end: 12px;
}

.job {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 4px;
  margin-block-start: 12px;
}

.job > :first-child {
  align-self: stretch;
}

:deep(.device) {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
}
</style>
