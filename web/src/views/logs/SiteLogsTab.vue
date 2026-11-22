<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter, type LocationQuery } from 'vue-router'
import { Globe } from '@lucide/vue'
import type { SiteLogKind } from '@/api/sites'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import SelectField from '@/components/common/SelectField.vue'
import type { SelectOption } from '@/components/common/types'
import SiteLog from '@/components/logs/SiteLog.vue'
import { Skeleton } from '@/components/ui/skeleton'
import { useSitesStore } from '@/stores/sites'

// Sites' access and error logs on the Logs page: pick a site, then the log
// (`?site=<code>&kind=access|error`).
const emit = defineEmits<{ status: [text: string] }>()

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const sites = useSitesStore()
const follow = ref(true)

onMounted(() => {
  if (!sites.list) void sites.load()
})

function queryText(name: string): string {
  const value = route.query[name]
  return typeof value === 'string' ? value : ''
}

const options = computed<SelectOption[]>(() =>
  sites.sites.map((site) => ({ value: site.code, label: site.domains[0] ? `${site.domains[0]} (${site.code})` : site.code })),
)
const site = computed(() => {
  const asked = queryText('site')
  if (asked && (!sites.list || sites.sites.some((entry) => entry.code === asked))) return asked
  return sites.sites[0]?.code ?? ''
})
const kind = computed<SiteLogKind>(() => (queryText('kind') === 'error' ? 'error' : 'access'))

function set(next: { site?: string; kind?: SiteLogKind }) {
  const query: LocationQuery = { ...route.query, site: next.site ?? site.value }
  if ((next.kind ?? kind.value) === 'error') query.kind = 'error'
  else delete query.kind
  void router.replace({ query })
}

const status = computed(() => {
  const current = sites.sites.find((entry) => entry.code === site.value)
  if (!current) return ''
  const name = current.domains[0] ?? current.code
  return `${name} · ${kind.value === 'access' ? t('sites.logs.access') : t('sites.logs.error')}`
})
watch(status, (value) => emit('status', value), { immediate: true })
</script>

<template>
  <ErrorState v-if="sites.loadError && !sites.list" :error="sites.loadError" class="rounded-lg border bg-card" @retry="sites.load()" />
  <div v-else-if="!sites.list" class="flex flex-col gap-3" aria-hidden="true">
    <Skeleton class="h-9 w-72" />
    <Skeleton class="h-80 w-full rounded-md" />
  </div>
  <EmptyState
    v-else-if="sites.sites.length === 0"
    :icon="Globe"
    class="rounded-lg border bg-card"
    :title="t('logs.sites.none')"
    :text="t('logs.sites.noneText')"
  />
  <SiteLog
    v-else
    v-model:follow="follow"
    :code="site"
    :kind="kind"
    box-class="h-[max(360px,calc(100dvh-340px))]"
    @update:kind="(value: SiteLogKind) => set({ kind: value })"
  >
    <template #controls>
      <SelectField
        :model-value="site"
        :options="options"
        :aria-label="t('common.field.site')"
        class="w-full sm:w-72"
        @update:model-value="(code: string) => set({ site: code })"
      />
    </template>
  </SiteLog>
</template>
