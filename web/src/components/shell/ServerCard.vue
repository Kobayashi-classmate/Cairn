<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Copy, Server } from '@lucide/vue'
import { fetchSystemHost, type HostSummary } from '@/api/system'
import { copyText } from '@/lib/notify'
import { useSessionStore } from '@/stores/session'

// The server this panel manages: host name, its primary address and the
// operating system, from the light host endpoint (only for users who may
// read the system status).
const { t } = useI18n()
const session = useSessionStore()
const host = ref<HostSummary | null>(null)

onMounted(async () => {
  if (!session.can('system.read')) return
  try {
    host.value = await fetchSystemHost()
  } catch (err) {
    // The card is informational; the overview page reports errors.
    console.warn('cairn: failed to load host information', err)
  }
})

const address = computed(() => host.value?.addresses[0] ?? '')
/** Every address, one per line, for the tooltip. */
const allAddresses = computed(() => host.value?.addresses.join('\n') ?? '')
</script>

<template>
  <section
    v-if="host"
    :aria-label="t('shell.server')"
    class="flex items-center gap-2.5 rounded-md border border-sidebar-border bg-card py-2 ps-2.5 pe-1 text-card-foreground"
  >
    <Server class="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
    <span class="flex min-w-0 flex-1 flex-col leading-tight">
      <span class="truncate text-detail font-semibold" :title="host.hostname">{{ host.hostname }}</span>
      <span
        v-if="address"
        class="truncate font-mono text-xs text-sidebar-foreground"
        dir="ltr"
        :title="allAddresses"
      >
        {{ address }}
      </span>
      <span v-if="host.os" class="truncate text-xs text-muted-foreground" :title="host.os">{{ host.os }}</span>
    </span>
    <button
      v-if="address"
      type="button"
      class="inline-flex size-7 shrink-0 items-center justify-center rounded-sm text-muted-foreground outline-none transition-colors hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 max-md:size-11"
      :aria-label="t('shell.copyAddress', { address })"
      :title="t('shell.copyAddress', { address })"
      @click="copyText(address)"
    >
      <Copy class="size-3.5" aria-hidden="true" />
    </button>
  </section>
</template>
