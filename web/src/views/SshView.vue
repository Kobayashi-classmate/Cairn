<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// SSH server: how people log in (passwords, root), the port (moved safely:
// both ports listen until the old one closes), authorized keys and the
// login log.
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NFormItem,
  NH2,
  NInputNumber,
  NSelect,
  NSpin,
  NSwitch,
  NTabPane,
  NTabs,
  NText,
} from 'naive-ui'
import {
  CLOSE_AFTER_DEFAULT,
  CLOSE_AFTER_MAX,
  CLOSE_AFTER_MIN,
  PERMIT_ROOT_LOGIN,
  cancelSshPort,
  changeSshPort,
  fetchSsh,
  finishSshPort,
  updateSshSettings,
  type PermitRootLogin,
  type SshView,
} from '@/api/ssh'
import type { Job } from '@/api/jobs'
import JobProgress from '@/components/JobProgress.vue'
import { useConfirm } from '@/composables/useConfirm'
import { useJobRun } from '@/composables/useJobRun'
import { useNow } from '@/composables/useNow'
import { errorMessage } from '@/i18n/errors'
import { formatDuration } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'
import SshKeys from './ssh/SshKeys.vue'
import SshLogins from './ssh/SshLogins.vue'

const { t } = useI18n()
const confirm = useConfirm()
const session = useSessionStore()
const now = useNow(1000)

const view = ref<SshView | null>(null)
const loadError = ref<unknown>(null)
const tab = ref('keys')
const settings = reactive({ passwordAuthentication: true, permitRootLogin: 'prohibit-password' as PermitRootLogin })
const port = reactive({ port: null as number | null, closeAfter: CLOSE_AFTER_DEFAULT as number | null })

const canManage = computed(() => session.can('ssh.manage'))

async function load() {
  try {
    view.value = await fetchSsh()
    loadError.value = null
  } catch (err) {
    loadError.value = err
  }
}

onMounted(load)

// The forms start from what the server has.
watch(view, (value) => {
  if (!value) return
  settings.passwordAuthentication = value.passwordAuthentication
  settings.permitRootLogin = value.permitRootLogin
})

const { jobId, busy, run, finished, dismiss } = useJobRun((_job: Job) => void load())

const rootOptions = computed(() => PERMIT_ROOT_LOGIN.map((value) => ({ value, label: t(`ssh.permitRootLogin.${value}`) })))
const settingsChanged = computed(
  () =>
    !!view.value &&
    (settings.passwordAuthentication !== view.value.passwordAuthentication || settings.permitRootLogin !== view.value.permitRootLogin),
)
const keyCount = computed(() => view.value?.users.reduce((sum, user) => sum + user.keys, 0) ?? 0)
const writable = computed(() => canManage.value && !!view.value?.installed && !busy.value)

async function saveSettings() {
  if (!view.value) return
  if (view.value.passwordAuthentication && !settings.passwordAuthentication) {
    const ok = await confirm({
      title: t('ssh.settings.passwordsOffTitle'),
      content: t('ssh.settings.passwordsOffText'),
      confirmText: t('ssh.settings.passwordsOffSubmit'),
      danger: true,
    })
    if (!ok) return
  }
  // Step-up protected: the client asks for the password and retries.
  void run(() => updateSshSettings({ ...settings }))
}

const portValid = computed(
  () =>
    port.port !== null &&
    Number.isInteger(port.port) &&
    port.port >= 1 &&
    port.port <= 65535 &&
    !view.value?.ports.includes(port.port) &&
    port.closeAfter !== null &&
    port.closeAfter >= CLOSE_AFTER_MIN &&
    port.closeAfter <= CLOSE_AFTER_MAX,
)

async function movePort() {
  if (!portValid.value || port.port === null) return
  const minutes = port.closeAfter ?? CLOSE_AFTER_DEFAULT
  const ok = await confirm({
    title: t('ssh.port.title', { port: port.port }),
    content: t('ssh.port.text', { minutes }),
    confirmText: t('ssh.port.submit'),
  })
  if (ok) void run(() => changeSshPort(port.port ?? 0, minutes))
}

async function finishPort() {
  const ok = await confirm({
    title: t('ssh.port.finishTitle'),
    content: t('ssh.port.finishText'),
    confirmText: t('ssh.port.finish'),
    danger: true,
  })
  if (ok) void run(finishSshPort)
}

function cancelPort() {
  void run(cancelSshPort)
}

const closesIn = computed(() => (view.value?.pending ? Math.max(0, view.value.pending.closeAt - now.value) : 0))
</script>

<template>
  <div class="ssh">
    <NH2 class="ssh__title">{{ t('ssh.title') }}</NH2>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NSpin v-if="!view && !loadError" class="wait" />

    <template v-else-if="view">
      <NAlert v-if="!view.installed" type="warning" class="block">{{ t('errors.ssh.not_installed') }}</NAlert>

      <div v-if="jobId" class="job">
        <JobProgress :job-id="jobId" @finished="finished" />
        <NButton size="small" quaternary :disabled="busy" @click="dismiss">{{ t('sites.create.close') }}</NButton>
      </div>

      <NAlert v-if="view.pending" type="warning" class="block">
        <div>{{ t('ssh.port.pending', { old: view.pending.oldPorts.join(', '), port: view.pending.newPort, time: formatDuration(closesIn) }) }}</div>
        <div v-if="canManage" class="block__actions">
          <NButton size="small" type="primary" :disabled="busy" @click="finishPort">{{ t('ssh.port.finish') }}</NButton>
          <NButton size="small" :disabled="busy" @click="cancelPort">{{ t('ssh.port.cancel') }}</NButton>
        </div>
      </NAlert>

      <div class="cards">
        <NCard size="small" :title="t('ssh.status.title')">
          <dl class="facts">
            <dt>{{ t('ssh.status.service') }}</dt>
            <dd class="mono">{{ view.service }}<template v-if="view.socketActivated"> {{ t('ssh.status.socket') }}</template></dd>
            <dt>{{ t('ssh.status.ports') }}</dt>
            <dd class="mono">{{ view.ports.join(', ') }}</dd>
            <dt>{{ t('ssh.status.listen') }}</dt>
            <dd class="mono">{{ view.listenAddresses.join(', ') }}</dd>
            <dt>{{ t('ssh.status.pubkey') }}</dt>
            <dd>{{ view.pubkeyAuthentication ? t('ssh.status.on') : t('ssh.status.off') }}</dd>
            <dt>{{ t('ssh.status.firewall') }}</dt>
            <dd>{{ view.firewall || t('ssh.status.noFirewall') }}</dd>
          </dl>
        </NCard>

        <NCard size="small" :title="t('ssh.settings.title')">
          <NFormItem :label="t('ssh.settings.passwords')" :show-feedback="false" label-placement="left" class="item">
            <NSwitch v-model:value="settings.passwordAuthentication" :disabled="!writable" />
          </NFormItem>
          <NText v-if="!settings.passwordAuthentication && keyCount === 0" type="error" tag="p" class="hint">
            {{ t('errors.ssh.no_keys') }}
          </NText>
          <NFormItem :label="t('ssh.settings.root')" :show-feedback="false" class="item">
            <NSelect v-model:value="settings.permitRootLogin" :options="rootOptions" :disabled="!writable" />
          </NFormItem>
          <div v-if="canManage" class="card-actions">
            <NButton type="primary" size="small" :disabled="!writable || !settingsChanged" @click="saveSettings">
              {{ t('common.action.save') }}
            </NButton>
          </div>
        </NCard>

        <NCard size="small" :title="t('ssh.port.cardTitle')">
          <NText depth="3" tag="p" class="hint">{{ t('ssh.port.intro') }}</NText>
          <div class="port">
            <NFormItem :label="t('ssh.port.new')" :show-feedback="false">
              <NInputNumber v-model:value="port.port" :min="1" :max="65535" :precision="0" :disabled="!writable || !!view.pending" />
            </NFormItem>
            <NFormItem :label="t('ssh.port.closeAfter')" :show-feedback="false">
              <NInputNumber
                v-model:value="port.closeAfter"
                :min="CLOSE_AFTER_MIN"
                :max="CLOSE_AFTER_MAX"
                :precision="0"
                :disabled="!writable || !!view.pending"
              >
                <template #suffix>{{ t('cron.schedule.minutesUnit') }}</template>
              </NInputNumber>
            </NFormItem>
          </div>
          <div v-if="canManage" class="card-actions">
            <NButton size="small" type="primary" :disabled="!writable || !!view.pending || !portValid" @click="movePort">
              {{ t('ssh.port.submit') }}
            </NButton>
          </div>
        </NCard>
      </div>

      <NTabs v-model:value="tab" type="line" class="tabs">
        <NTabPane name="keys" :tab="t('ssh.tabs.keys')">
          <SshKeys :users="view.users" :can-manage="canManage && view.installed" :root-keys="view.rootKeys" @changed="load" />
        </NTabPane>
        <NTabPane name="logins" :tab="t('ssh.tabs.logins')">
          <SshLogins />
        </NTabPane>
      </NTabs>
    </template>
  </div>
</template>

<style scoped>
.ssh {
  max-inline-size: 1300px;
}

.ssh__title {
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

.block__actions {
  display: flex;
  gap: 8px;
  margin-block-start: 8px;
}

.cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
  gap: 12px;
}

.facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 4px 12px;
  margin: 0;
  font-size: 13px;
}

.facts dt {
  opacity: 0.7;
}

.facts dd {
  margin: 0;
  overflow-wrap: anywhere;
}

.item {
  margin-block-end: 12px;
}

.hint {
  margin: 0 0 12px;
  font-size: 12px;
}

.port {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 16px;
}

.card-actions {
  display: flex;
  justify-content: flex-end;
  margin-block-start: 12px;
}

.tabs {
  margin-block-start: 16px;
}

.job {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 4px;
  margin-block-end: 12px;
}

.job > :first-child {
  align-self: stretch;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
}
</style>
