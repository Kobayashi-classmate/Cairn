<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, useRouter, type LocationQuery } from 'vue-router'
import { Database, Plus } from '@lucide/vue'
import Card from '@/components/common/Card.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import Notice from '@/components/common/Notice.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useNavRoutes } from '@/composables/useNavRoutes'
import { TAB_QUERY, withoutDrawer } from '@/composables/usePageTab'
import { productName } from '@/databases/labels'
import { useDatabasesStore } from '@/stores/databases'
import { useSessionStore } from '@/stores/session'
import { useSoftwareStore } from '@/stores/software'
import EngineCards from './databases/EngineCards.vue'
import EnginePanel from './databases/EnginePanel.vue'
import SoftwareOverlays from './software/SoftwareOverlays.vue'

// The databases page: one service card per installed server on top (the
// selected card is the page's tab, ?tab=<engine id>), and below it the
// selected server's databases, users and dumps or Redis settings and keys.
// "Service settings" opens the server's software drawer over this page.
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const store = useDatabasesStore()
const session = useSessionStore()
const navRoutes = useNavRoutes()

const canManage = computed(() => session.can('databases.manage'))
const status = computed(() => store.status)
const engines = computed(() => store.engines)

/** Old addresses (?engine=x&tab=dumps) name the engine and the sub-tab differently. */
function migrateQuery() {
  const engine = route.query.engine
  if (typeof engine !== 'string') return
  const query: LocationQuery = { ...route.query, [TAB_QUERY]: engine }
  delete query.engine
  const old = route.query[TAB_QUERY]
  if (old === 'dumps' || old === 'users') query.view = old
  void router.replace({ query })
}

onMounted(() => {
  migrateQuery()
  void store.load()
})

// The software drawer opened here starts, stops, installs and reconfigures
// the servers; the catalog reloads after each of those jobs, and so do the servers.
const software = useSoftwareStore()
watch(
  () => software.catalog,
  (_now, before) => {
    if (before) void store.load()
  },
)

const selectedId = computed(() => {
  const wanted = route.query[TAB_QUERY]
  return typeof wanted === 'string' && engines.value.some((e) => e.id === wanted) ? wanted : engines.value[0]?.id
})
const selected = computed(() => (selectedId.value ? store.engine(selectedId.value) : null))

/** A card's link: the same page with that engine, its first sub-tab, no drawer. */
function engineLocation(id: string) {
  const query = withoutDrawer(route.query)
  delete query.view
  query[TAB_QUERY] = id
  return { query }
}

const headerStatus = computed(() => {
  const s = status.value
  if (!s || !s.supported) return ''
  const total = s.engines.length
  const running = s.engines.filter((e) => e.running).length
  if (total === 0) return t('databases.status.none')
  return `${t('databases.status.count', { count: total }, total)} · ${t('databases.status.running', { count: running }, running)}`
})

/** Installed servers the panel cannot manage here yet, by product name. */
const unmanaged = computed(() => (status.value?.unmanaged ?? []).map(productName).join(t('databases.listSeparator')))
const softwareLink = computed(() => (navRoutes.value.has('software') ? { name: 'software', query: { tab: 'database' } } : null))
</script>

<template>
  <div>
    <PageHeader :title="t('databases.title')" :status="headerStatus" :status-loading="!status && !store.loadError">
      <template v-if="softwareLink && session.can('software.manage')" #actions>
        <Button as-child variant="outline" class="max-md:h-11">
          <RouterLink :to="softwareLink">
            <Plus aria-hidden="true" />
            {{ t('databases.installServer') }}
          </RouterLink>
        </Button>
      </template>
    </PageHeader>

    <div class="flex flex-col gap-5">
      <Card v-if="!status && store.loadError" flush>
        <ErrorState :error="store.loadError" @retry="store.load()" />
      </Card>
      <template v-else-if="!status">
        <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-3" aria-hidden="true">
          <Skeleton v-for="index in 2" :key="index" class="h-[132px] rounded-lg" />
        </div>
        <Skeleton class="h-72 w-full rounded-lg" />
      </template>
      <template v-else>
        <Notice v-if="!status.supported" tone="warning">
          {{ status.reason === 'platform' ? t('databases.unsupported.platform') : t('errors.db.unsupported') }}
        </Notice>
        <template v-else>
          <Notice v-if="store.loadError" tone="destructive">
            {{ t('common.state.refreshFailed') }}
            <template #action>
              <button type="button" class="underline underline-offset-2 hover:no-underline" @click="store.load()">{{ t('common.action.retry') }}</button>
            </template>
          </Notice>
          <Notice v-if="!status.statusAvailable" tone="info">{{ t('databases.statusUnavailable') }}</Notice>
          <Notice v-if="unmanaged" tone="info">{{ t('databases.unmanaged', { names: unmanaged }) }}</Notice>

          <Card v-if="engines.length === 0" flush>
            <EmptyState :icon="Database" :title="t('databases.empty.title')" :text="t('databases.empty.text')">
              <template v-if="softwareLink" #actions>
                <Button as-child>
                  <RouterLink :to="softwareLink">{{ t('databases.empty.install') }}</RouterLink>
                </Button>
              </template>
            </EmptyState>
          </Card>
          <template v-else>
            <EngineCards :engines="engines" :selected="selectedId" :location="engineLocation" :can-manage="canManage" />
            <EnginePanel v-if="selected" :key="selected.id" :engine="selected" :can-manage="canManage" />
          </template>
        </template>
      </template>
    </div>

    <SoftwareOverlays />
  </div>
</template>
