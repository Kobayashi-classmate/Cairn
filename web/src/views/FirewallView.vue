<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Firewall: port rules and trusted or blocked addresses. Every change must
// be confirmed from a new connection within a minute, or it is rolled back
// (useFirewallConfirm), so a change that cuts the panel off undoes itself.
import { computed, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NCollapse,
  NCollapseItem,
  NDataTable,
  NEmpty,
  NH2,
  NIcon,
  NSpace,
  NSpin,
  NSwitch,
  NTabPane,
  NTabs,
  NTag,
  NText,
  NTooltip,
  type DataTableColumns,
} from 'naive-ui'
import { AddOutline, LockClosedOutline } from '@vicons/ionicons5'
import { isStepUpRequired } from '@/api/client'
import {
  deleteAddress,
  deleteRule,
  disableFirewall,
  enableFirewall,
  fetchFirewall,
  setPingBlocked,
  type ChangeResult,
  type FirewallAddress,
  type FirewallRule,
  type FirewallView,
} from '@/api/firewall'
import type { Job } from '@/api/jobs'
import JobProgress from '@/components/JobProgress.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { useFirewallConfirm } from '@/composables/useFirewallConfirm'
import { useJobRun } from '@/composables/useJobRun'
import { useNow } from '@/composables/useNow'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'
import AddressDialog from './firewall/AddressDialog.vue'
import ConfirmBar from './firewall/ConfirmBar.vue'
import { isProtected, ownerLabel } from './firewall/owners'
import RuleDialog from './firewall/RuleDialog.vue'

/** A rollback is worth mentioning for a day. */
const ROLLBACK_NOTICE_SECONDS = 24 * 3600

const { t } = useI18n()
const confirm = useConfirm()
const showError = useErrorToast()
const session = useSessionStore()
const now = useNow(1000)

const view = ref<FirewallView | null>(null)
const loading = ref(false)
const loadError = ref<unknown>(null)
const tab = ref('rules')
const showRule = ref(false)
const editingRule = ref<FirewallRule | null>(null)
const showAddress = ref(false)
const working = ref(false)

const canManage = computed(() => session.can('firewall.manage'))

async function load() {
  loading.value = true
  try {
    view.value = await fetchFirewall()
    loadError.value = null
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

onMounted(load)

const changes = useFirewallConfirm(() => void load())

/** Changes are possible: allowed, nothing waiting, no second firewall in the way. */
const writable = computed(
  () => canManage.value && !!view.value && view.value.systemd && !view.value.conflict && !changes.checking.value && !working.value,
)
const ruleWritable = computed(() => writable.value && !!view.value?.enabled)

/** A change made elsewhere (another tab) that waits for confirmation. */
const foreignPending = computed(() => (changes.checking.value ? null : (view.value?.pending ?? null)))
const recentRollback = computed(() => {
  const rollback = view.value?.lastRollback
  return rollback && now.value - rollback.at < ROLLBACK_NOTICE_SECONDS ? rollback : null
})

/** Starts confirming a change the server just made. */
function track(result: ChangeResult) {
  if (result.pending) void changes.confirm(result.pending)
  else void load()
}

async function change(start: () => Promise<ChangeResult>) {
  working.value = true
  try {
    track(await start())
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  } finally {
    working.value = false
  }
}

// Enabling and disabling are jobs. Enabling leaves a change to confirm:
// once the job is done, the pending change of kind "enable" is the one it made.
const { jobId, busy, run, finished, dismiss } = useJobRun(async (job: Job) => {
  await load()
  const waiting = view.value?.pending
  if (job.kind === 'firewall.enable' && job.state === 'succeeded' && waiting?.kind === 'enable') void changes.confirm(waiting)
})

async function enable() {
  const current = view.value
  if (!current) return
  const ok = await confirm({
    title: t('firewall.enable.title'),
    content: t('firewall.enable.text', {
      backend: current.backend,
      ssh: current.sshPorts.map((port) => `${port}/tcp`).join(', '),
      panel: `${current.panelPort}/tcp`,
    }),
    confirmText: t('firewall.enable.submit'),
  })
  // Step-up protected: the client asks for the password and retries.
  if (ok) void run(enableFirewall)
}

async function disable() {
  const ok = await confirm({
    title: t('firewall.disable.title'),
    content: t('firewall.disable.text'),
    confirmText: t('firewall.disable.submit'),
    danger: true,
  })
  if (ok) void run(disableFirewall)
}

function reapply() {
  void run(enableFirewall)
}

function togglePing(block: boolean) {
  void change(() => setPingBlocked(block))
}

function openRule(rule: FirewallRule | null) {
  editingRule.value = rule
  showRule.value = true
}

async function removeRule(rule: FirewallRule) {
  const ok = await confirm({
    title: t('firewall.rule.deleteTitle'),
    content: t('firewall.rule.deleteText', { ports: rule.ports.join(', ') }),
    confirmText: t('common.action.delete'),
    danger: true,
  })
  if (ok) void change(() => deleteRule(rule.id))
}

async function removeAddress(address: FirewallAddress) {
  const ok = await confirm({
    title: t('firewall.address.deleteTitle', { address: address.address }),
    content: address.action === 'allow' ? t('firewall.address.deleteAllowText') : t('firewall.address.deleteDenyText'),
    confirmText: t('common.action.delete'),
    danger: true,
  })
  if (ok) void change(() => deleteAddress(address.id))
}

function actionTag(action: string) {
  return h(NTag, { size: 'small', type: action === 'allow' ? 'success' : 'error', bordered: false }, () => t(`firewall.action.${action}`))
}

function ownerCell(rule: FirewallRule) {
  if (!rule.owner) return null
  if (isProtected(rule.owner)) {
    return h(NTooltip, null, {
      trigger: () =>
        h('span', { class: 'owner' }, [h(NIcon, { size: 14 }, () => h(LockClosedOutline)), h('span', null, ownerLabel(rule.owner))]),
      default: () => t('firewall.rule.protected'),
    })
  }
  return h(NTag, { size: 'small', type: 'info', bordered: false }, () => ownerLabel(rule.owner))
}

const ruleColumns = computed<DataTableColumns<FirewallRule>>(() => [
  { key: 'action', title: t('common.field.action'), width: 90, render: (rule) => actionTag(rule.action) },
  { key: 'protocol', title: t('common.field.protocol'), width: 100, render: (rule) => t(`firewall.protocol.${rule.protocol}`) },
  { key: 'ports', title: t('common.field.ports'), minWidth: 140, render: (rule) => h('span', { class: 'mono' }, rule.ports.join(', ')) },
  {
    key: 'sources',
    title: t('common.field.sources'),
    minWidth: 180,
    render: (rule) =>
      rule.sources.length === 0
        ? h(NText, { depth: 3 }, () => t('firewall.anywhere'))
        : h(NSpace, { size: 4 }, () => rule.sources.map((source) => h(NTag, { size: 'small', bordered: false, class: 'mono' }, () => source))),
  },
  { key: 'note', title: t('common.field.note'), minWidth: 140, render: (rule) => rule.note || '' },
  { key: 'owner', title: t('firewall.rule.owner'), width: 190, render: ownerCell },
  {
    key: 'actions',
    title: '',
    width: 150,
    render: (rule) => {
      if (!canManage.value || isProtected(rule.owner)) return null
      return h(NSpace, { size: 4, wrap: false }, () => [
        h(NButton, { size: 'small', quaternary: true, disabled: !ruleWritable.value, onClick: () => openRule(rule) }, () =>
          rule.owner ? t('firewall.rule.editSources') : t('common.action.edit'),
        ),
        rule.owner
          ? null
          : h(
              NButton,
              { size: 'small', quaternary: true, type: 'error', disabled: !ruleWritable.value, onClick: () => void removeRule(rule) },
              () => t('common.action.delete'),
            ),
      ])
    },
  },
])

const addressColumns = computed<DataTableColumns<FirewallAddress>>(() => [
  { key: 'address', title: t('common.field.address'), minWidth: 200, render: (entry) => h('span', { class: 'mono' }, entry.address) },
  {
    key: 'action',
    title: t('common.field.action'),
    width: 130,
    render: (entry) =>
      h(NTag, { size: 'small', type: entry.action === 'allow' ? 'success' : 'error', bordered: false }, () =>
        entry.action === 'allow' ? t('firewall.address.allow') : t('firewall.address.deny'),
      ),
  },
  { key: 'note', title: t('common.field.note'), minWidth: 160, render: (entry) => entry.note || '' },
  {
    key: 'actions',
    title: '',
    width: 100,
    render: (entry) =>
      canManage.value
        ? h(
            NButton,
            { size: 'small', quaternary: true, type: 'error', disabled: !ruleWritable.value, onClick: () => void removeAddress(entry) },
            () => t('common.action.delete'),
          )
        : null,
  },
])
</script>

<template>
  <div class="firewall">
    <NH2 class="firewall__title">{{ t('firewall.title') }}</NH2>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>

    <NSpin v-if="!view && !loadError" class="wait" />
    <template v-else-if="view">
      <NAlert v-if="!view.systemd" type="warning" class="block">{{ t('firewall.noSystemd') }}</NAlert>
      <NAlert v-if="view.conflict" type="error" class="block">{{ t('firewall.conflict', { conflict: view.conflict }) }}</NAlert>
      <NAlert v-if="view.enabled && !view.loaded" type="warning" class="block">
        {{ t('firewall.notLoaded') }}
        <NButton v-if="canManage" size="small" class="block__action" :disabled="!writable || busy" @click="reapply">
          {{ t('firewall.reapply') }}
        </NButton>
      </NAlert>
      <NAlert v-if="recentRollback" type="warning" class="block">
        {{ t('firewall.rolledBack', { time: formatDateTime(recentRollback.at) }) }}
      </NAlert>
      <ConfirmBar v-if="changes.checking.value" :remaining="changes.remaining.value" />
      <NAlert v-else-if="foreignPending" type="warning" class="block">
        {{ t('firewall.foreignPending', { seconds: Math.max(0, foreignPending.expires - now) }) }}
        <NButton
          v-if="canManage"
          size="small"
          type="primary"
          class="block__action"
          @click="changes.confirm(foreignPending)"
        >
          {{ t('firewall.keep') }}
        </NButton>
      </NAlert>

      <NCard size="small" class="status">
        <div class="status__row">
          <NTag :type="view.enabled ? (view.loaded ? 'success' : 'warning') : 'default'" :bordered="false">
            {{ view.enabled ? t('firewall.state.enabled') : t('firewall.state.disabled') }}
          </NTag>
          <NText v-if="view.backend" depth="3">
            {{ t('firewall.backend', { backend: view.backend }) }}
            <template v-if="!view.installed">{{ t('firewall.backendMissing') }}</template>
          </NText>
          <NText v-else depth="3">{{ t('firewall.noBackend') }}</NText>
          <NText depth="3">{{ t('firewall.clientIp', { ip: view.clientIp }) }}</NText>
          <span class="status__spacer" />
          <label class="ping">
            <NSwitch
              :value="view.blockPing"
              size="small"
              :disabled="!ruleWritable"
              @update:value="togglePing"
            />
            <span>{{ t('firewall.blockPing') }}</span>
          </label>
          <template v-if="canManage">
            <NButton v-if="!view.enabled" type="primary" size="small" :disabled="!writable || !view.backend || busy" @click="enable">
              {{ t('firewall.enable.submit') }}
            </NButton>
            <NButton v-else size="small" :disabled="!writable || busy" @click="disable">{{ t('firewall.disable.submit') }}</NButton>
          </template>
        </div>
        <NText depth="3" tag="p" class="status__note">
          {{ t('firewall.alwaysOpen', { ssh: view.sshPorts.join(', '), panel: view.panelPort }) }}
        </NText>
        <div v-if="jobId" class="job">
          <JobProgress :job-id="jobId" @finished="finished" />
          <NButton size="small" quaternary :disabled="busy" @click="dismiss">{{ t('sites.create.close') }}</NButton>
        </div>
      </NCard>

      <NTabs v-model:value="tab" type="line">
        <NTabPane name="rules" :tab="t('firewall.tabs.rules')">
          <div v-if="canManage" class="toolbar">
            <NButton type="primary" :disabled="!ruleWritable" @click="openRule(null)">
              <template #icon>
                <NIcon><AddOutline /></NIcon>
              </template>
              {{ t('firewall.rule.createTitle') }}
            </NButton>
            <NText v-if="!view.enabled" depth="3" class="toolbar__hint">{{ t('firewall.enableFirst') }}</NText>
          </div>
          <NText depth="3" tag="p" class="intro">{{ t('firewall.rule.intro') }}</NText>
          <NDataTable
            :columns="ruleColumns"
            :data="view.rules"
            :row-key="(rule: FirewallRule) => rule.id"
            :loading="loading"
            :scroll-x="980"
            size="small"
          />
        </NTabPane>
        <NTabPane name="addresses" :tab="t('firewall.tabs.addresses')">
          <div v-if="canManage" class="toolbar">
            <NButton type="primary" :disabled="!ruleWritable" @click="showAddress = true">
              <template #icon>
                <NIcon><AddOutline /></NIcon>
              </template>
              {{ t('firewall.address.createTitle') }}
            </NButton>
          </div>
          <NText depth="3" tag="p" class="intro">{{ t('firewall.address.intro') }}</NText>
          <NEmpty v-if="view.addresses.length === 0" :description="t('firewall.address.empty')" class="empty" />
          <NDataTable
            v-else
            :columns="addressColumns"
            :data="view.addresses"
            :row-key="(entry: FirewallAddress) => entry.id"
            :loading="loading"
            :scroll-x="640"
            size="small"
          />
        </NTabPane>
      </NTabs>

      <NCollapse v-if="view.external.length > 0" class="external">
        <NCollapseItem :title="t('firewall.external.title', { count: view.external.length })" name="external">
          <NText depth="3" tag="p" class="intro">{{ t('firewall.external.intro') }}</NText>
          <pre class="external__list" dir="ltr">{{ view.external.join('\n') }}</pre>
        </NCollapseItem>
      </NCollapse>
    </template>

    <RuleDialog v-if="view" v-model:show="showRule" :rule="editingRule" :client-ip="view.clientIp" @changed="track" />
    <AddressDialog v-if="view" v-model:show="showAddress" :client-ip="view.clientIp" @changed="track" />
  </div>
</template>

<style scoped>
.firewall {
  max-inline-size: 1300px;
}

.firewall__title {
  margin: 0 0 12px;
}

.wait {
  display: flex;
  justify-content: center;
  padding-block: 64px;
}

.block {
  margin-block-end: 12px;
}

.block__action {
  margin-inline-start: 12px;
}

.status {
  margin-block-end: 12px;
}

.status__row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 16px;
}

.status__spacer {
  flex: 1;
}

.status__note {
  margin: 8px 0 0;
  font-size: 13px;
}

.ping {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}

.toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
  margin-block: 12px;
}

.toolbar__hint {
  font-size: 13px;
}

.intro {
  margin-block: 0 12px;
  font-size: 13px;
}

.empty {
  padding-block: 32px;
}

.external {
  margin-block-start: 16px;
}

.external__list {
  margin: 0;
  padding: 10px;
  overflow-x: auto;
  border-radius: 3px;
  background: rgba(128, 128, 128, 0.1);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
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

:deep(.owner) {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
}
</style>
