<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import {
  NAlert,
  NButton,
  NDataTable,
  NEmpty,
  NIcon,
  NSpace,
  NTag,
  NText,
  NTooltip,
  type DataTableColumns,
} from 'naive-ui'
import { AddOutline, CloudUploadOutline } from '@vicons/ionicons5'
import { fetchCerts, type CertList, type CertView } from '@/api/certs'
import { caLabel, certDisplayState, certStateTagType, daysLeft, expiryType } from '@/certs/labels'
import { useNow } from '@/composables/useNow'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatRelativeTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'
import IssueCertDialog from './IssueCertDialog.vue'
import UploadCertDialog from './UploadCertDialog.vue'

/** How often the list refreshes while a certificate is being issued. */
const REFRESH_MS = 3000

const emit = defineEmits<{ addDnsAccount: [] }>()

const { t } = useI18n()
const session = useSessionStore()
const now = useNow(60_000)

const list = ref<CertList | null>(null)
const loadError = ref<unknown>(null)
const showIssue = ref(false)
const showUpload = ref(false)

const canManage = computed(() => session.can('certs.manage'))

let timer: ReturnType<typeof setTimeout> | undefined
let unmounted = false

async function load() {
  clearTimeout(timer)
  try {
    list.value = await fetchCerts()
    loadError.value = null
  } catch (err) {
    loadError.value = err
  }
  if (!unmounted && list.value?.certs.some((cert) => cert.state === 'issuing')) {
    timer = setTimeout(() => void load(), REFRESH_MS)
  }
}

onMounted(load)
onBeforeUnmount(() => {
  unmounted = true
  clearTimeout(timer)
})

function sourceLabel(cert: CertView): string {
  return cert.source === 'upload' ? t('certs.source.upload') : caLabel(cert.ca ?? '')
}

function stateCell(cert: CertView) {
  const state = certDisplayState(cert, now.value)
  const tag = h(NTag, { size: 'small', bordered: false, type: certStateTagType(state) }, () => t(`certs.state.${state}`))
  if (!cert.errorCode) return tag
  // Why issuing or the last renewal failed, on hover.
  return h(NTooltip, null, {
    trigger: () => tag,
    default: () => errorText(cert.errorCode!, cert.errorParams ?? {}) ?? cert.errorCode,
  })
}

const columns = computed<DataTableColumns<CertView>>(() => [
  {
    key: 'name',
    title: t('common.field.name'),
    minWidth: 160,
    render: (cert) => h(RouterLink, { to: { name: 'cert', params: { id: cert.id } }, class: 'link' }, () => cert.name),
  },
  {
    key: 'domains',
    title: t('common.field.domains'),
    minWidth: 200,
    render: (cert) =>
      h(NSpace, { size: 6, wrap: false }, () => [
        h('span', { dir: 'ltr' }, cert.domains[0] ?? ''),
        cert.domains.length > 1
          ? h(NText, { depth: 3 }, () => t('sites.list.moreDomains', { count: cert.domains.length - 1 }))
          : null,
      ]),
  },
  {
    key: 'source',
    title: t('certs.column.source'),
    width: 170,
    render: (cert) => h(NTag, { size: 'small', bordered: false }, () => sourceLabel(cert)),
  },
  {
    key: 'notAfter',
    title: t('certs.column.expires'),
    width: 160,
    render: (cert) => {
      if (!cert.notAfter) return '-'
      const type = expiryType(daysLeft(cert, now.value))
      const text = formatRelativeTime(cert.notAfter, now.value)
      return type === 'default' ? text : h(NText, { type }, () => text)
    },
  },
  {
    key: 'autoRenew',
    title: t('certs.column.autoRenew'),
    width: 120,
    render: (cert) =>
      cert.source === 'acme' ? (cert.autoRenew ? t('common.status.on') : t('common.status.off')) : '-',
  },
  {
    key: 'usedBy',
    title: t('certs.column.usedBy'),
    minWidth: 160,
    render: (cert) =>
      h(NSpace, { size: 4 }, () => [
        ...cert.sites.map((code) =>
          h(RouterLink, { to: { name: 'site', params: { code } }, class: 'link' }, () => code),
        ),
        cert.panel ? h(NTag, { size: 'small', type: 'info', bordered: false }, () => t('certs.panelTag')) : null,
      ]),
  },
  { key: 'state', title: t('certs.column.state'), width: 150, render: stateCell },
])
</script>

<template>
  <div>
    <div v-if="canManage" class="toolbar">
      <NButton type="primary" @click="showIssue = true">
        <template #icon>
          <NIcon><AddOutline /></NIcon>
        </template>
        {{ t('certs.issue.title') }}
      </NButton>
      <NButton @click="showUpload = true">
        <template #icon>
          <NIcon><CloudUploadOutline /></NIcon>
        </template>
        {{ t('certs.upload.title') }}
      </NButton>
    </div>

    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NAlert v-else-if="list && !list.statusAvailable" type="info" class="block">{{ t('certs.statusUnavailable') }}</NAlert>

    <NDataTable
      v-if="!list || list.certs.length > 0"
      :columns="columns"
      :data="list?.certs ?? []"
      :loading="!list && !loadError"
      :row-key="(cert: CertView) => cert.id"
      :scroll-x="1100"
      size="small"
    />
    <NEmpty v-else :description="t('certs.empty.title')" class="empty">
      <template #extra>
        <NText depth="3" tag="p" class="empty__text">{{ t('certs.empty.text') }}</NText>
      </template>
    </NEmpty>

    <IssueCertDialog v-model:show="showIssue" @changed="load" @add-dns-account="emit('addDnsAccount')" />
    <UploadCertDialog v-model:show="showUpload" @changed="load" />
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-block-end: 12px;
}

.block {
  margin-block-end: 12px;
}

.empty {
  padding-block: 32px;
}

.empty__text {
  max-inline-size: 520px;
  margin: 0 auto;
  text-align: center;
}

:deep(.link) {
  color: inherit;
  font-weight: 500;
}
</style>
