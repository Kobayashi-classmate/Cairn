<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { DoorClosed, Undo2 } from '@lucide/vue'
import { serverClockOffset, serverNow } from '@/api/client'
import { cancelSshPort, fetchSsh, finishSshPort, type SshView } from '@/api/ssh'
import ErrorState from '@/components/common/ErrorState.vue'
import Notice from '@/components/common/Notice.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import PendingChangeBanner from '@/components/common/PendingChangeBanner.vue'
import type { PageTab } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { confirmAction } from '@/composables/useConfirmDialog'
import { useNow } from '@/composables/useNow'
import { usePageTab } from '@/composables/usePageTab'
import { formatDuration } from '@/i18n/format'
import { launchJob } from '@/jobs/dock'
import { useSessionStore } from '@/stores/session'
import PortPanel from './ssh/PortPanel.vue'
import SettingsPanel from './ssh/SettingsPanel.vue'
import SshKeys from './ssh/SshKeys.vue'
import SshLogins from './ssh/SshLogins.vue'

// SSH server ("list" skeleton with tabs, ?tab=): how people sign in
// (passwords, root), the port (moved safely: both ports listen until the
// old one closes, shown in the banner on top), authorized keys per user and
// the sign-ins.
type Tab = 'settings' | 'port' | 'keys' | 'logins'
const TABS: readonly Tab[] = ['settings', 'port', 'keys', 'logins']

/** After the old port's time is up, give the server's timer a moment before looking again. */
const CLOSE_SETTLE_MS = 5000
/** Looks again this many more times while the old port is still open after its time. */
const CLOSE_RECHECKS = 6

const { t } = useI18n()
const session = useSessionStore()
const now = useNow(1000)
const { tab, tabLocation } = usePageTab(TABS, 'settings')

const view = ref<SshView | null>(null)
const loadError = ref<unknown>(null)
/** A port job (close the old port, cancel) is under way. */
const portBusy = ref(false)

const canManage = computed(() => session.can('ssh.manage'))

let loadSeq = 0
async function load() {
  const mine = ++loadSeq
  try {
    const next = await fetchSsh()
    if (mine !== loadSeq) return
    view.value = next
    loadError.value = null
  } catch (err) {
    if (mine === loadSeq) loadError.value = err
  }
}

onMounted(load)

const keyCount = computed(() => view.value?.users.reduce((sum, user) => sum + user.keys, 0))

const tabs = computed<PageTab<Tab>[]>(() => [
  { value: 'settings', label: t('ssh.tabs.settings') },
  { value: 'port', label: t('ssh.tabs.port') },
  { value: 'keys', label: t('ssh.tabs.keys'), count: keyCount.value },
  { value: 'logins', label: t('ssh.tabs.logins') },
])

const status = computed(() => {
  const current = view.value
  if (!current) return ''
  if (!current.installed) return t('errors.ssh.not_installed')
  return [
    t('ssh.header.ports', { ports: current.ports.join(', ') }, current.ports.length),
    current.passwordAuthentication ? t('ssh.header.passwordsOn') : t('ssh.header.passwordsOff'),
    t('ssh.header.root', { value: t(`ssh.permitRootLogin.${current.permitRootLogin}`) }),
  ].join(' · ')
})

// closeAt is on the server's clock, which may differ from this browser's.
/** Milliseconds until a server time (Unix seconds). */
function msUntil(at: number): number {
  return at * 1000 - serverNow()
}

// The port move.
const pending = computed(() => view.value?.pending ?? null)
/** The server's time in Unix seconds, ticking every second. */
const serverSeconds = computed(() => now.value + serverClockOffset() / 1000)
const closesIn = computed(() => (pending.value ? Math.max(0, Math.ceil(pending.value.closeAt - serverSeconds.value)) : 0))

// The server's timer closes the old port at closeAt: look again then, and
// a few more times if it has not closed yet.
let closeTimer: ReturnType<typeof setTimeout> | undefined
let rechecks = 0
let unmounted = false

function scheduleCloseCheck() {
  clearTimeout(closeTimer)
  const closeAt = pending.value?.closeAt
  if (!closeAt || unmounted) return
  closeTimer = setTimeout(async () => {
    await load()
    if (unmounted || pending.value?.closeAt !== closeAt || rechecks >= CLOSE_RECHECKS) return
    rechecks++
    scheduleCloseCheck()
  }, Math.max(0, msUntil(closeAt)) + CLOSE_SETTLE_MS)
}

watch(
  () => pending.value?.closeAt,
  () => {
    rechecks = 0
    scheduleCloseCheck()
  },
)
onBeforeUnmount(() => {
  unmounted = true
  clearTimeout(closeTimer)
})

async function runPortJob(start: typeof finishSshPort, rethrow: boolean) {
  portBusy.value = true
  const job = await launchJob(start, {
    rethrow,
    onFinished: () => {
      portBusy.value = false
      void load()
    },
  }).catch((err: unknown) => {
    portBusy.value = false
    throw err
  })
  if (!job) portBusy.value = false
}

function finishPort() {
  const current = pending.value
  if (!current) return
  void confirmAction({
    title: t('ssh.port.finishTitle', { old: current.oldPorts.join(', ') }),
    description: t('ssh.port.finishText', { port: current.newPort }),
    confirmLabel: t('ssh.port.finish'),
    destructive: true,
    // Step-up protected: the client asks for the password and retries.
    run: () => runPortJob(finishSshPort, true),
  })
}

function cancelPort() {
  void runPortJob(cancelSshPort, false)
}

/** What the banner says about closing the old port. */
const pendingText = computed(() => {
  const current = pending.value
  if (!current) return ''
  const named = { old: current.oldPorts.join(', '), port: current.newPort }
  if (current.held === 'not_listening') return t('ssh.port.heldNotListening', named)
  if (current.held === 'unreached') return t(current.reachedAt ? 'ssh.port.heldReached' : 'ssh.port.heldUnreached', named)
  return t(current.reachedAt ? 'ssh.port.pendingReached' : 'ssh.port.pendingText', named)
})
const pendingCountdown = computed(() => {
  const current = pending.value
  if (!current) return ''
  if (current.held) return t('ssh.port.held')
  if (closesIn.value <= 0) return t('ssh.port.closing')
  return t(current.reachedAt ? 'ssh.port.closesIn' : 'ssh.port.closesInIfReached', { time: formatDuration(closesIn.value) })
})

const steps = computed(() => {
  const current = pending.value
  if (!current) return []
  return [
    t('ssh.port.step.listening'),
    t('ssh.port.step.check', { port: current.newPort }),
    t('ssh.port.step.closed', { old: current.oldPorts.join(', ') }),
  ]
})
</script>

<template>
  <div>
    <PageHeader
      :title="t('ssh.title')"
      :status="status"
      :status-loading="!view && !loadError"
      :tabs="tabs"
      :tab="tab"
      :tab-location="tabLocation"
    />

    <div class="flex flex-col gap-4">
      <ErrorState v-if="loadError && !view" :error="loadError" class="rounded-lg border bg-card" @retry="load" />

      <template v-else-if="view">
        <Notice v-if="!view.installed" tone="warning">{{ t('errors.ssh.not_installed') }}</Notice>
        <Notice v-if="loadError" tone="destructive">
          {{ t('common.state.refreshFailed') }}
          <template #action>
            <button type="button" class="underline underline-offset-2 hover:no-underline" @click="load">{{ t('common.action.retry') }}</button>
          </template>
        </Notice>

        <PendingChangeBanner
          v-if="pending"
          :tone="pending.held ? 'warning' : 'waiting'"
          :title="t('ssh.port.pendingTitle', { port: pending.newPort })"
          :description="pendingText"
          :steps="steps"
          :current="1"
          :countdown="pendingCountdown"
        >
          <template v-if="canManage" #actions>
            <Button size="sm" class="max-md:h-11" :disabled="portBusy" @click="finishPort">
              <DoorClosed aria-hidden="true" />
              {{ t('ssh.port.finish') }}
            </Button>
            <Button variant="outline" size="sm" class="max-md:h-11" :disabled="portBusy" @click="cancelPort">
              <Undo2 aria-hidden="true" />
              {{ t('ssh.port.cancel') }}
            </Button>
          </template>
        </PendingChangeBanner>

        <SettingsPanel v-if="tab === 'settings'" :view="view" :can-manage="canManage" @changed="load" />
        <PortPanel v-else-if="tab === 'port'" :view="view" :can-manage="canManage" @changed="load" />
        <SshKeys
          v-else-if="tab === 'keys'"
          :users="view.users"
          :can-manage="canManage && view.installed"
          :root-keys="view.rootKeys"
          @changed="load"
        />
        <SshLogins v-else />
      </template>

      <template v-else>
        <SshLogins v-if="tab === 'logins'" />
        <div v-else class="grid gap-4 lg:grid-cols-2" aria-hidden="true">
          <Skeleton class="h-56 w-full rounded-lg" />
          <Skeleton class="h-56 w-full rounded-lg" />
        </div>
      </template>
    </div>
  </div>
</template>
