<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { ArrowRight, Globe } from '@lucide/vue'
import { fetchSites, type SiteList, type SiteView } from '@/api/sites'
import Card from '@/components/common/Card.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import SkeletonRows from '@/components/common/SkeletonRows.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { useClock } from '@/composables/useClock'
import { siteDisplayState } from '@/sites/labels'
import { SITE_STATE_TONE, httpsStatus, siteStateText, siteTypeText } from '@/sites/display'

// The first sites with their state and HTTPS; failed ones come first.
const props = defineProps<{ canCreate: boolean }>()

/** Sites listed on the card. */
const SHOWN = 5

const { t } = useI18n()
const now = useClock()

const list = ref<SiteList | null>(null)
const error = ref<unknown>(null)

async function load() {
  error.value = null
  try {
    list.value = await fetchSites()
  } catch (err) {
    error.value = err
  }
}

onMounted(load)

const RANK = { failed: 0, applying: 1, active: 2, stopped: 3 } as const

const shown = computed<SiteView[]>(() =>
  [...(list.value?.sites ?? [])]
    .sort((a, b) => RANK[siteDisplayState(a)] - RANK[siteDisplayState(b)])
    .slice(0, SHOWN),
)
</script>

<template>
  <Card :title="t('sites.title')" flush>
    <template #actions>
      <RouterLink :to="{ name: 'sites' }" class="inline-flex items-center gap-1 text-detail font-medium text-link hover:underline tap-y">
        {{ t('overview.sites.all') }}
        <ArrowRight class="size-3.5" aria-hidden="true" />
      </RouterLink>
    </template>
    <SkeletonRows v-if="!list && !error" two-line :rows="3" />
    <ErrorState v-else-if="error && !list" :error="error" compact @retry="load" />
    <EmptyState v-else-if="shown.length === 0" :icon="Globe" compact :title="t('sites.empty.title')">
      <template v-if="props.canCreate" #actions>
        <Button as-child size="sm">
          <RouterLink :to="{ name: 'sites', query: { new: '1' } }">{{ t('sites.create.title') }}</RouterLink>
        </Button>
      </template>
    </EmptyState>
    <ul v-else class="m-0 list-none divide-y p-0">
      <li v-for="site in shown" :key="site.code" class="flex items-center gap-3 px-4 py-[11px] md:px-[18px]">
        <div class="flex min-w-0 flex-1 flex-col">
          <RouterLink
            :to="{ name: 'sites', query: { open: `site:${site.code}` } }"
            class="truncate font-medium text-foreground tap-y hover:underline"
            dir="ltr"
          >
            {{ site.domains[0] ?? site.code }}
          </RouterLink>
          <span class="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
            <span class="truncate">{{ siteTypeText(site) }}</span>
            <span aria-hidden="true">·</span>
            <StatusDot :tone="SITE_STATE_TONE[siteDisplayState(site)]">{{ siteStateText(site) }}</StatusDot>
          </span>
        </div>
        <StatusPill :tone="httpsStatus(site, now).tone">
          {{ site.tls ? t('overview.sites.https', { status: httpsStatus(site, now).text }) : httpsStatus(site, now).text }}
        </StatusPill>
      </li>
    </ul>
  </Card>
</template>
