<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { ArrowRight, Server } from '@lucide/vue'
import type { OverviewService } from '@/api/overview'
import Card from '@/components/common/Card.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import SkeletonRows from '@/components/common/SkeletonRows.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { SERVICE_TONE, serviceName, serviceVersion } from './labels'

// Services of installed software: name, version and whether it runs.
defineProps<{
  services: readonly OverviewService[] | null
  error: unknown
  /** Link to the software page (when it is in the menu). */
  softwareLink: boolean
}>()

const emit = defineEmits<{ retry: [] }>()

const { t } = useI18n()
</script>

<template>
  <Card :title="t('overview.services.title')" flush>
    <template v-if="softwareLink" #actions>
      <RouterLink :to="{ name: 'software' }" class="inline-flex items-center gap-1 text-detail font-medium text-link hover:underline tap-y">
        {{ t('common.nav.software') }}
        <ArrowRight class="size-3.5" aria-hidden="true" />
      </RouterLink>
    </template>
    <SkeletonRows v-if="!services && !error" :rows="4" />
    <ErrorState v-else-if="error && !services" :error="error" compact @retry="emit('retry')" />
    <EmptyState
      v-else-if="!services || services.length === 0"
      :icon="Server"
      compact
      :title="t('overview.services.empty')"
    />
    <ul v-else class="m-0 list-none divide-y p-0">
      <li
        v-for="service in services"
        :key="service.unit"
        class="flex items-center gap-3 px-4 py-[11px] md:px-[18px]"
      >
        <span class="min-w-0 flex-1 truncate font-medium" :title="service.unit">{{ serviceName(service) }}</span>
        <span
          v-if="serviceVersion(service)"
          class="max-w-[40%] truncate font-mono text-xs text-muted-foreground"
          dir="ltr"
          :title="service.version"
        >
          {{ serviceVersion(service) }}
        </span>
        <StatusDot :tone="SERVICE_TONE[service.state]" class="min-w-[76px] text-detail text-muted-foreground">
          {{ t(`overview.services.state.${service.state}`) }}
        </StatusDot>
      </li>
    </ul>
  </Card>
</template>
