<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NCollapse,
  NCollapseItem,
  NIcon,
  NSpin,
  NTag,
  NText,
} from 'naive-ui'
import { CheckmarkCircleOutline, RefreshOutline } from '@vicons/ionicons5'
import { isApiError, isStepUpRequired } from '@/api/client'
import {
  applyUpdate,
  checkForUpdates,
  fetchUpdateStatus,
  type UpdateStatus,
} from '@/api/updates'
import MarkdownText from '@/components/MarkdownText'
import { useConfirm } from '@/composables/useConfirm'
import { pickLocalized } from '@/i18n'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'
import CloudSettings from './CloudSettings.vue'

/** How often to ask for progress while an update runs. */
const POLL_INTERVAL_MS = 3000
/** Give up when the panel has not answered for this long (it restarts during updates). */
const GIVE_UP_MS = 3 * 60_000

const { t, te } = useI18n()
const confirm = useConfirm()
const session = useSessionStore()

const canManage = computed(() => session.can('settings.manage'))

const status = ref<UpdateStatus | null>(null)
const loadError = ref<unknown>(null)
const actionError = ref<unknown>(null)
const checking = ref(false)
const applying = ref(false)
/** A progress request failed while the job runs: the panel is most likely restarting. */
const restarting = ref(false)
const gaveUp = ref(false)
/** The version this page asked to install, if any. */
const target = ref<string | null>(null)

let timer: ReturnType<typeof setTimeout> | undefined
const polling = ref(false)
let lastAnswer = 0
let unmounted = false

const job = computed(() => status.value?.job ?? null)
const running = computed(() => job.value?.state === 'running' || polling.value)

/** Keep asking while the job runs, and after it succeeds until the new version answers. */
function needsPolling(next: UpdateStatus): boolean {
  if (next.job.state === 'running') return true
  return next.job.state === 'succeeded' && target.value !== null && next.current !== target.value
}

function startPolling() {
  if (polling.value || unmounted) return
  polling.value = true
  gaveUp.value = false
  restarting.value = false
  lastAnswer = Date.now()
  timer = setTimeout(poll, POLL_INTERVAL_MS)
}

async function poll() {
  if (unmounted) return
  try {
    const next = await fetchUpdateStatus()
    status.value = next
    lastAnswer = Date.now()
    restarting.value = false
    if (needsPolling(next)) {
      timer = setTimeout(poll, POLL_INTERVAL_MS)
    } else {
      polling.value = false
    }
  } catch {
    // Connection errors and 502s are expected while the panel restarts.
    if (Date.now() - lastAnswer >= GIVE_UP_MS) {
      polling.value = false
      restarting.value = false
      gaveUp.value = true
      return
    }
    restarting.value = true
    timer = setTimeout(poll, POLL_INTERVAL_MS)
  }
}

async function load() {
  try {
    status.value = await fetchUpdateStatus()
    loadError.value = null
    if (status.value.job.state === 'running') startPolling()
  } catch (err) {
    loadError.value = err
  }
}

onMounted(load)
onBeforeUnmount(() => {
  unmounted = true
  clearTimeout(timer)
})

async function check() {
  actionError.value = null
  checking.value = true
  try {
    status.value = await checkForUpdates()
  } catch (err) {
    if (!isStepUpRequired(err)) actionError.value = err
  } finally {
    checking.value = false
  }
}

async function install(version: string) {
  const ok = await confirm({
    title: t('updates.confirm.title', { version }),
    content: t('updates.confirm.content'),
    confirmText: t('updates.install', { version }),
  })
  if (!ok) return
  actionError.value = null
  applying.value = true
  try {
    const started = await applyUpdate(version)
    target.value = version
    if (status.value) status.value = { ...status.value, job: started }
    startPolling()
  } catch (err) {
    if (isApiError(err) && err.code === 'update.in_progress') {
      // Someone else started it; follow that job instead.
      target.value = version
      startPolling()
    }
    if (!isStepUpRequired(err)) actionError.value = err
  } finally {
    applying.value = false
  }
}

function retryPolling() {
  startPolling()
}

/** A technical `reason` parameter of an update error, shown as is. */
function errorReason(err: unknown): string {
  return isApiError(err) && typeof err.params.reason === 'string' ? err.params.reason : ''
}

function reasonText(reason: string): string {
  const key = `updates.reason.${reason}`
  return te(key) ? t(key) : t('updates.reason.unknown', { reason })
}

/** The running panel is newer than the code loaded in this tab. */
const staleAssets = computed(
  () => !!status.value && !!session.meta && session.meta.version !== status.value.current,
)

/** Shown for an update started from this page, or when this tab still runs the old code. */
const succeeded = computed(
  () =>
    job.value?.state === 'succeeded' &&
    !!status.value &&
    job.value.version === status.value.current &&
    (target.value !== null || staleAssets.value),
)

function reload() {
  window.location.reload()
}
</script>

<template>
  <NCard :title="t('updates.title')" size="small">
    <template #header-extra>
      <NButton
        v-if="canManage"
        size="small"
        :loading="checking"
        :disabled="running || !status"
        @click="check"
      >
        <template #icon>
          <NIcon><RefreshOutline /></NIcon>
        </template>
        {{ t('updates.check') }}
      </NButton>
    </template>

    <NText depth="3" tag="p" class="intro">{{ t('updates.scope') }}</NText>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NAlert v-if="actionError" type="error" class="block" closable @close="actionError = null">
      <div>{{ errorMessage(actionError) }}</div>
      <code v-if="errorReason(actionError)" class="mono" dir="ltr">{{ errorReason(actionError) }}</code>
    </NAlert>

    <NSpin :show="!status && !loadError">
      <template v-if="status">
        <dl class="facts">
          <div>
            <dt><NText depth="3">{{ t('updates.current') }}</NText></dt>
            <dd dir="ltr">{{ status.current }}</dd>
          </div>
          <div>
            <dt><NText depth="3">{{ t('common.field.channel') }}</NText></dt>
            <dd>{{ te(`updates.channel.${status.channel}`) ? t(`updates.channel.${status.channel}`) : status.channel }}</dd>
          </div>
          <div>
            <dt><NText depth="3">{{ t('updates.checkedAt') }}</NText></dt>
            <dd>{{ status.checkedAt > 0 ? formatDateTime(status.checkedAt) : t('updates.never') }}</dd>
          </div>
        </dl>

        <!-- Progress of an update started here or elsewhere. -->
        <NAlert v-if="running" type="info" class="block" :show-icon="false">
          <div class="progress">
            <NSpin size="small" />
            <span>
              {{
                restarting
                  ? t('updates.job.restarting')
                  : t('updates.job.running', { version: job?.version || target || '' })
              }}
            </span>
          </div>
        </NAlert>
        <NAlert v-else-if="gaveUp" type="error" :title="t('updates.job.timeoutTitle')" class="block">
          <p class="para">{{ t('updates.job.timeout') }}</p>
          <NButton size="small" @click="retryPolling">{{ t('updates.job.checkAgain') }}</NButton>
        </NAlert>
        <NAlert
          v-else-if="job?.state === 'failed'"
          type="error"
          :title="t('updates.job.failedTitle', { version: job.version })"
          class="block"
        >
          <p class="para pre">{{ job.error }}</p>
          <NCollapse v-if="job.log">
            <NCollapseItem :title="t('updates.job.log')" name="log">
              <pre class="log" dir="ltr">{{ job.log }}</pre>
            </NCollapseItem>
          </NCollapse>
        </NAlert>
        <NAlert
          v-else-if="succeeded && job"
          type="success"
          :title="t('updates.job.succeeded', { version: job.version })"
          class="block"
        >
          <template v-if="staleAssets">
            <p class="para">{{ t('updates.job.reloadHint') }}</p>
            <NButton size="small" type="primary" @click="reload">{{ t('updates.job.reload') }}</NButton>
          </template>
        </NAlert>

        <!-- What the release server offers. -->
        <div v-if="status.status === 'up_to_date'" class="state">
          <NIcon :size="20" class="state__ok"><CheckmarkCircleOutline /></NIcon>
          <span>{{ t('updates.upToDate') }}</span>
        </div>
        <NAlert v-else-if="status.status === 'unavailable'" type="warning" class="block">
          {{ reasonText(status.reason) }}
        </NAlert>
        <div v-else-if="status.status === 'available' && status.latest" class="release">
          <div class="release__head">
            <NText strong class="release__title">
              {{ t('updates.available', { version: status.latest.version }) }}
            </NText>
            <NTag v-if="status.latest.security" type="error" size="small">
              {{ t('updates.securityRelease') }}
            </NTag>
            <NText depth="3">{{ formatDateTime(status.latest.publishedAt) }}</NText>
          </div>
          <MarkdownText
            v-if="pickLocalized(status.latest.notes)"
            :source="pickLocalized(status.latest.notes)"
            class="release__notes"
          />
          <NButton
            v-if="canManage"
            type="primary"
            :loading="applying"
            :disabled="running"
            @click="install(status.latest.version)"
          >
            {{ t('updates.install', { version: status.latest.version }) }}
          </NButton>
        </div>
      </template>
    </NSpin>
  </NCard>

  <CloudSettings @saved="load" />
</template>

<style scoped>
.intro {
  margin-block: 0 12px;
}

.block {
  margin-block-end: 12px;
}

.facts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 12px 24px;
  margin: 0 0 16px;
}

.facts dd {
  margin: 2px 0 0;
}

.progress {
  display: flex;
  align-items: center;
  gap: 10px;
}

.para {
  margin: 0 0 8px;
}

.pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.log {
  max-block-size: 320px;
  margin: 0;
  padding: 8px 10px;
  overflow: auto;
  border-radius: 4px;
  background: rgb(128 128 128 / 12%);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre;
}

.mono {
  display: block;
  margin-block-start: 6px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
  overflow-wrap: anywhere;
}

.state {
  display: flex;
  align-items: center;
  gap: 8px;
}

.state__ok {
  color: #18a058;
}

.release {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 12px;
}

.release__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.release__title {
  font-size: 16px;
}

.release__notes {
  inline-size: 100%;
}
</style>
