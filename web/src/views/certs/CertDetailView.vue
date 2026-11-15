<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NCard, NH2, NIcon, NInput, NSpin, NSwitch, NTag, NText, useMessage } from 'naive-ui'
import { ArrowBackOutline } from '@vicons/ionicons5'
import { isStepUpRequired } from '@/api/client'
import { deleteCert, fetchCert, renewCert, updateCert, type CertView } from '@/api/certs'
import type { Job } from '@/api/jobs'
import { caLabel, certDisplayState, certStateTagType, daysLeft, expiryType } from '@/certs/labels'
import JobProgress from '@/components/JobProgress.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { useNow } from '@/composables/useNow'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatDateTime, formatRelativeTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'

const REFRESH_MS = 3000

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()
const session = useSessionStore()
const now = useNow(60_000)

const id = computed(() => String(route.params.id ?? ''))
const cert = ref<CertView | null>(null)
const loadError = ref<unknown>(null)
const actionJob = ref<string | null>(null)
const deleting = ref(false)
const busy = ref(false)
const editingName = ref(false)
const name = ref('')

const canManage = computed(() => session.can('certs.manage'))
const state = computed(() => (cert.value ? certDisplayState(cert.value, now.value) : null))

let timer: ReturnType<typeof setTimeout> | undefined
let unmounted = false

async function load() {
  clearTimeout(timer)
  try {
    cert.value = await fetchCert(id.value)
    loadError.value = null
  } catch (err) {
    loadError.value = err
  }
  if (!unmounted && cert.value?.state === 'issuing' && !actionJob.value) {
    timer = setTimeout(() => void load(), REFRESH_MS)
  }
}

watch(id, () => {
  cert.value = null
  actionJob.value = null
  void load()
}, { immediate: true })

onBeforeUnmount(() => {
  unmounted = true
  clearTimeout(timer)
})

async function run(action: () => Promise<Job>, isDelete = false) {
  busy.value = true
  try {
    actionJob.value = (await action()).id
    deleting.value = isDelete
  } catch (err) {
    showError(err)
  } finally {
    busy.value = false
  }
}

function renew() {
  const current = cert.value
  if (current) void run(() => renewCert(current.id))
}

async function remove() {
  const current = cert.value
  if (!current) return
  const ok = await confirm({
    title: t('certs.delete.title', { name: current.name }),
    content: t('certs.delete.content'),
    confirmText: t('common.action.delete'),
    danger: true,
  })
  if (ok) void run(() => deleteCert(current.id), true)
}

function onFinished(job: Job) {
  if (deleting.value && job.state === 'succeeded') {
    void router.replace({ name: 'certs' })
    return
  }
  void load()
}

async function save(changes: { name?: string; autoRenew?: boolean }) {
  const current = cert.value
  if (!current) return
  busy.value = true
  try {
    cert.value = {
      ...current,
      ...(await updateCert(current.id, {
        name: changes.name ?? current.name,
        autoRenew: changes.autoRenew ?? current.autoRenew,
      })),
      chain: current.chain,
    }
    editingName.value = false
    message.success(t('settings.panel.saved'))
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  } finally {
    busy.value = false
  }
}

function startRename() {
  name.value = cert.value?.name ?? ''
  editingName.value = true
}

const failure = computed(() => {
  const current = cert.value
  return current?.errorCode ? (errorText(current.errorCode, current.errorParams ?? {}) ?? current.errorCode) : ''
})

const failureReason = computed(() => {
  const reason = cert.value?.errorParams?.reason
  return typeof reason === 'string' ? reason : ''
})

const expiry = computed(() => {
  const current = cert.value
  if (!current?.notAfter) return null
  return { text: formatRelativeTime(current.notAfter, now.value), type: expiryType(daysLeft(current, now.value)) }
})
</script>

<template>
  <div class="cert">
    <RouterLink :to="{ name: 'certs' }" class="cert__back">
      <NIcon><ArrowBackOutline /></NIcon>
      {{ t('certs.title') }}
    </RouterLink>

    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>

    <NSpin :show="!cert && !loadError">
      <template v-if="cert && state">
        <header class="cert__header">
          <template v-if="editingName">
            <NInput v-model:value="name" size="small" class="cert__name-input" :maxlength="100" />
            <NButton size="small" type="primary" :loading="busy" @click="save({ name: name.trim() })">
              {{ t('common.action.save') }}
            </NButton>
            <NButton size="small" @click="editingName = false">{{ t('common.action.cancel') }}</NButton>
          </template>
          <template v-else>
            <NH2 class="cert__title">{{ cert.name }}</NH2>
            <NButton v-if="canManage" size="small" quaternary @click="startRename">{{ t('certs.detail.rename') }}</NButton>
          </template>
          <NTag :type="certStateTagType(state)" :bordered="false">{{ t(`certs.state.${state}`) }}</NTag>
          <span class="cert__spacer" />
          <template v-if="canManage">
            <NButton v-if="cert.source === 'acme'" :disabled="busy || !!actionJob || cert.state === 'issuing'" @click="renew">
              {{ t('certs.detail.renew') }}
            </NButton>
            <NButton type="error" ghost :disabled="busy || !!actionJob" @click="remove">
              {{ t('common.action.delete') }}
            </NButton>
          </template>
        </header>

        <NCard v-if="actionJob" size="small" class="block">
          <JobProgress :job-id="actionJob" @finished="onFinished" />
        </NCard>

        <NAlert
          v-if="cert.errorCode"
          :type="cert.state === 'failed' ? 'error' : 'warning'"
          :title="cert.state === 'failed' ? t('certs.detail.failed') : t('certs.detail.renewalFailed')"
          class="block"
        >
          <div>{{ failure }}</div>
          <pre v-if="failureReason" class="reason" dir="ltr">{{ failureReason }}</pre>
        </NAlert>

        <NCard size="small" class="block">
          <dl class="facts">
            <dt>{{ t('common.field.domains') }}</dt>
            <dd class="mono" dir="ltr">{{ cert.domains.join(', ') }}</dd>
            <dt>{{ t('certs.column.source') }}</dt>
            <dd>
              {{ cert.source === 'upload' ? t('certs.source.upload') : caLabel(cert.ca ?? '') }}
              <template v-if="cert.challenge"> · {{ t(cert.challenge === 'dns-01' ? 'certs.challenge.dns01' : 'certs.challenge.http01') }}</template>
            </dd>
            <template v-if="cert.notBefore && cert.notAfter">
              <dt>{{ t('certs.detail.validity') }}</dt>
              <dd>
                {{ formatDateTime(cert.notBefore) }} – {{ formatDateTime(cert.notAfter) }}
                <NText v-if="expiry" :type="expiry.type === 'default' ? undefined : expiry.type" class="expiry">
                  ({{ expiry.text }})
                </NText>
              </dd>
            </template>
            <template v-if="cert.source === 'acme'">
              <dt>{{ t('certs.column.autoRenew') }}</dt>
              <dd>
                <NSwitch
                  :value="cert.autoRenew"
                  :disabled="!canManage || busy"
                  size="small"
                  @update:value="(value: boolean) => save({ autoRenew: value })"
                />
              </dd>
            </template>
            <template v-if="cert.keyType">
              <dt>{{ t('common.field.keyType') }}</dt>
              <dd>{{ t(`certs.keyType.${cert.keyType}`) }}</dd>
            </template>
            <template v-if="cert.issuer">
              <dt>{{ t('certs.detail.issuer') }}</dt>
              <dd dir="ltr">{{ cert.issuer }}</dd>
            </template>
            <template v-if="cert.serial">
              <dt>{{ t('certs.detail.serial') }}</dt>
              <dd class="mono" dir="ltr">{{ cert.serial }}</dd>
            </template>
            <template v-if="cert.fingerprint">
              <dt>{{ t('certs.detail.fingerprint') }}</dt>
              <dd class="mono" dir="ltr">{{ cert.fingerprint }}</dd>
            </template>
            <dt>{{ t('certs.column.usedBy') }}</dt>
            <dd>
              <span v-if="cert.sites.length === 0 && !cert.panel">{{ t('certs.detail.unused') }}</span>
              <span class="used">
                <RouterLink v-for="code in cert.sites" :key="code" :to="{ name: 'site', params: { code } }">{{ code }}</RouterLink>
                <NTag v-if="cert.panel" size="small" type="info" :bordered="false">{{ t('certs.panelTag') }}</NTag>
              </span>
            </dd>
            <template v-if="cert.lastJob">
              <dt>{{ t('sites.lastChange') }}</dt>
              <dd>
                <RouterLink :to="{ name: 'job', params: { id: cert.lastJob } }">{{ t('software.viewOutput') }}</RouterLink>
              </dd>
            </template>
          </dl>
        </NCard>

        <NCard v-if="cert.chain?.length" :title="t('certs.detail.chain')" size="small">
          <ol class="chain">
            <li v-for="(link, index) in cert.chain" :key="index">
              <div dir="ltr">{{ link.subject }}</div>
              <NText depth="3" class="small" dir="ltr">
                {{ t('certs.detail.chainIssuer', { issuer: link.issuer, date: formatDateTime(link.notAfter) }) }}
              </NText>
            </li>
          </ol>
        </NCard>
      </template>
    </NSpin>
  </div>
</template>

<style scoped>
.cert {
  max-inline-size: 1000px;
}

.cert__back {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-block-end: 8px;
  color: inherit;
  text-decoration: none;
  opacity: 0.75;
}

.cert__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
  margin-block-end: 16px;
}

.cert__title {
  margin: 0;
}

.cert__name-input {
  inline-size: 260px;
}

.cert__spacer {
  flex: 1;
}

.block {
  margin-block-end: 16px;
}

.facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 8px 20px;
  margin: 0;
}

.facts dt {
  opacity: 0.7;
}

.facts dd {
  margin: 0;
  overflow-wrap: anywhere;
}

.expiry {
  margin-inline-start: 6px;
}

.used {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px 10px;
}

.chain {
  margin: 0;
  padding-inline-start: 1.4em;
}

.chain li {
  margin-block-end: 8px;
}

.small {
  font-size: 12px;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
}

.reason {
  margin: 8px 0 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
