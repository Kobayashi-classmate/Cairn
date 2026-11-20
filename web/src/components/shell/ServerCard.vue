<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Server } from '@lucide/vue'
import { fetchSystemOverview, type HostInfo } from '@/api/system'
import { useSessionStore } from '@/stores/session'

// The server this panel manages: host name and operating system, from the
// overview endpoint (only for users who may read the system status).
const { t } = useI18n()
const session = useSessionStore()
const host = ref<HostInfo | null>(null)

onMounted(async () => {
  if (!session.can('system.read')) return
  try {
    host.value = (await fetchSystemOverview()).host
  } catch (err) {
    // The card is informational; the overview page reports errors.
    console.warn('cairn: failed to load host information', err)
  }
})
</script>

<template>
  <section
    v-if="host"
    :aria-label="t('shell.server')"
    class="flex items-center gap-2.5 rounded-md border border-sidebar-border bg-card px-2.5 py-2 text-card-foreground"
  >
    <Server class="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
    <span class="flex min-w-0 flex-1 flex-col">
      <span class="truncate text-detail font-semibold" :title="host.hostname">{{ host.hostname }}</span>
      <span v-if="host.os" class="truncate text-xs text-muted-foreground" :title="host.os">{{ host.os }}</span>
    </span>
  </section>
</template>
