<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { Copy, LayoutDashboard, Link2, Lock, MonitorCog, RefreshCw, ScrollText, Trash2 } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import { fetchCert, updateCert, type CertView } from '@/api/certs'
import Card from '@/components/common/Card.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import FormField from '@/components/common/FormField.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import Notice from '@/components/common/Notice.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import SettingsDrawer from '@/components/common/SettingsDrawer.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import type { DrawerItem } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { caLabel, certDisplayState } from '@/certs/labels'
import { useClock } from '@/composables/useClock'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { useNavRoutes } from '@/composables/useNavRoutes'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatDate, formatDateTime } from '@/i18n/format'
import { copyText, notifyError, notifySuccess } from '@/lib/notify'
import { expiryStatus } from '@/sites/display'
import { useCertsStore } from '@/stores/certs'
import { useSessionStore } from '@/stores/session'
import { CERT_STATE_TONE, askDeleteCert, askPanelCert, canServePanel, renew } from './actions'

// A certificate's drawer: its facts with the name and auto-renewal
// editable, the chain, and what uses it. Renew and "Use for the panel" sit
// in the header; Delete at the bottom of the sub-menu.
type Section = 'overview' | 'chain' | 'usage'

const { t } = useI18n()
const store = useCertsStore()
const session = useSessionStore()
const navRoutes = useNavRoutes()
const drawer = useDrawerRoute('cert')
const now = useClock()

const cert = ref<CertView | null>(null)
const error = ref<unknown>(null)
const name = ref('')
const savingName = ref(false)
const savingRenew = ref(false)
const nameError = ref<unknown>(null)

const canManage = computed(() => session.can('certs.manage'))

async function load() {
  const id = drawer.id.value
  if (!id) return
  error.value = null
  try {
    const loaded = await fetchCert(id)
    if (drawer.id.value !== id) return
    cert.value = loaded
    name.value = loaded.name
  } catch (err) {
    error.value = err
  }
}

watch(
  () => drawer.id.value,
  (id) => {
    cert.value = null
    nameError.value = null
    if (id) void load()
  },
  { immediate: true },
)

// The list changed this certificate (a job ended): reload its details.
watch(
  () => store.list?.certs.find((c) => c.id === drawer.id.value)?.updatedAt,
  (updated, before) => {
    if (updated !== undefined && before !== undefined && updated !== before) void load()
  },
)

const items = computed<DrawerItem<Section>[]>(() => [
  { value: 'overview', label: t('certs.drawer.overview'), icon: LayoutDashboard },
  { value: 'chain', label: t('certs.detail.chain'), icon: Link2 },
  { value: 'usage', label: t('certs.column.usedBy'), icon: ScrollText },
])

const dangerItems = computed(() =>
  canManage.value ? [{ value: 'delete', label: t('certs.action.delete'), icon: Trash2 }] : [],
)

const section = computed<Section>(() => items.value.find((i) => i.value === drawer.section.value)?.value ?? 'overview')

const state = computed(() => (cert.value ? certDisplayState(cert.value, now.value) : null))
const status = computed(() => (state.value ? { tone: CERT_STATE_TONE[state.value], label: t(`certs.state.${state.value}`) } : null))
const source = computed(() => {
  const c = cert.value
  if (!c) return ''
  return c.source === 'upload' ? t('certs.source.upload') : caLabel(c.ca ?? '')
})

const meta = computed(() => {
  const c = cert.value
  if (!c) return ''
  const parts = [source.value]
  if (c.notAfter && c.state !== 'issuing') parts.push(expiryStatus(c.notAfter, now.value).text)
  parts.push(t('sites.list.domainCount', { count: c.domains.length }, c.domains.length))
  return parts.join(' · ')
})

const failure = computed(() => {
  const c = cert.value
  return c?.errorCode ? (errorText(c.errorCode, c.errorParams ?? {}) ?? c.errorCode) : ''
})

const failureReason = computed(() => {
  const reason = cert.value?.errorParams?.reason
  return typeof reason === 'string' ? reason : ''
})

const dnsAccountName = computed(() => {
  const id = cert.value?.dnsAccount
  if (!id) return ''
  return store.accounts?.find((a) => a.id === id)?.name ?? id
})

async function saveChanges(changes: { name?: string; autoRenew?: boolean }) {
  const current = cert.value
  if (!current) return
  try {
    const updated = await updateCert(current.id, {
      name: changes.name ?? current.name,
      autoRenew: changes.autoRenew ?? current.autoRenew,
    })
    cert.value = { ...current, ...updated, chain: current.chain }
    name.value = cert.value.name
    notifySuccess(t('certs.drawer.saved'))
    void store.load()
    return true
  } catch (err) {
    if (!isStepUpRequired(err)) throw err
    return false
  }
}

async function saveName() {
  const trimmed = name.value.trim()
  if (!trimmed || trimmed === cert.value?.name) return
  savingName.value = true
  nameError.value = null
  try {
    await saveChanges({ name: trimmed })
  } catch (err) {
    nameError.value = err
  } finally {
    savingName.value = false
  }
}

async function setAutoRenew(value: boolean) {
  savingRenew.value = true
  try {
    await saveChanges({ autoRenew: value })
  } catch (err) {
    notifyError(err)
  } finally {
    savingRenew.value = false
  }
}

const autoRenew = computed({
  get: () => cert.value?.autoRenew === true,
  set: (value: boolean) => void setAutoRenew(value),
})

async function onDanger() {
  const current = cert.value
  if (current && (await askDeleteCert(current))) drawer.close()
}

const inUse = computed(() => (cert.value ? cert.value.sites.length > 0 || cert.value.panel : false))
</script>

<template>
  <SettingsDrawer
    :open="drawer.id.value !== null"
    :title="cert?.name ?? ''"
    :icon="Lock"
    :status="status"
    :meta="meta"
    :items="items"
    :danger-items="dangerItems"
    :section="section"
    :loading="!cert && !error"
    :error="cert ? null : error"
    @close="drawer.close()"
    @update:section="drawer.setSection"
    @danger="onDanger"
    @retry="load"
  >
    <template v-if="cert && canManage" #actions>
      <Button
        v-if="cert.source === 'acme'"
        variant="outline"
        size="sm"
        class="max-md:h-11"
        :disabled="cert.state === 'issuing'"
        @click="renew(cert)"
      >
        <RefreshCw aria-hidden="true" />
        {{ t('certs.detail.renew') }}
      </Button>
      <Button
        v-if="!cert.panel && canServePanel(cert, now)"
        variant="outline"
        size="sm"
        class="max-md:h-11"
        @click="askPanelCert(cert)"
      >
        <MonitorCog aria-hidden="true" />
        {{ t('certs.action.usePanel') }}
      </Button>
      <Button v-else-if="cert.panel" variant="outline" size="sm" class="max-md:h-11" @click="askPanelCert(null, cert)">
        <MonitorCog aria-hidden="true" />
        {{ t('certs.action.stopPanel') }}
      </Button>
    </template>

    <template v-if="cert">
      <template v-if="section === 'overview'">
        <Notice v-if="cert.errorCode" :tone="cert.state === 'failed' ? 'destructive' : 'warning'">
          {{ cert.state === 'failed' ? t('certs.detail.failed') : t('certs.detail.renewalFailed') }}: {{ failure }}
          <template v-if="cert.lastJob" #action>
            <RouterLink :to="{ name: 'job', params: { id: cert.lastJob } }" class="text-current underline underline-offset-2">
              {{ t('common.dock.viewLog') }}
            </RouterLink>
          </template>
        </Notice>
        <pre
          v-if="failureReason"
          class="m-0 overflow-x-auto rounded-md bg-muted px-4 py-3 font-mono text-xs whitespace-pre-wrap [overflow-wrap:anywhere]"
          dir="ltr"
        >{{ failureReason }}</pre>

        <Card :title="t('certs.drawer.facts')" :level="3" flush>
          <KeyValueList>
            <KeyValueRow :label="t('certs.column.state')">
              <StatusDot v-if="state" :tone="CERT_STATE_TONE[state]">{{ t(`certs.state.${state}`) }}</StatusDot>
            </KeyValueRow>
            <KeyValueRow :label="t('common.field.domains')" mono>
              <span v-for="domain in cert.domains" :key="domain" dir="ltr">{{ domain }}</span>
            </KeyValueRow>
            <KeyValueRow :label="t('certs.column.source')">
              {{ source }}
              <span v-if="cert.challenge" class="text-muted-foreground">
                · {{ t(cert.challenge === 'dns-01' ? 'certs.challenge.dns01' : 'certs.challenge.http01') }}
              </span>
              <span v-if="dnsAccountName" class="text-muted-foreground">· {{ dnsAccountName }}</span>
            </KeyValueRow>
            <KeyValueRow v-if="cert.issuer" :label="t('certs.detail.issuer')"><span dir="ltr">{{ cert.issuer }}</span></KeyValueRow>
            <KeyValueRow v-if="cert.notBefore && cert.notAfter" :label="t('certs.detail.validity')">
              <span>{{ formatDate(cert.notBefore) }} – {{ formatDate(cert.notAfter) }}</span>
              <StatusPill :tone="expiryStatus(cert.notAfter, now).tone">{{ expiryStatus(cert.notAfter, now).text }}</StatusPill>
            </KeyValueRow>
            <KeyValueRow v-if="cert.keyType" :label="t('common.field.keyType')">{{ t(`certs.keyType.${cert.keyType}`) }}</KeyValueRow>
            <KeyValueRow v-if="cert.serial" :label="t('certs.detail.serial')" mono>
              <span dir="ltr" class="break-all">{{ cert.serial }}</span>
            </KeyValueRow>
            <KeyValueRow v-if="cert.fingerprint" :label="t('certs.detail.fingerprint')" mono>
              <span dir="ltr" class="break-all">{{ cert.fingerprint }}</span>
              <Button
                variant="ghost"
                size="icon-sm"
                :aria-label="t('certs.drawer.copyFingerprint')"
                :title="t('certs.drawer.copyFingerprint')"
                @click="copyText(cert.fingerprint ?? '')"
              >
                <Copy aria-hidden="true" />
              </Button>
            </KeyValueRow>
            <KeyValueRow :label="t('sites.overview.updated')">
              <RelativeTime :value="cert.updatedAt" />
              <RouterLink
                v-if="cert.lastJob"
                :to="{ name: 'job', params: { id: cert.lastJob } }"
                class="text-detail text-link underline-offset-2 hover:underline"
              >
                {{ t('sites.overview.viewChange') }}
              </RouterLink>
            </KeyValueRow>
          </KeyValueList>
        </Card>

        <Card :title="t('certs.drawer.settings')" :level="3">
          <form class="flex flex-col gap-5" novalidate @submit.prevent="saveName">
            <div class="flex flex-wrap items-end gap-2">
              <FormField id="cert-name" :label="t('common.field.name')" class="min-w-0 flex-[1_1_240px]">
                <template #default="field">
                  <Input v-bind="field" v-model="name" maxlength="100" autocomplete="off" :disabled="!canManage || savingName" />
                </template>
              </FormField>
              <Button
                v-if="canManage"
                type="submit"
                variant="outline"
                class="max-md:h-11"
                :disabled="savingName || !name.trim() || name.trim() === cert.name"
              >
                {{ t('certs.detail.rename') }}
              </Button>
            </div>
            <Notice v-if="nameError" tone="destructive">{{ errorMessage(nameError) }}</Notice>
            <SwitchField
              v-if="cert.source === 'acme'"
              v-model="autoRenew"
              :label="t('certs.issue.autoRenew')"
              :hint="t('certs.drawer.autoRenewHint')"
              :disabled="!canManage || savingRenew"
            />
          </form>
        </Card>
      </template>

      <template v-else-if="section === 'chain'">
        <Card v-if="cert.chain && cert.chain.length > 0" :level="3" flush>
          <ol class="m-0 list-none divide-y p-0">
            <li v-for="(link, index) in cert.chain" :key="index" class="flex flex-col gap-0.5 px-4 py-3 md:px-[18px]">
              <span class="flex items-center gap-2">
                <StatusPill :tone="index === 0 ? 'progress' : 'neutral'">
                  {{ index === 0 ? t('certs.drawer.leaf') : t('certs.drawer.intermediate', { n: index }) }}
                </StatusPill>
                <span class="min-w-0 font-medium [overflow-wrap:anywhere]" dir="ltr">{{ link.subject }}</span>
              </span>
              <span class="text-xs text-muted-foreground [overflow-wrap:anywhere]" dir="ltr">
                {{ t('certs.detail.chainIssuer', { issuer: link.issuer, date: formatDateTime(link.notAfter) }) }}
              </span>
            </li>
          </ol>
        </Card>
        <EmptyState v-else :icon="Link2" :title="t('certs.drawer.noChain')" />
      </template>

      <template v-else-if="section === 'usage'">
        <Card :level="3" flush>
          <ul v-if="inUse" class="m-0 list-none divide-y p-0">
            <li v-for="code in cert.sites" :key="code" class="flex items-center gap-3 px-4 py-3 md:px-[18px]">
              <span class="min-w-0 flex-1 font-mono text-[13px]">{{ code }}</span>
              <RouterLink
                v-if="navRoutes.has('sites')"
                :to="{ name: 'sites', query: { open: `site:${code}`, section: 'https' } }"
                class="text-detail font-medium text-link underline-offset-2 hover:underline"
              >
                {{ t('certs.drawer.openSite') }}
              </RouterLink>
            </li>
            <li v-if="cert.panel" class="flex items-center gap-3 px-4 py-3 md:px-[18px]">
              <span class="min-w-0 flex-1">{{ t('certs.drawer.panelUses') }}</span>
              <RouterLink
                v-if="session.can('settings.read')"
                :to="{ name: 'settings-panel' }"
                class="text-detail font-medium text-link underline-offset-2 hover:underline">
                {{ t('common.nav.settings') }}
              </RouterLink>
            </li>
          </ul>
          <EmptyState v-else compact :title="t('certs.detail.unused')" :text="t('certs.drawer.unusedText')" />
        </Card>
        <p v-if="inUse" class="m-0 text-detail text-muted-foreground">{{ t('certs.drawer.deleteNote') }}</p>
      </template>
    </template>
  </SettingsDrawer>
</template>
