<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { ArrowRight, Archive, Play } from '@lucide/vue'
import { fetchPlans, runPlan, type BackupPlan } from '@/api/backups'
import type { SiteView } from '@/api/sites'
import Card from '@/components/common/Card.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import SkeletonRows from '@/components/common/SkeletonRows.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { useNavRoutes } from '@/composables/useNavRoutes'
import { errorText } from '@/i18n/errors'
import { formatBytes } from '@/i18n/format'
import { launchJob } from '@/jobs/dock'
import { backupStatus } from '@/sites/display'
import { useSessionStore } from '@/stores/session'

// The backup plans that include the site, with their last run. Plans are
// set up on the backups page.
const props = defineProps<{
  site: SiteView
  /** The site list reports backups. */
  backupsKnown: boolean
}>()

const { t } = useI18n()
const session = useSessionStore()
const navRoutes = useNavRoutes()

const canRead = computed(() => navRoutes.value.has('backups') && session.can('backups.read'))
const canRun = computed(() => canRead.value && session.can('backups.manage'))

const plans = ref<BackupPlan[] | null>(null)
const error = ref<unknown>(null)
const running = ref<string | null>(null)

async function load() {
  if (!canRead.value) return
  error.value = null
  try {
    const all = await fetchPlans()
    plans.value = all.filter((plan) =>
      plan.targets.some((target) => target.kind === 'site' && target.site === props.site.code),
    )
  } catch (err) {
    error.value = err
  }
}

onMounted(load)

async function run(plan: BackupPlan) {
  running.value = plan.id
  await launchJob(() => runPlan(plan.id), { onFinished: () => void load() })
  running.value = null
}

const summary = computed(() => backupStatus(props.site.backup, props.backupsKnown))

function runError(plan: BackupPlan): string {
  const err = plan.lastRun?.error
  return err ? (errorText(err.code, err.params ?? {}) ?? err.code) : ''
}
</script>

<template>
  <p class="m-0 text-muted-foreground">{{ t('sites.backup.intro') }}</p>

  <Card v-if="canRead" :title="t('sites.backup.plans')" :level="3" flush>
    <template #actions>
      <RouterLink :to="{ name: 'backups' }" class="inline-flex items-center gap-1 text-detail font-medium text-link hover:underline tap-y">
        {{ t('common.nav.backups') }}
        <ArrowRight class="size-3.5 rtl:rotate-180" aria-hidden="true" />
      </RouterLink>
    </template>
    <SkeletonRows v-if="!plans && !error" two-line :rows="2" />
    <ErrorState v-else-if="error" :error="error" compact @retry="load" />
    <EmptyState v-else-if="plans && plans.length === 0" :icon="Archive" compact :title="t('sites.backup.none')" :text="t('sites.backup.noneText')" />
    <ul v-else class="m-0 list-none divide-y p-0">
      <li
        v-for="plan in plans"
        :key="plan.id"
        class="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3 md:px-[18px]"
      >
        <div class="flex min-w-0 flex-[1_1_220px] flex-col gap-0.5">
          <span class="font-medium">{{ plan.name }}</span>
          <span class="flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground">
            <template v-if="plan.lastRun">
              <RelativeTime :value="plan.lastRun.finished ?? plan.lastRun.started" />
              <span v-if="plan.lastRun.ok && plan.lastRun.size">· {{ formatBytes(plan.lastRun.size) }}</span>
              <span v-if="!plan.lastRun.ok && runError(plan)" class="text-destructive">· {{ runError(plan) }}</span>
            </template>
            <span v-else>{{ t('sites.backup.never') }}</span>
          </span>
        </div>
        <StatusPill v-if="plan.lastRun" :tone="plan.lastRun.ok ? 'success' : 'destructive'">
          {{ plan.lastRun.ok ? t('sites.backup.ok') : t('sites.backup.failed') }}
        </StatusPill>
        <StatusPill v-else tone="warning">{{ t('sites.backup.never') }}</StatusPill>
        <Button v-if="canRun" variant="outline" size="sm" :disabled="running === plan.id" @click="run(plan)">
          <Play aria-hidden="true" />
          {{ t('sites.backup.runNow') }}
        </Button>
      </li>
    </ul>
  </Card>

  <Card v-else :title="t('sites.backup.summary')" :level="3" flush>
    <KeyValueList>
      <KeyValueRow :label="t('sites.list.backup')">
        <span v-if="!summary" class="text-muted-foreground">—</span>
        <template v-else>
          <StatusPill v-if="summary.text" :tone="summary.tone">{{ summary.text }}</StatusPill>
          <RelativeTime v-if="summary.at" :value="summary.at" />
        </template>
      </KeyValueRow>
      <KeyValueRow v-if="site.backup" :label="t('sites.backup.plans')">
        {{ t('sites.backup.planCount', { count: site.backup.plans.length }, site.backup.plans.length) }}
      </KeyValueRow>
    </KeyValueList>
  </Card>
</template>
