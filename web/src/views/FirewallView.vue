<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Power, PowerOff, Undo2 } from '@lucide/vue'
import { serverClockOffset, serverNow } from '@/api/client'
import {
  CONFIRM_SECONDS,
  FORWARDS_MAX,
  disableFirewall,
  enableFirewall,
  fetchFirewall,
  setPingBlocked,
  type ChangeResult,
  type FirewallForward,
  type FirewallRule,
  type FirewallView,
} from '@/api/firewall'
import type { Job } from '@/api/jobs'
import ErrorState from '@/components/common/ErrorState.vue'
import Notice from '@/components/common/Notice.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import PendingChangeBanner from '@/components/common/PendingChangeBanner.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import type { PageTab } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { confirmAction } from '@/composables/useConfirmDialog'
import { useFirewallConfirm, type ChangeInfo } from '@/composables/useFirewallConfirm'
import { useNow } from '@/composables/useNow'
import { usePageTab } from '@/composables/usePageTab'
import { errorMessage } from '@/i18n/errors'
import { formatWhen } from '@/i18n/format'
import { launchJob } from '@/jobs/dock'
import { notifyError, notifySuccess } from '@/lib/notify'
import type { Tone } from '@/lib/tone'
import { useSessionStore } from '@/stores/session'
import AddressSheet from './firewall/AddressSheet.vue'
import AddressTable from './firewall/AddressTable.vue'
import BasicPanel from './firewall/BasicPanel.vue'
import ForwardSheet from './firewall/ForwardSheet.vue'
import ForwardTable from './firewall/ForwardTable.vue'
import { pendingKindLabel } from './firewall/labels'
import RuleSheet from './firewall/RuleSheet.vue'
import RuleTable from './firewall/RuleTable.vue'

// Firewall ("list" skeleton): port rules, trusted and blocked addresses,
// port forwards and basic settings in four tabs (?tab=). Every change made
// while the firewall is on must be confirmed from a new connection within
// a minute or it is rolled back (useFirewallConfirm); the banner at the top
// follows it through "applied, confirm, saved".
type Tab = 'rules' | 'addresses' | 'forwards' | 'basic'
const TABS: readonly Tab[] = ['rules', 'addresses', 'forwards', 'basic']

/** A rollback is worth mentioning for a day. */
const ROLLBACK_NOTICE_SECONDS = 24 * 3600
/** After a change made elsewhere expires, look again once its fate is known. */
const FOREIGN_SETTLE_MS = 3000

const { t } = useI18n()
const session = useSessionStore()
const now = useNow(1000)
/** The server's time in Unix seconds, ticking every second: pending changes expire on its clock. */
const serverSeconds = computed(() => now.value + serverClockOffset() / 1000)
const { tab, tabLocation } = usePageTab(TABS, 'rules')

const view = ref<FirewallView | null>(null)
const loadError = ref<unknown>(null)
/** A ping change or an undo is being sent. */
const working = ref(false)
/** An enable or disable job runs; the server takes no other change meanwhile. */
const jobRunning = ref(false)

const canManage = computed(() => session.can('firewall.manage'))

let loadSeq = 0
async function load() {
  const mine = ++loadSeq
  try {
    const next = await fetchFirewall()
    if (mine !== loadSeq) return
    view.value = next
    loadError.value = null
  } catch (err) {
    if (mine === loadSeq) loadError.value = err
  }
}

onMounted(load)

const changes = useFirewallConfirm(() => void load())
const { pending, info, abandoned, unconfirmed, kept, busy, remaining } = changes

/** Changes are possible: allowed, nothing waiting, no second firewall in the way. */
const writable = computed(
  () =>
    canManage.value &&
    !!view.value &&
    view.value.systemd &&
    !view.value.conflict &&
    !busy.value &&
    !jobRunning.value &&
    !working.value &&
    !foreignPending.value &&
    !failedRollback.value,
)
/** Rules change only while the firewall is on (as before). */
const ruleWritable = computed(() => writable.value && !!view.value?.enabled)
const canAddForward = computed(() => (view.value?.forwards.length ?? 0) < FORWARDS_MAX)

/**
 * A change the server could not undo: its rules are still in force and
 * nothing else can change until a rollback works.
 */
const failedRollback = computed(() => {
  const waiting = view.value?.pending
  return waiting?.rollbackFailed && !busy.value ? waiting : null
})

/** A change made elsewhere (another tab, the API) that waits for confirmation. */
const foreignPending = computed(() => {
  const waiting = view.value?.pending
  if (!waiting || busy.value || waiting.rollbackFailed) return null
  return waiting.expires > serverSeconds.value ? waiting : null
})

// A change made elsewhere is kept or rolled back when it expires: look again then.
let foreignTimer: ReturnType<typeof setTimeout> | undefined
watch(
  () => view.value?.pending?.id,
  () => {
    clearTimeout(foreignTimer)
    const waiting = view.value?.pending
    if (!waiting || busy.value) return
    const wait = Math.max(0, waiting.expires * 1000 - serverNow()) + FOREIGN_SETTLE_MS
    foreignTimer = setTimeout(() => void load(), wait)
  },
)
onBeforeUnmount(() => clearTimeout(foreignTimer))

const recentRollback = computed(() => {
  const rollback = view.value?.lastRollback
  return rollback && serverSeconds.value - rollback.at < ROLLBACK_NOTICE_SECONDS ? rollback : null
})

/** Starts confirming a change the server just made. */
function track(result: ChangeResult, about: ChangeInfo = {}) {
  if (result.pending) {
    void changes.confirm(result.pending, about)
  } else {
    // The firewall is off: the change was only stored.
    notifySuccess(t('firewall.saved'))
    void load()
  }
}

async function change(start: () => Promise<ChangeResult>, about: ChangeInfo) {
  working.value = true
  try {
    track(await start(), about)
  } catch (err) {
    notifyError(err)
  } finally {
    working.value = false
  }
}

function togglePing(block: boolean) {
  void change(() => setPingBlocked(block), {
    summary: block ? t('firewall.summary.pingBlock') : t('firewall.summary.pingAllow'),
    undo: () => setPingBlocked(!block),
  })
}

function undo() {
  const last = kept.value
  const revert = last?.undo
  if (!last || !revert) return
  void change(revert, { summary: t('firewall.summary.undo', { summary: last.summary ?? pendingKindLabel(last.kind) }) })
}

/** Confirms a change no longer confirmed after all, while there is time. */
function keepAbandoned() {
  if (abandoned.value) void changes.confirm(abandoned.value, info.value ?? {})
}

/** Confirms a change made elsewhere from this browser. */
function keepForeign() {
  if (foreignPending.value) void changes.confirm(foreignPending.value)
}

// Enabling and disabling are jobs. Enabling leaves a change to confirm:
// the finished job names it (params.pending).
async function startEnable(rethrow: boolean) {
  const job = await launchJob(enableFirewall, {
    rethrow,
    onFinished: async (done: Job) => {
      jobRunning.value = false
      await load()
      const made = done.params.pending
      const waiting = view.value?.pending
      if (done.state === 'succeeded' && waiting && typeof made === 'string' && waiting.id === made) {
        void changes.confirm(waiting, { summary: t('firewall.summary.enable') })
      }
    },
  })
  if (job) jobRunning.value = true
}

function enable() {
  const current = view.value
  if (!current) return
  void confirmAction({
    title: t('firewall.enable.title'),
    description:
      t('firewall.enable.text', {
        backend: current.backend,
        ssh: current.sshPorts.map((port) => `${port}/tcp`).join(', '),
        panel: `${current.panelPort}/tcp`,
      }) + (current.sshUnread ? ` ${t('firewall.sshUnread')}` : ''),
    confirmLabel: t('firewall.enable.submit'),
    // Step-up protected: the client asks for the password and retries.
    run: () => startEnable(true),
  })
}

function disable() {
  void confirmAction({
    title: t('firewall.disable.title'),
    description: t('firewall.disable.text'),
    confirmLabel: t('firewall.disable.submit'),
    destructive: true,
    run: async () => {
      const job = await launchJob(disableFirewall, {
        rethrow: true,
        onFinished: () => {
          jobRunning.value = false
          void load()
        },
      })
      if (job) jobRunning.value = true
    },
  })
}

/** The rules are not in effect: apply them again (needs confirming like turning on). */
function reapply() {
  void startEnable(false)
}

// Sheets.
const showRule = ref(false)
const editingRule = ref<FirewallRule | null>(null)
const showAddress = ref(false)
const showForward = ref(false)
const editingForward = ref<FirewallForward | null>(null)

function openRule(rule: FirewallRule | null) {
  editingRule.value = rule
  showRule.value = true
}

function openForward(forward: FirewallForward | null) {
  editingForward.value = forward
  showForward.value = true
}

const keptPorts = computed(() => (view.value ? [...view.value.sshPorts, view.value.panelPort] : []))

const tabs = computed<PageTab<Tab>[]>(() => [
  { value: 'rules', label: t('firewall.tabs.rules'), count: view.value?.rules.length },
  { value: 'addresses', label: t('firewall.tabs.addresses'), count: view.value?.addresses.length },
  { value: 'forwards', label: t('firewall.tabs.forwards'), count: view.value?.forwards.length },
  { value: 'basic', label: t('firewall.tabs.basic') },
])

const state = computed<{ tone: Tone; text: string } | null>(() => {
  const current = view.value
  if (!current) return null
  if (!current.enabled) return { tone: 'neutral', text: t('firewall.state.disabled') }
  return current.loaded ? { tone: 'success', text: t('firewall.state.enabled') } : { tone: 'warning', text: t('firewall.state.notLoaded') }
})

/** The primary button of the header: add something in the current tab. */
const addAction = computed(() => {
  if (!canManage.value || !view.value?.enabled) return null
  if (tab.value === 'rules') return { label: t('firewall.rule.add'), run: () => openRule(null), disabled: !ruleWritable.value }
  if (tab.value === 'addresses') return { label: t('firewall.address.add'), run: () => (showAddress.value = true), disabled: !ruleWritable.value }
  if (tab.value === 'forwards') {
    return { label: t('firewall.forward.add'), run: () => openForward(null), disabled: !ruleWritable.value || !canAddForward.value }
  }
  return null
})

// The banner.
const steps = computed(() => [t('firewall.pending.step.applied'), t('firewall.pending.step.confirm'), t('firewall.pending.step.saved')])
const timeLeft = computed(() => (remaining.value / CONFIRM_SECONDS) * 100)
const foreignLeft = computed(() => (foreignPending.value ? Math.max(0, Math.ceil(foreignPending.value.expires - serverSeconds.value)) : 0))
</script>

<template>
  <div>
    <PageHeader :title="t('firewall.title')" :status-loading="!view && !loadError" :tabs="tabs" :tab="tab" :tab-location="tabLocation">
      <template v-if="view" #status>
        <span class="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
          <StatusDot v-if="state" :tone="state.tone" class="text-foreground">{{ state.text }}</StatusDot>
          <span aria-hidden="true">·</span>
          <span v-if="view.backend" class="font-mono text-[12.5px]">{{ view.backend }}</span>
          <span v-else>{{ t('firewall.status.noBackend') }}</span>
          <span aria-hidden="true">·</span>
          <span>{{ t('firewall.status.clientIp') }} <span class="font-mono text-[12.5px]" dir="ltr">{{ view.clientIp }}</span></span>
        </span>
      </template>
      <template v-if="canManage && view" #actions>
        <template v-if="view.enabled">
          <Button variant="outline" class="max-md:h-11" :disabled="!writable" @click="disable">
            <PowerOff aria-hidden="true" />
            {{ t('firewall.disable.submit') }}
          </Button>
          <Button v-if="addAction" class="max-md:h-11" :disabled="addAction.disabled" @click="addAction.run">
            <Plus aria-hidden="true" />
            {{ addAction.label }}
          </Button>
        </template>
        <Button v-else class="max-md:h-11" :disabled="!writable || !view.backend" @click="enable">
          <Power aria-hidden="true" />
          {{ t('firewall.enable.submit') }}
        </Button>
      </template>
    </PageHeader>

    <div class="flex flex-col gap-4">
      <ErrorState v-if="loadError && !view" :error="loadError" class="rounded-lg border bg-card" @retry="load" />

      <template v-else>
        <template v-if="view">
          <Notice v-if="!view.systemd" tone="warning">{{ t('firewall.noSystemd') }}</Notice>
          <Notice v-if="view.conflict" tone="destructive">{{ t('firewall.conflict', { conflict: view.conflict }) }}</Notice>
          <Notice v-if="!view.backend" tone="warning">{{ t('firewall.noBackend') }}</Notice>
          <Notice v-if="view.enabled && !view.loaded" tone="warning">
            {{ t('firewall.notLoaded') }}
            <template v-if="canManage" #action>
              <button type="button" class="underline underline-offset-2 hover:no-underline disabled:opacity-50" :disabled="!writable" @click="reapply">
                {{ t('firewall.reapply') }}
              </button>
            </template>
          </Notice>

          <Notice v-if="failedRollback" tone="destructive">
            {{
              t('firewall.rollbackFailed', {
                change: pendingKindLabel(failedRollback.kind),
                detail: failedRollback.rollbackFailed?.detail ?? '',
              })
            }}
            <template v-if="canManage" #action>
              <button
                type="button"
                class="underline underline-offset-2 hover:no-underline disabled:opacity-50"
                :disabled="working || jobRunning"
                @click="changes.rollBackForeign(failedRollback)"
              >
                {{ t('firewall.rollbackRetry') }}
              </button>
            </template>
          </Notice>
          <Notice v-if="view.sshUnread" tone="warning">{{ t('firewall.sshUnread') }}</Notice>

          <PendingChangeBanner
            v-if="pending"
            :title="t('firewall.pending.checkingTitle')"
            :steps="steps"
            :current="1"
            :countdown="t('firewall.pending.countdown', { seconds: remaining })"
            :time-left="timeLeft"
          >
            <template #description>
              <span v-if="info?.summary" class="font-medium text-foreground">{{ info.summary }}. </span>
              {{ t('firewall.pending.checkingText') }}
            </template>
            <template #actions>
              <Button variant="outline" size="sm" class="max-md:h-11" @click="changes.abandon()">
                <Undo2 aria-hidden="true" />
                {{ t('firewall.pending.rollBack') }}
              </Button>
            </template>
          </PendingChangeBanner>

          <PendingChangeBanner
            v-else-if="abandoned"
            tone="warning"
            :title="unconfirmed ? t('firewall.pending.unconfirmedTitle') : t('firewall.pending.abandonedTitle')"
            :description="unconfirmed ? t('firewall.pending.unconfirmedText', { error: errorMessage(unconfirmed) }) : t('firewall.pending.abandonedText')"
            :steps="steps"
            :current="1"
            :countdown="t('firewall.pending.abandonedCountdown', { seconds: remaining })"
            :time-left="timeLeft"
          >
            <template v-if="remaining > 2" #actions>
              <Button variant="outline" size="sm" class="max-md:h-11" @click="keepAbandoned">{{ t('firewall.pending.keep') }}</Button>
            </template>
          </PendingChangeBanner>

          <PendingChangeBanner
            v-else-if="foreignPending"
            tone="waiting"
            :title="t('firewall.pending.foreignTitle')"
            :description="t('firewall.pending.foreignText', { change: pendingKindLabel(foreignPending.kind) })"
            :steps="steps"
            :current="1"
            :countdown="t('firewall.pending.countdown', { seconds: foreignLeft })"
            :time-left="(foreignLeft / CONFIRM_SECONDS) * 100"
          >
            <template v-if="canManage" #actions>
              <Button variant="outline" size="sm" class="max-md:h-11" @click="changes.rollBackForeign(foreignPending)">
                <Undo2 aria-hidden="true" />
                {{ t('firewall.pending.rollBack') }}
              </Button>
              <Button size="sm" class="max-md:h-11" @click="keepForeign">{{ t('firewall.pending.keep') }}</Button>
            </template>
          </PendingChangeBanner>

          <PendingChangeBanner
            v-else-if="kept"
            tone="success"
            :title="t('firewall.pending.keptTitle')"
            :description="kept.summary ?? pendingKindLabel(kept.kind)"
            :steps="steps"
            :current="3"
            dismissible
            @dismiss="changes.dismissKept()"
          >
            <template v-if="kept.undo && canManage" #actions>
              <Button variant="outline" size="sm" class="max-md:h-11" :disabled="!writable" @click="undo">
                <Undo2 aria-hidden="true" />
                {{ t('firewall.pending.undo') }}
              </Button>
            </template>
          </PendingChangeBanner>

          <Notice v-if="recentRollback && !busy" tone="warning">
            {{ t('firewall.rolledBack', { time: formatWhen(recentRollback.at, serverSeconds) }) }}
          </Notice>

          <p v-if="canManage && !view.enabled && tab !== 'basic'" class="m-0 text-detail text-muted-foreground">{{ t('firewall.enableFirst') }}</p>
        </template>

        <template v-if="tab === 'rules'">
          <p class="m-0 text-detail text-muted-foreground">{{ t('firewall.rule.intro') }}</p>
          <RuleTable
            :rules="view?.rules ?? []"
            :loading="!view"
            :error="view ? loadError : null"
            :can-manage="canManage"
            :writable="ruleWritable"
            @create="openRule(null)"
            @edit="openRule"
            @changed="track"
            @retry="load"
          />
        </template>
        <template v-else-if="tab === 'addresses'">
          <p class="m-0 text-detail text-muted-foreground">{{ t('firewall.address.intro') }}</p>
          <AddressTable
            :addresses="view?.addresses ?? []"
            :loading="!view"
            :error="view ? loadError : null"
            :can-manage="canManage"
            :writable="ruleWritable"
            @create="showAddress = true"
            @changed="track"
            @retry="load"
          />
        </template>
        <template v-else-if="tab === 'forwards'">
          <p class="m-0 text-detail text-muted-foreground">{{ t('firewall.forward.intro') }}</p>
          <Notice v-if="!canAddForward" tone="info">{{ t('firewall.forward.limit', { max: FORWARDS_MAX }) }}</Notice>
          <ForwardTable
            :forwards="view?.forwards ?? []"
            :loading="!view"
            :error="view ? loadError : null"
            :can-manage="canManage"
            :writable="ruleWritable"
            :can-add="canAddForward"
            @create="openForward(null)"
            @edit="openForward"
            @changed="track"
            @retry="load"
          />
        </template>
        <BasicPanel v-else-if="view" :view="view" :can-manage="canManage" :writable="ruleWritable" :working="working" @ping="togglePing" />
        <div v-else class="grid gap-4 lg:grid-cols-2" aria-hidden="true">
          <Skeleton class="h-64 w-full rounded-lg" />
          <Skeleton class="h-64 w-full rounded-lg" />
        </div>
      </template>
    </div>

    <template v-if="view">
      <RuleSheet v-model:open="showRule" :rule="editingRule" :client-ip="view.clientIp" @changed="track" />
      <AddressSheet v-model:open="showAddress" :client-ip="view.clientIp" @changed="track" />
      <ForwardSheet
        v-model:open="showForward"
        :forward="editingForward"
        :forwards="view.forwards"
        :ip-forward="view.ipForward"
        :kept-ports="keptPorts"
        @changed="track"
      />
    </template>
  </div>
</template>
