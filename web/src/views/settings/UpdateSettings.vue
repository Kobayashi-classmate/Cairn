<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { Download, LoaderCircle, RefreshCw } from '@lucide/vue'
import { isApiError, isStepUpRequired } from '@/api/client'
import { applyUpdate, checkForUpdates, fetchUpdateStatus, type UpdateStatus } from '@/api/updates'
import Card from '@/components/common/Card.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import Notice from '@/components/common/Notice.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import MarkdownText from '@/components/MarkdownText'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { confirmAction } from '@/composables/useConfirmDialog'
import { pickLocalized } from '@/i18n'
import { errorMessage } from '@/i18n/errors'
import { formatDate } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'

// Online update of the panel itself. Installing is not a regular job: the
// panel restarts while it runs, so this page asks for progress until the
// new version answers (and gives up after a few minutes of silence).

/** How often to ask for progress while an update runs. */
const POLL_INTERVAL_MS = 3000
/** Give up when the panel has not answered for this long (it restarts during updates). */
const GIVE_UP_MS = 3 * 60_000

const { t, te } = useI18n()
const session = useSessionStore()

const canManage = computed(() => session.can('settings.manage'))

const status = ref<UpdateStatus | null>(null)
const loadError = ref<unknown>(null)
const actionError = ref<unknown>(null)
const checking = ref(false)
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
  loadError.value = null
  try {
    status.value = await fetchUpdateStatus()
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

function askInstall(version: string) {
  void confirmAction({
    title: t('updates.confirm.title', { version }),
    description: t('updates.confirm.content'),
    confirmLabel: t('updates.install', { version }),
    run: async () => {
      actionError.value = null
      try {
        const started = await applyUpdate(version)
        target.value = version
        if (status.value) status.value = { ...status.value, job: started }
        startPolling()
      } catch (err) {
        // A declined re-authentication keeps the dialog open.
        if (isStepUpRequired(err)) throw err
        if (isApiError(err) && err.code === 'update.in_progress') {
          // Someone else started it; follow that job instead.
          target.value = version
          startPolling()
        }
        // Shown on the page with its technical reason.
        actionError.value = err
      }
    },
  })
}

/** A technical `reason` parameter of an update error, shown as is. */
function errorReason(err: unknown): string {
  return isApiError(err) && typeof err.params.reason === 'string' ? err.params.reason : ''
}

function reasonText(reason: string): string {
  const key = `updates.reason.${reason}`
  return te(key) ? t(key) : t('updates.reason.unknown', { reason })
}

function channelText(channel: string): string {
  return te(`updates.channel.${channel}`) ? t(`updates.channel.${channel}`) : channel
}

/** The running panel is newer than the code loaded in this tab. */
const staleAssets = computed(() => !!status.value && !!session.meta && session.meta.version !== status.value.current)

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
  <Card :title="t('updates.title')" :description="t('updates.scope')" :level="3" flush>
    <template v-if="canManage" #actions>
      <Button variant="outline" size="sm" class="max-md:h-11" :disabled="checking || running || !status" @click="check">
        <LoaderCircle v-if="checking" class="animate-spin" aria-hidden="true" />
        <RefreshCw v-else aria-hidden="true" />
        {{ t('updates.check') }}
      </Button>
    </template>
    <ErrorState v-if="loadError" :error="loadError" compact @retry="load" />
    <div v-else-if="!status" class="flex flex-col gap-2 px-4 py-4 md:px-[18px]" role="status">
      <span class="sr-only">{{ t('common.state.loading') }}</span>
      <Skeleton class="h-4 w-40" />
      <Skeleton class="h-4 w-56" />
      <Skeleton class="h-4 w-32" />
    </div>
    <KeyValueList v-else>
      <KeyValueRow :label="t('updates.current')" mono>{{ status.current }}</KeyValueRow>
      <KeyValueRow :label="t('common.field.channel')">
        {{ channelText(status.channel) }}
        <RouterLink :to="{ name: 'settings-cloud' }" class="text-detail text-link underline-offset-2 hover:underline">
          {{ t('updates.changeChannel') }}
        </RouterLink>
      </KeyValueRow>
      <KeyValueRow :label="t('updates.checkedAt')">
        <RelativeTime v-if="status.checkedAt > 0" :value="status.checkedAt" />
        <span v-else class="text-muted-foreground">{{ t('updates.never') }}</span>
      </KeyValueRow>
    </KeyValueList>
  </Card>

  <Notice v-if="actionError" tone="destructive" dismissible @dismiss="actionError = null">
    {{ errorMessage(actionError) }}
    <code v-if="errorReason(actionError)" class="mt-1 block font-mono text-xs font-normal [overflow-wrap:anywhere]" dir="ltr">
      {{ errorReason(actionError) }}
    </code>
  </Notice>

  <template v-if="status">
    <!-- Progress of an update started here or elsewhere. -->
    <div v-if="running" role="status" class="flex items-center gap-3 rounded-lg bg-primary-soft px-4 py-2.5 font-medium text-primary-soft-foreground">
      <LoaderCircle class="size-4 shrink-0 animate-spin" aria-hidden="true" />
      {{ restarting ? t('updates.job.restarting') : t('updates.job.running', { version: job?.version || target || '' }) }}
    </div>
    <Card v-else-if="gaveUp" :title="t('updates.job.timeoutTitle')" :level="3">
      <p class="m-0 mb-3 text-muted-foreground">{{ t('updates.job.timeout') }}</p>
      <Button variant="outline" size="sm" class="max-md:h-11" @click="startPolling">{{ t('updates.job.checkAgain') }}</Button>
    </Card>
    <Card v-else-if="job?.state === 'failed'" :level="3">
      <template #title>
        <span class="text-destructive">{{ t('updates.job.failedTitle', { version: job.version }) }}</span>
      </template>
      <p class="m-0 whitespace-pre-wrap [overflow-wrap:anywhere]">{{ job.error }}</p>
      <details v-if="job.log" class="group mt-3">
        <summary class="cursor-pointer text-detail font-medium text-link">{{ t('updates.job.log') }}</summary>
        <pre class="mt-2 max-h-80 overflow-auto rounded-md bg-muted px-3 py-2 font-mono text-xs leading-relaxed" dir="ltr">{{ job.log }}</pre>
      </details>
    </Card>
    <Notice v-else-if="succeeded && job" tone="success">
      {{ t('updates.job.succeeded', { version: job.version }) }}
      <span v-if="staleAssets" class="block font-normal">{{ t('updates.job.reloadHint') }}</span>
      <template v-if="staleAssets" #action>
        <button type="button" class="underline underline-offset-2 hover:no-underline" @click="reload">{{ t('updates.job.reload') }}</button>
      </template>
    </Notice>

    <!-- What the release server offers. -->
    <Notice v-if="status.status === 'up_to_date'" tone="success">{{ t('updates.upToDate') }}</Notice>
    <Notice v-else-if="status.status === 'unavailable'" tone="warning">
      {{ reasonText(status.reason) }}
      <template v-if="status.reason === 'cloud_disabled'" #action>
        <RouterLink :to="{ name: 'settings-cloud' }" class="whitespace-nowrap text-current underline underline-offset-2 hover:no-underline">
          {{ t('updates.openCloud') }}
        </RouterLink>
      </template>
    </Notice>
    <Card v-else-if="status.status === 'available' && status.latest" :level="3">
      <template #title>{{ t('updates.available', { version: status.latest.version }) }}</template>
      <template #actions>
        <StatusPill v-if="status.latest.security" tone="destructive">{{ t('updates.securityRelease') }}</StatusPill>
        <span class="text-xs text-muted-foreground">{{ formatDate(status.latest.publishedAt) }}</span>
      </template>
      <div class="flex flex-col items-start gap-4">
        <MarkdownText
          v-if="pickLocalized(status.latest.notes)"
          :source="pickLocalized(status.latest.notes)"
          class="w-full [overflow-wrap:anywhere]"
        />
        <Button v-if="canManage" class="max-md:h-11" :disabled="running" @click="askInstall(status.latest.version)">
          <Download aria-hidden="true" />
          {{ t('updates.install', { version: status.latest.version }) }}
        </Button>
      </div>
    </Card>
  </template>
</template>
