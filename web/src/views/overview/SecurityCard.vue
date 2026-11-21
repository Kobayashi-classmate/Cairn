<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { ChevronRight, ShieldCheck } from '@lucide/vue'
import type { OverviewCheck } from '@/api/overview'
import Card from '@/components/common/Card.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import SkeletonRows from '@/components/common/SkeletonRows.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { useNavRoutes } from '@/composables/useNavRoutes'
import { CHECK_TONE, checkRows, checkStatusLabel, findingLocation } from './labels'

// How the firewall, SSH, the panel certificate and sign-in stand, with the
// number of findings to look at in the header.
const props = defineProps<{
  checks: readonly OverviewCheck[] | null
  error: unknown
}>()

const emit = defineEmits<{ retry: [] }>()

const { t } = useI18n()
const navRoutes = useNavRoutes()

const rows = computed(() => (props.checks ? checkRows(props.checks) : []))
const toReview = computed(() => rows.value.filter((row) => row.check.status !== 'ok'))
const anyBad = computed(() => toReview.value.some((row) => row.check.status === 'bad'))
</script>

<template>
  <Card :title="t('overview.security.title')" flush>
    <template v-if="checks && rows.length > 0" #actions>
      <StatusPill v-if="toReview.length > 0" :tone="anyBad ? 'destructive' : 'warning'">
        {{ t('overview.security.toReview', { count: toReview.length }, toReview.length) }}
      </StatusPill>
      <StatusPill v-else tone="success">{{ t('overview.security.allGood') }}</StatusPill>
    </template>
    <SkeletonRows v-if="!checks && !error" two-line :rows="4" />
    <ErrorState v-else-if="error && !checks" :error="error" compact @retry="emit('retry')" />
    <EmptyState v-else-if="rows.length === 0" :icon="ShieldCheck" compact :title="t('overview.security.empty')" />
    <ul v-else class="m-0 list-none divide-y p-0">
      <li v-for="(row, index) in rows" :key="`${row.check.code}-${index}`">
        <component
          :is="findingLocation(row.check, navRoutes) ? RouterLink : 'div'"
          :to="findingLocation(row.check, navRoutes) ?? undefined"
          class="flex items-center gap-3 px-4 py-[11px] text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset md:px-[18px] [&[href]]:hover:bg-accent/50"
        >
          <span class="flex min-w-0 flex-1 flex-col">
            <span class="font-medium">{{ row.title }}</span>
            <span class="text-xs text-muted-foreground [overflow-wrap:anywhere]">{{ row.detail }}</span>
          </span>
          <StatusPill :tone="CHECK_TONE[row.check.status]">{{ checkStatusLabel(row.check.status) }}</StatusPill>
          <ChevronRight
            v-if="findingLocation(row.check, navRoutes)"
            class="size-4 shrink-0 text-muted-foreground rtl:rotate-180"
            aria-hidden="true"
          />
        </component>
      </li>
    </ul>
  </Card>
</template>
