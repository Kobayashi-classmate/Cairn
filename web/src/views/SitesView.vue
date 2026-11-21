<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { Globe, Plus } from '@lucide/vue'
import type { SiteType } from '@/api/sites'
import type { StreamView } from '@/api/streams'
import Card from '@/components/common/Card.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Notice from '@/components/common/Notice.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import type { PageTab } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { useNavRoutes } from '@/composables/useNavRoutes'
import { usePageTab } from '@/composables/usePageTab'
import { useSessionStore } from '@/stores/session'
import { useSitesStore } from '@/stores/sites'
import SiteCreateSheet from './sites/SiteCreateSheet.vue'
import SiteDrawer from './sites/SiteDrawer.vue'
import SiteTable from './sites/SiteTable.vue'
import StreamSheet from './sites/StreamSheet.vue'
import StreamTable from './sites/StreamTable.vue'

// The sites page ("list" skeleton, interface spec section 6): tabs by type
// with counts, the sites table, and the four-layer proxies in a tab of
// their own. A site's settings open in a drawer kept in the address.
type Tab = 'all' | SiteType | 'stream'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const store = useSitesStore()
const session = useSessionStore()
const navRoutes = useNavRoutes()
const drawer = useDrawerRoute('site')

const list = computed(() => store.list)
const supported = computed(() => list.value?.supported !== false)

const TABS = computed<Tab[]>(() =>
  supported.value ? ['all', 'php', 'app', 'static', 'proxy', 'stream'] : ['all', 'php', 'app', 'static', 'proxy'],
)
const { tab, tabLocation } = usePageTab(TABS, 'all')

const canManage = computed(() => session.can('sites.manage'))
const nginxInstalled = computed(() => list.value?.nginx.installed === true)
/** Creating needs a supported system with nginx installed. */
const canCreate = computed(() => canManage.value && supported.value && nginxInstalled.value)

const showCreate = ref(false)
/** The "New site" sheet loads runtimes and certificates; it is mounted when first opened. */
const createUsed = ref(false)
watch(showCreate, (open) => {
  if (open) createUsed.value = true
})
const showStream = ref(false)
const editingStream = ref<StreamView | null>(null)

onMounted(() => {
  store.watch()
  void store.load()
  void store.loadStreams()
})
onBeforeUnmount(() => store.unwatch())

// Links from elsewhere (the overview) open the "New site" sheet with ?new=1.
watch(
  () => [route.query.new, list.value] as const,
  ([wanted]) => {
    if (wanted !== '1' || !list.value) return
    if (canCreate.value) showCreate.value = true
    const query = { ...route.query }
    delete query.new
    void router.replace({ query })
  },
  { immediate: true },
)

const counts = computed(() => {
  const sites = store.sites
  return {
    all: sites.length,
    php: sites.filter((s) => s.type === 'php').length,
    app: sites.filter((s) => s.type === 'app').length,
    static: sites.filter((s) => s.type === 'static').length,
    proxy: sites.filter((s) => s.type === 'proxy').length,
    stream: store.streams?.length,
  }
})

const tabs = computed<PageTab<Tab>[]>(() =>
  TABS.value.map((value) => ({
    value,
    label: t(`sites.pageTabs.${value}`),
    count: list.value ? counts.value[value] : undefined,
  })),
)

const filtered = computed(() => (tab.value === 'all' ? store.sites : store.sites.filter((s) => s.type === tab.value)))

const status = computed(() => {
  const l = list.value
  if (!l) return ''
  if (!l.supported) return t('sites.status.unsupported')
  const sites = t('sites.status.count', { count: l.sites.length }, l.sites.length)
  if (!l.statusAvailable) return sites
  const nginx = !l.nginx.installed
    ? t('sites.status.nginxMissing')
    : l.nginx.running
      ? t('sites.status.nginxRunning')
      : t('sites.status.nginxStopped')
  return `${sites} · ${nginx}`
})

const emptyTitle = computed(() => (tab.value === 'all' ? t('sites.empty.title') : t(`sites.empty.byType.${tab.value}`)))

function openCreate() {
  showCreate.value = true
}

function openStreamCreate() {
  editingStream.value = null
  showStream.value = true
}

function openStreamEdit(stream: StreamView) {
  editingStream.value = stream
  showStream.value = true
}

const unsupportedText = computed(() =>
  list.value?.reason === 'selinux_pending' ? t('sites.unsupported.selinux') : t('errors.site.unsupported'),
)
</script>

<template>
  <div>
    <PageHeader
      :title="t('sites.title')"
      :status="status"
      :status-loading="!list && !store.loadError"
      :tabs="supported ? tabs : []"
      :tab="tab"
      :tab-location="tabLocation"
    >
      <template v-if="canManage && supported" #actions>
        <Button v-if="tab === 'stream'" class="max-md:h-11" @click="openStreamCreate">
          <Plus aria-hidden="true" />
          {{ t('streams.dialog.createTitle') }}
        </Button>
        <Button v-else class="max-md:h-11" :disabled="!canCreate" @click="openCreate">
          <Plus aria-hidden="true" />
          {{ t('sites.create.title') }}
        </Button>
      </template>
    </PageHeader>

    <div class="flex flex-col gap-4">
      <template v-if="list">
        <Notice v-if="!list.supported" tone="warning">{{ unsupportedText }}</Notice>
        <template v-else>
          <Notice v-if="!list.statusAvailable" tone="info">{{ t('sites.statusUnavailable') }}</Notice>
          <Notice v-else-if="!list.nginx.installed && store.sites.length > 0" tone="warning">
            {{ t('sites.nginxMissing') }}
            <template v-if="navRoutes.has('software')" #action>
              <RouterLink :to="{ name: 'software' }" class="text-current underline underline-offset-2">
                {{ t('sites.installNginx') }}
              </RouterLink>
            </template>
          </Notice>
          <Notice v-else-if="list.nginx.installed && !list.nginx.running" tone="warning">{{ t('sites.nginxStopped') }}</Notice>
          <Notice v-if="tab === 'app' && !list.systemd" tone="info">{{ t('sites.app.noSystemdNotice') }}</Notice>
        </template>
      </template>

      <template v-if="supported">
        <StreamTable
          v-if="tab === 'stream'"
          :streams="store.streams ?? []"
          :loading="store.streamsLoading"
          :error="store.streamsError"
          :can-manage="canManage"
          @edit="openStreamEdit"
          @create="openStreamCreate"
          @retry="store.loadStreams()"
        />
        <Card v-else-if="list && list.statusAvailable && !list.nginx.installed && store.sites.length === 0" flush>
          <EmptyState :icon="Globe" :title="t('sites.nginxMissing')" :text="t('sites.empty.nginxText')">
            <template v-if="navRoutes.has('software')" #actions>
              <Button as-child>
                <RouterLink :to="{ name: 'software' }">{{ t('sites.installNginx') }}</RouterLink>
              </Button>
            </template>
          </EmptyState>
        </Card>
        <SiteTable
          v-else
          :sites="filtered"
          :loading="store.loading || (!list && !store.loadError)"
          :error="store.loadError"
          :backups-known="list?.backups === true"
          :can-manage="canManage"
          :can-create="canCreate"
          :empty-title="emptyTitle"
          :empty-text="tab === 'all' ? t('sites.empty.text') : undefined"
          @open="(code: string) => drawer.open(code)"
          @create="openCreate"
          @retry="store.load()"
        />
      </template>
    </div>

    <SiteDrawer />
    <SiteCreateSheet v-if="createUsed" v-model:open="showCreate" />
    <StreamSheet v-model:open="showStream" :stream="editingStream" />
  </div>
</template>
