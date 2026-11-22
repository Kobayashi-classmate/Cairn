<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { ExternalLink } from '@lucide/vue'
import type { SiteLogKind } from '@/api/sites'
import SiteLog from '@/components/logs/SiteLog.vue'
import { useSessionStore } from '@/stores/session'

// The site's nginx access and error logs, followed while open.
defineProps<{ code: string }>()

const { t } = useI18n()
const session = useSessionStore()
const kind = ref<SiteLogKind>('access')
const follow = ref(true)
</script>

<template>
  <SiteLog v-model:kind="kind" v-model:follow="follow" :code="code">
    <template v-if="session.can('logs.read')" #controls>
      <RouterLink
        :to="{ name: 'logs', query: { tab: 'sites', site: code, kind } }"
        class="order-last ms-auto inline-flex items-center gap-1.5 text-detail font-medium text-link underline-offset-4 hover:underline max-md:min-h-11"
      >
        <ExternalLink class="size-3.5" aria-hidden="true" />
        {{ t('logs.openInLogs') }}
      </RouterLink>
    </template>
  </SiteLog>
</template>
