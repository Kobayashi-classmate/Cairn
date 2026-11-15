<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NDataTable,
  NEmpty,
  NIcon,
  NModal,
  NSpace,
  NTag,
  NText,
  NTooltip,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { AddOutline } from '@vicons/ionicons5'
import { isStepUpRequired } from '@/api/client'
import { deleteStream, fetchStreams, startStream, stopStream, type StreamView } from '@/api/streams'
import JobProgress from '@/components/JobProgress.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { errorMessage, errorText } from '@/i18n/errors'
import StreamDialog from './StreamDialog.vue'

const REFRESH_MS = 3000

defineProps<{ canManage: boolean }>()

const { t } = useI18n()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()

const streams = ref<StreamView[] | null>(null)
const loadError = ref<unknown>(null)
const busy = ref<string | null>(null)
const showDialog = ref(false)
const editing = ref<StreamView | null>(null)
/** The delete job, followed in a small dialog. */
const deleteJob = ref<string | null>(null)

let timer: ReturnType<typeof setTimeout> | undefined
let unmounted = false

async function load() {
  clearTimeout(timer)
  try {
    streams.value = await fetchStreams()
    loadError.value = null
  } catch (err) {
    loadError.value = err
  }
  if (!unmounted && streams.value?.some((s) => s.state === 'applying')) {
    timer = setTimeout(() => void load(), REFRESH_MS)
  }
}

onMounted(load)
onBeforeUnmount(() => {
  unmounted = true
  clearTimeout(timer)
})

function openCreate() {
  editing.value = null
  showDialog.value = true
}

function openEdit(stream: StreamView) {
  editing.value = stream
  showDialog.value = true
}

async function toggle(stream: StreamView) {
  busy.value = stream.code
  try {
    if (stream.enabled) await stopStream(stream.code)
    else await startStream(stream.code)
    message.info(t(stream.enabled ? 'sites.action.stopping' : 'sites.action.starting', { site: stream.code }))
    await load()
  } catch (err) {
    showError(err)
  } finally {
    busy.value = null
  }
}

async function remove(stream: StreamView) {
  const ok = await confirm({
    title: t('streams.delete.title', { code: stream.code }),
    content: t('streams.delete.content'),
    confirmText: t('common.action.delete'),
    danger: true,
  })
  if (!ok) return
  try {
    // Step-up protected: the client asks for the password and retries.
    deleteJob.value = (await deleteStream(stream.code)).id
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  }
}

function stateOf(stream: StreamView): 'active' | 'stopped' | 'applying' | 'failed' {
  if (stream.state === 'applying' || stream.state === 'failed') return stream.state
  return stream.enabled ? 'active' : 'stopped'
}

const STATE_TAG = { active: 'success', stopped: 'default', applying: 'info', failed: 'error' } as const

const columns = computed<DataTableColumns<StreamView>>(() => [
  { key: 'code', title: t('streams.code'), width: 150 },
  {
    key: 'protocol',
    title: t('common.field.protocol'),
    width: 110,
    render: (s) => h(NTag, { size: 'small', bordered: false }, () => s.protocol.toUpperCase()),
  },
  { key: 'port', title: t('common.field.port'), width: 100 },
  {
    key: 'upstreams',
    title: t('common.field.upstreams'),
    minWidth: 220,
    render: (s) =>
      h(NSpace, { size: 6, wrap: false }, () => [
        h('span', { dir: 'ltr', class: 'mono' }, s.upstreams[0] ?? ''),
        s.upstreams.length > 1
          ? h(NText, { depth: 3 }, () => t('sites.list.moreDomains', { count: s.upstreams.length - 1 }))
          : null,
      ]),
  },
  {
    key: 'balance',
    title: t('common.field.balance'),
    width: 150,
    render: (s) => (s.balance === 'hash' ? t('streams.balance.hash') : t('streams.balance.roundRobin')),
  },
  {
    key: 'state',
    title: t('sites.list.state'),
    width: 130,
    render: (s) => {
      const state = stateOf(s)
      const tag = h(NTag, { size: 'small', bordered: false, type: STATE_TAG[state] }, () => t(`sites.state.${state}`))
      if (state === 'failed' && s.errorCode) {
        return h(NTooltip, null, { trigger: () => tag, default: () => errorText(s.errorCode!) ?? s.errorCode })
      }
      return tag
    },
  },
  {
    key: 'actions',
    title: '',
    width: 230,
    render: (s) =>
      h(NSpace, { size: 4, wrap: false }, () => [
        h(NButton, { size: 'small', quaternary: true, disabled: s.state === 'applying', onClick: () => openEdit(s) }, () =>
          t('common.action.edit'),
        ),
        h(
          NButton,
          {
            size: 'small',
            quaternary: true,
            loading: busy.value === s.code,
            disabled: s.state === 'applying',
            onClick: () => void toggle(s),
          },
          () => (s.enabled ? t('sites.action.stop') : t('sites.action.start')),
        ),
        h(
          NButton,
          { size: 'small', quaternary: true, type: 'error', disabled: s.state === 'applying', onClick: () => void remove(s) },
          () => t('common.action.delete'),
        ),
      ]),
  },
])

</script>

<template>
  <div>
    <NText depth="3" tag="p" class="intro">{{ t('streams.intro') }}</NText>
    <div v-if="canManage" class="toolbar">
      <NButton type="primary" @click="openCreate">
        <template #icon>
          <NIcon><AddOutline /></NIcon>
        </template>
        {{ t('streams.dialog.createTitle') }}
      </NButton>
    </div>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NDataTable
      v-if="!streams || streams.length > 0"
      :columns="canManage ? columns : columns.slice(0, -1)"
      :data="streams ?? []"
      :loading="!streams && !loadError"
      :row-key="(s: StreamView) => s.code"
      :scroll-x="1000"
      size="small"
    />
    <NEmpty v-else :description="t('streams.empty')" class="empty" />

    <StreamDialog v-model:show="showDialog" :stream="editing" @changed="load" />

    <NModal
      :show="deleteJob !== null"
      preset="card"
      :title="t('jobs.kindName.stream.delete')"
      class="job-dialog"
      @update:show="(open: boolean) => !open && (deleteJob = null)"
    >
      <JobProgress v-if="deleteJob" :job-id="deleteJob" @finished="load" />
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
  margin-block-end: 12px;
}

.empty {
  padding-block: 32px;
}

.job-dialog {
  inline-size: min(520px, calc(100vw - 32px));
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
}
</style>
