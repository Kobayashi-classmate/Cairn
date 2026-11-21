<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { Plus, Upload } from '@lucide/vue'
import Notice from '@/components/common/Notice.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import type { PageTab } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { useClock } from '@/composables/useClock'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { usePageTab } from '@/composables/usePageTab'
import { daysUntil } from '@/sites/display'
import { useCertsStore } from '@/stores/certs'
import { useSessionStore } from '@/stores/session'
import AcmeSettingsPanel from './certs/AcmeSettingsPanel.vue'
import CertDrawer from './certs/CertDrawer.vue'
import CertTable from './certs/CertTable.vue'
import DnsAccounts from './certs/DnsAccounts.vue'
import IssueCertSheet from './certs/IssueCertSheet.vue'
import UploadCertSheet from './certs/UploadCertSheet.vue'

// The certificates page ("list" skeleton): certificates, DNS accounts and
// ACME settings in tabs; a certificate's settings open in a drawer.
type Tab = 'certs' | 'dns' | 'acme'
const TABS: readonly Tab[] = ['certs', 'dns', 'acme']

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const store = useCertsStore()
const session = useSessionStore()
const drawer = useDrawerRoute('cert')
const now = useClock()
const { tab, tabLocation } = usePageTab(TABS, 'certs')

const canManage = computed(() => session.can('certs.manage'))
const showIssue = ref(false)
const showUpload = ref(false)
const issueDomains = ref<string[]>([])
const dnsRef = ref<InstanceType<typeof DnsAccounts> | null>(null)

onMounted(() => {
  store.watch()
  void store.load()
  void store.loadAccounts()
})
onBeforeUnmount(() => store.unwatch())

// A site's HTTPS settings link here with ?issue=<names> to open the issue sheet.
watch(
  () => route.query.issue,
  (value) => {
    if (typeof value !== 'string') return
    issueDomains.value = value.split(',').filter(Boolean)
    if (canManage.value) showIssue.value = true
    const query = { ...route.query }
    delete query.issue
    void router.replace({ query })
  },
  { immediate: true },
)

const certs = computed(() => store.list?.certs ?? [])

const tabs = computed<PageTab<Tab>[]>(() => [
  { value: 'certs', label: t('certs.tabs.certs'), count: store.list ? certs.value.length : undefined },
  { value: 'dns', label: t('certs.tabs.dns'), count: store.accounts ? store.accounts.length : undefined },
  { value: 'acme', label: t('certs.tabs.acme') },
])

const status = computed(() => {
  if (!store.list) return ''
  const total = t('certs.status.count', { count: certs.value.length }, certs.value.length)
  const expiring = certs.value.filter(
    (c) => c.state === 'valid' && c.notAfter !== undefined && c.notAfter > now.value && daysUntil(c.notAfter, now.value) <= 14,
  ).length
  const failed = certs.value.filter((c) => c.state === 'failed' || c.errorCode).length
  const parts = [total]
  if (expiring > 0) parts.push(t('certs.status.expiring', { count: expiring }, expiring))
  if (failed > 0) parts.push(t('certs.status.failed', { count: failed }, failed))
  const panel = certs.value.find((c) => c.id === store.list?.panel)
  parts.push(panel ? t('certs.status.panel', { name: panel.name }) : t('certs.status.panelSelfSigned'))
  return parts.join(' · ')
})

function openIssue() {
  issueDomains.value = []
  showIssue.value = true
}

/** The new-account form opens once the DNS accounts tab is shown. */
const pendingDnsCreate = ref(false)
watch(dnsRef, (accounts) => {
  if (accounts && pendingDnsCreate.value) {
    pendingDnsCreate.value = false
    accounts.openCreate()
  }
})

/** From the issue sheet: show the DNS accounts tab with the new-account form. */
async function addDnsAccount() {
  if (dnsRef.value) {
    dnsRef.value.openCreate()
    return
  }
  pendingDnsCreate.value = true
  await router.push(tabLocation('dns'))
}
</script>

<template>
  <div>
    <PageHeader
      :title="t('certs.title')"
      :status="status"
      :status-loading="!store.list && !store.loadError"
      :tabs="tabs"
      :tab="tab"
      :tab-location="tabLocation"
    >
      <template v-if="canManage" #actions>
        <template v-if="tab === 'certs'">
          <Button class="max-md:h-11" variant="outline" @click="showUpload = true">
            <Upload aria-hidden="true" />
            {{ t('certs.upload.title') }}
          </Button>
          <Button class="max-md:h-11" @click="openIssue">
            <Plus aria-hidden="true" />
            {{ t('certs.issue.title') }}
          </Button>
        </template>
        <Button v-else-if="tab === 'dns'" class="max-md:h-11" @click="dnsRef?.openCreate()">
          <Plus aria-hidden="true" />
          {{ t('dns.dialog.createTitle') }}
        </Button>
      </template>
    </PageHeader>

    <div v-if="tab === 'certs'" class="flex flex-col gap-4">
      <Notice v-if="store.list && !store.list.statusAvailable" tone="info">{{ t('certs.statusUnavailable') }}</Notice>
      <CertTable
        :certs="certs"
        :loading="store.loading || (!store.list && !store.loadError)"
        :error="store.loadError"
        :can-manage="canManage"
        @open="(id: string) => drawer.open(id)"
        @issue="openIssue"
        @retry="store.load()"
      />
    </div>
    <DnsAccounts v-else-if="tab === 'dns'" ref="dnsRef" />
    <AcmeSettingsPanel v-else />

    <CertDrawer />
    <IssueCertSheet v-model:open="showIssue" :initial-domains="issueDomains" @add-dns-account="addDnsAccount" />
    <UploadCertSheet v-model:open="showUpload" />
  </div>
</template>
