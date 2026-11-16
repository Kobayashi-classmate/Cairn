<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Web terminal: one tab per session. Sessions outlive the page for a few
// minutes, so a reload lists them and joins them again.
import { computed, defineAsyncComponent, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NEmpty, NH2, NIcon, NSpin, NSwitch, NTabPane, NTabs, NTag, NText } from 'naive-ui'
import { AddOutline } from '@vicons/ionicons5'
import { isApiError, isStepUpRequired } from '@/api/client'
import {
  TERMINALS_MAX,
  closeTermSession,
  fetchTermSessions,
  fetchTermUsers,
  setRootTerminal,
  type TermConnection,
  type TermSession,
  type TermUsers,
} from '@/api/terminal'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'
import NewTerminalDialog from './terminal/NewTerminalDialog.vue'

// xterm.js is only loaded when a terminal is shown.
const TerminalPane = defineAsyncComponent(() => import('./terminal/TerminalPane.vue'))

const { t } = useI18n()
const confirm = useConfirm()
const showError = useErrorToast()
const session = useSessionStore()

const canAdmin = computed(() => session.can('terminal.admin'))

const users = ref<TermUsers | null>(null)
const sessions = ref<TermSession[]>([])
const states = ref<Record<string, TermConnection>>({})
const active = ref<string | undefined>(undefined)
const loading = ref(true)
const loadError = ref<unknown>(null)
const showNew = ref(false)
const switching = ref(false)

async function loadUsers() {
  users.value = await fetchTermUsers()
}

async function load() {
  loading.value = true
  try {
    const [, list] = await Promise.all([loadUsers(), fetchTermSessions()])
    sessions.value = list
    loadError.value = null
    if (!active.value || !list.some((entry) => entry.id === active.value)) active.value = list[list.length - 1]?.id
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

onMounted(load)

const full = computed(() => sessions.value.length >= TERMINALS_MAX)

function opened(created: TermSession) {
  sessions.value.push(created)
  active.value = created.id
}

function label(entry: TermSession): string {
  const site = users.value?.users.find((user) => user.name === entry.user)?.site
  return site ? t('terminal.user.site', { user: entry.user, site }) : entry.user
}

async function close(id: string | number) {
  const entry = sessions.value.find((item) => item.id === id)
  if (!entry) return
  if (states.value[entry.id] !== 'ended') {
    const ok = await confirm({
      title: t('terminal.close.title', { name: label(entry) }),
      content: t('terminal.close.text'),
      confirmText: t('terminal.close.submit'),
      danger: true,
    })
    if (!ok) return
  }
  try {
    await closeTermSession(entry.id)
  } catch (err) {
    // A session that already ended is gone on the server too.
    if (!(isApiError(err) && err.status === 404)) {
      showError(err)
      return
    }
  }
  const index = sessions.value.findIndex((item) => item.id === entry.id)
  sessions.value.splice(index, 1)
  delete states.value[entry.id]
  if (active.value === entry.id) active.value = sessions.value[Math.min(index, sessions.value.length - 1)]?.id
}

async function toggleRoot(enabled: boolean) {
  if (enabled) {
    const ok = await confirm({
      title: t('terminal.root.enableTitle'),
      content: t('terminal.root.enableText'),
      confirmText: t('terminal.root.enable'),
      danger: true,
    })
    if (!ok) return
  }
  switching.value = true
  try {
    await setRootTerminal(enabled)
    await loadUsers()
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  } finally {
    switching.value = false
  }
}

function stateTag(id: string): { type: 'warning' | 'default'; text: string } | null {
  const state = states.value[id]
  if (state === 'reconnecting') return { type: 'warning', text: t('terminal.state.reconnecting') }
  if (state === 'ended') return { type: 'default', text: t('terminal.state.ended') }
  return null
}
</script>

<template>
  <div class="terminal">
    <div class="terminal__header">
      <NH2 class="terminal__title">{{ t('terminal.title') }}</NH2>
      <div class="terminal__tools">
        <label v-if="canAdmin && users" class="root-switch">
          <NSwitch
            :value="users.rootTerminal"
            size="small"
            :loading="switching"
            :disabled="switching || users.locked"
            @update:value="toggleRoot"
          />
          <span>{{ t('terminal.root.label') }}</span>
        </label>
        <NButton type="primary" :disabled="!users || full" @click="showNew = true">
          <template #icon>
            <NIcon><AddOutline /></NIcon>
          </template>
          {{ t('terminal.new.button') }}
        </NButton>
      </div>
    </div>

    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NAlert v-if="canAdmin && users?.locked" type="info" class="block">{{ t('terminal.root.locked') }}</NAlert>
    <NAlert v-else-if="canAdmin && users?.rootTerminal" type="warning" class="block">{{ t('terminal.root.active') }}</NAlert>
    <NText v-if="full" depth="3" tag="p" class="note">{{ t('errors.detailed.term.too_many', { max: TERMINALS_MAX }) }}</NText>

    <NSpin v-if="loading && sessions.length === 0" class="terminal__wait" />
    <NEmpty v-else-if="sessions.length === 0 && !loadError" :description="t('terminal.empty.title')" class="empty">
      <template #extra>
        <NText depth="3" tag="p" class="empty__text">{{ t('terminal.empty.text') }}</NText>
        <NButton type="primary" :disabled="!users" @click="showNew = true">{{ t('terminal.new.button') }}</NButton>
      </template>
    </NEmpty>
    <NTabs
      v-else-if="sessions.length > 0"
      v-model:value="active"
      type="card"
      closable
      :addable="!full"
      class="terminal__tabs"
      @close="close"
      @add="showNew = true"
    >
      <NTabPane v-for="entry in sessions" :key="entry.id" :name="entry.id" display-directive="show">
        <template #tab>
          <span class="tab">
            <span>{{ label(entry) }}</span>
            <NTag v-if="stateTag(entry.id)" size="tiny" :type="stateTag(entry.id)?.type" :bordered="false">
              {{ stateTag(entry.id)?.text }}
            </NTag>
          </span>
        </template>
        <div class="session">
          <NText depth="3" class="session__info" dir="ltr">
            {{ t('terminal.info', { user: entry.user, shell: entry.shell, home: entry.home, created: formatDateTime(entry.createdAt) }) }}
          </NText>
          <div class="session__screen">
            <TerminalPane :session-id="entry.id" :active="active === entry.id" @status="(state: TermConnection) => (states[entry.id] = state)" />
          </div>
          <NText depth="3" tag="p" class="session__hint">{{ t('terminal.hint') }}</NText>
        </div>
      </NTabPane>
    </NTabs>

    <NewTerminalDialog
      v-if="users"
      v-model:show="showNew"
      :users="users.users"
      :root-terminal="users.rootTerminal"
      @opened="opened"
    />
  </div>
</template>

<style scoped>
.terminal {
  display: flex;
  flex-direction: column;
  max-inline-size: 1400px;
}

.terminal__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 16px;
  margin-block-end: 12px;
}

.terminal__title {
  margin: 0;
}

.terminal__tools {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 16px;
}

.root-switch {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  font-size: 13px;
}

.block {
  margin-block-end: 12px;
}

.note {
  margin-block: 0 12px;
  font-size: 13px;
}

.terminal__wait {
  display: flex;
  justify-content: center;
  padding-block: 64px;
}

.empty {
  padding-block: 48px;
}

.empty__text {
  max-inline-size: 520px;
  margin: 0 auto 12px;
  text-align: center;
}

.tab {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.session {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.session__info {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  overflow-wrap: anywhere;
}

.session__screen {
  block-size: max(320px, calc(100vh - 280px));
}

.session__hint {
  margin: 0;
  font-size: 12px;
}
</style>
