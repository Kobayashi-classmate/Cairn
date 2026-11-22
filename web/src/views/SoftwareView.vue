<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Ellipsis, RefreshCw, Wand2, Zap } from '@lucide/vue'
import Notice from '@/components/common/Notice.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import type { PageTab } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { usePageTab } from '@/composables/usePageTab'
import { SOFTWARE_TABS, inTab, lineKey, type SoftwareTab } from '@/software/display'
import { useSessionStore } from '@/stores/session'
import { useSoftwareStore } from '@/stores/software'
import { refreshIndexes } from './software/actions'
import SoftwareOverlays from './software/SoftwareOverlays.vue'
import SoftwareTable from './software/SoftwareTable.vue'
import SourcePolicySheet from './software/SourcePolicySheet.vue'

// The software page ("list" skeleton, interface spec section 6): tabs by
// kind with counts and one table of products. Installing opens a form
// sheet; an installed line's settings open in a drawer kept in the address.
const { t } = useI18n()
const router = useRouter()
const store = useSoftwareStore()
const session = useSessionStore()
const drawer = useDrawerRoute('software')
const { tab, tabLocation } = usePageTab(SOFTWARE_TABS, 'all')

const canManage = computed(() => session.can('software.manage'))
const catalog = computed(() => store.catalog)
const showPolicy = ref(false)
const refreshing = ref(false)

onMounted(() => {
  void store.load()
  void store.discover()
})

const tabs = computed<PageTab<SoftwareTab>[]>(() =>
  SOFTWARE_TABS.map((value) => ({
    value,
    label: t(`software.pageTabs.${value}`),
    count: catalog.value ? store.software.filter((entry) => inTab(entry, value)).length : undefined,
  })),
)

const rows = computed(() => store.software.filter((entry) => inTab(entry, tab.value)))

const status = computed(() => {
  const c = catalog.value
  if (!c) return ''
  const parts: string[] = []
  if (c.release) parts.push(`${c.release} · ${c.arch}`)
  if (c.statusAvailable) {
    const count = store.software.filter((entry) => inTab(entry, 'installed')).length
    parts.push(t('software.status.installed', { count }, count))
  }
  return parts.join(' · ')
})

const emptyTitle = computed(() => (tab.value === 'installed' ? t('software.empty.installed') : t('software.empty.title')))

async function refresh() {
  refreshing.value = true
  await refreshIndexes()
  refreshing.value = false
}

function openWizard() {
  void router.push({ name: 'welcome', query: { step: 'software' } })
}
</script>

<template>
  <div>
    <PageHeader
      :title="t('software.title')"
      :status="status"
      :status-loading="!catalog && !store.loadError"
      :tabs="tabs"
      :tab="tab"
      :tab-location="tabLocation"
    >
      <template #actions>
        <Button variant="outline" class="max-md:h-11" @click="showPolicy = true">
          <Zap aria-hidden="true" />
          {{ t('software.policy.title') }}
        </Button>
        <template v-if="canManage">
          <Button variant="outline" class="max-md:h-11" :disabled="refreshing" @click="refresh">
            <RefreshCw :class="refreshing ? 'animate-spin' : ''" aria-hidden="true" />
            {{ t('software.refresh') }}
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger as-child>
              <Button variant="outline" size="icon-lg" class="max-md:size-11" :aria-label="t('software.more')" :title="t('software.more')">
                <Ellipsis aria-hidden="true" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" class="min-w-48">
              <DropdownMenuItem class="max-md:min-h-11" @select="openWizard">
                <Wand2 aria-hidden="true" />
                {{ t('wizard.open') }}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </template>
      </template>
    </PageHeader>

    <div class="flex flex-col gap-4">
      <template v-if="catalog">
        <Notice v-if="!catalog.supported" tone="warning">{{ t('software.unsupported') }}</Notice>
        <Notice v-else-if="!catalog.statusAvailable" tone="info">{{ t('software.statusUnavailable') }}</Notice>
      </template>

      <SoftwareTable
        :software="rows"
        :loading="store.loading || (!catalog && !store.loadError)"
        :error="store.loadError"
        :can-manage="canManage"
        :empty-title="emptyTitle"
        :empty-text="tab === 'installed' ? t('software.empty.installedText') : undefined"
        @open="(software: string, line: string) => drawer.open(lineKey(software, line))"
        @install="(software: string) => store.openInstall(software)"
        @retry="store.load()"
      />
    </div>

    <SoftwareOverlays />
    <SourcePolicySheet v-model:open="showPolicy" />
  </div>
</template>
