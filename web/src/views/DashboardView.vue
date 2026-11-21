<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { ArrowRight, Plus, SquareTerminal } from '@lucide/vue'
import { isAbortError } from '@/api/client'
import { fetchOverview, type Overview } from '@/api/overview'
import { fetchSystemOverview, type HistoryPoint, type SystemOverview } from '@/api/system'
import Notice from '@/components/common/Notice.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import TotpNotice from '@/components/shell/TotpNotice.vue'
import { Button } from '@/components/ui/button'
import { useNavRoutes } from '@/composables/useNavRoutes'
import { usePolling } from '@/composables/usePolling'
import { formatDuration } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'
import MetricCards from './overview/MetricCards.vue'
import RecentJobsCard from './overview/RecentJobsCard.vue'
import SecurityCard from './overview/SecurityCard.vue'
import ServicesCard from './overview/ServicesCard.vue'
import SitesCard from './overview/SitesCard.vue'
import { findingLocation, moduleNames, noticeAction, noticeText } from './overview/labels'

// The overview (interface spec, section 6, "overview" skeleton): notices,
// four metric cards, then two columns of cards. Every card loads and fails
// on its own.
const METRICS_POLL_MS = 5000
const SUMMARY_POLL_MS = 60_000
/** Samples kept for the network card's bars. */
const HISTORY_KEEP = 32

const { t } = useI18n()
const session = useSessionStore()
const navRoutes = useNavRoutes()

// System metrics (system.read).
const system = ref<SystemOverview | null>(null)
const systemError = ref<unknown>(null)
const history = ref<HistoryPoint[]>([])

async function loadSystem(signal?: AbortSignal) {
  if (!session.can('system.read')) return
  try {
    const data = await fetchSystemOverview(signal)
    const newest = history.value.at(-1)?.ts ?? data.history.at(-1)?.ts ?? -Infinity
    const base = history.value.length > 0 ? history.value : data.history
    const live = data.current.ts > newest
      ? [...base, { ts: data.current.ts, cpuPercent: data.current.cpuPercent, memUsedPercent: 0, netRxBps: data.current.netRxBps, netTxBps: data.current.netTxBps }]
      : base
    history.value = live.slice(-HISTORY_KEEP)
    system.value = data
    systemError.value = null
  } catch (err) {
    if (!isAbortError(err)) systemError.value = err
  }
}

usePolling(loadSystem, METRICS_POLL_MS)

// Services, security checks and notices.
const summary = ref<Overview | null>(null)
const summaryError = ref<unknown>(null)

async function loadSummary(signal?: AbortSignal) {
  try {
    summary.value = await fetchOverview(signal)
    summaryError.value = null
  } catch (err) {
    if (!isAbortError(err)) summaryError.value = err
  }
}

usePolling(loadSummary, SUMMARY_POLL_MS)

function retrySummary() {
  summaryError.value = null
  void loadSummary()
}

function retrySystem() {
  systemError.value = null
  void loadSystem()
}

const status = computed(() => {
  const host = system.value?.host
  if (!host) return ''
  return t('overview.status', {
    host: host.hostname,
    os: host.os,
    uptime: formatDuration(host.uptimeSeconds, 1),
  })
})

const notices = computed(() =>
  (summary.value?.notices ?? []).flatMap((notice) => {
    const text = noticeText(notice)
    return text ? [{ notice, text, to: findingLocation(notice, navRoutes.value) }] : []
  }),
)

const showSites = computed(() => navRoutes.value.has('sites') && session.can('sites.read'))
const canCreateSite = computed(() => showSites.value && session.can('sites.manage'))
const showTerminal = computed(() => navRoutes.value.has('terminal'))
const showMetrics = computed(() => session.can('system.read'))
</script>

<template>
  <div>
    <PageHeader
      :title="t('dashboard.title')"
      :status="status"
      :status-loading="showMetrics && !system && !systemError"
    >
      <template #actions>
        <Button v-if="showTerminal" class="max-md:h-11" as-child variant="outline">
          <RouterLink :to="{ name: 'terminal' }">
            <SquareTerminal aria-hidden="true" />
            {{ t('overview.openTerminal') }}
          </RouterLink>
        </Button>
        <Button v-if="canCreateSite" class="max-md:h-11" as-child>
          <RouterLink :to="{ name: 'sites', query: { new: '1' } }">
            <Plus aria-hidden="true" />
            {{ t('sites.create.title') }}
          </RouterLink>
        </Button>
      </template>
    </PageHeader>

    <div class="flex flex-col gap-4 lg:gap-5">
      <!-- The two-step verification reminder appears here only (a dot on the avatar elsewhere). -->
      <div class="flex flex-col gap-2 empty:hidden">
        <TotpNotice class="mb-0" />
        <Notice
          v-for="(item, index) in notices"
          :key="`${item.notice.code}-${index}`"
          :tone="item.notice.status === 'bad' ? 'destructive' : 'warning'"
        >
          {{ item.text }}
          <template v-if="item.to" #action>
            <RouterLink
              :to="item.to"
              class="inline-flex items-center gap-1 whitespace-nowrap text-current underline underline-offset-2 hover:no-underline"
            >
              {{ noticeAction(item.notice) }}
              <ArrowRight class="size-3.5 rtl:rotate-180" aria-hidden="true" />
            </RouterLink>
          </template>
        </Notice>
      </div>

      <p v-if="summary && summary.unavailable.length > 0" class="m-0 text-detail text-muted-foreground">
        {{ t('overview.unavailable', { modules: moduleNames(summary.unavailable) }) }}
      </p>

      <MetricCards
        v-if="showMetrics"
        :host="system?.host ?? null"
        :current="system?.current ?? null"
        :history="history"
        :error="systemError"
        @retry="retrySystem"
      />

      <div class="grid grid-cols-[repeat(auto-fit,minmax(min(380px,100%),1fr))] gap-4 lg:gap-5">
        <SitesCard v-if="showSites" :can-create="canCreateSite" />
        <ServicesCard
          :services="summary?.services ?? null"
          :error="summaryError"
          :software-link="navRoutes.has('software')"
          @retry="retrySummary"
        />
        <RecentJobsCard />
        <SecurityCard :checks="summary?.checks ?? null" :error="summaryError" @retry="retrySummary" />
      </div>
    </div>
  </div>
</template>
