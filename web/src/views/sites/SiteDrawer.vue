<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Archive,
  ArrowLeftRight,
  Box,
  ExternalLink,
  FileCode,
  FolderTree,
  Globe,
  LayoutDashboard,
  Lock,
  Pause,
  Play,
  ScrollText,
  Shuffle,
  Trash2,
  Waypoints,
} from '@lucide/vue'
import type { SiteView } from '@/api/sites'
import SettingsDrawer from '@/components/common/SettingsDrawer.vue'
import type { DrawerItem } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { useCertChoices } from '@/certs/useCertChoices'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { siteDisplayState } from '@/sites/labels'
import { SITE_STATE_TONE, siteStateText, siteTypeText, siteUrl } from '@/sites/display'
import { useInstalledRuntimes } from '@/sites/useRuntimes'
import { useSessionStore } from '@/stores/session'
import { useSitesStore } from '@/stores/sites'
import { askDeleteSite, toggleSite } from './actions'
import SiteAppPane from './drawer/SiteAppPane.vue'
import SiteBackupPane from './drawer/SiteBackupPane.vue'
import SiteDirsPane from './drawer/SiteDirsPane.vue'
import SiteDomainsPane from './drawer/SiteDomainsPane.vue'
import SiteHttpsPane from './drawer/SiteHttpsPane.vue'
import SiteLogsPane from './drawer/SiteLogsPane.vue'
import SiteOverviewPane from './drawer/SiteOverviewPane.vue'
import SitePhpPane from './drawer/SitePhpPane.vue'
import SiteProxyPane from './drawer/SiteProxyPane.vue'
import SiteRewritePane from './drawer/SiteRewritePane.vue'

// A site's settings drawer (interface spec, section 6). The sub-items are
// the ones the site's type has; each saves the whole site as a job.
type Section = 'overview' | 'domains' | 'https' | 'php' | 'rewrite' | 'proxy' | 'app' | 'dirs' | 'logs' | 'backup'

const { t } = useI18n()
const store = useSitesStore()
const session = useSessionStore()
const drawer = useDrawerRoute('site')
const { certs, reload: reloadCerts } = useCertChoices()
const { runtimes } = useInstalledRuntimes()

/** A site not (yet) in the list, fetched on its own. */
const fetched = ref<SiteView | null>(null)
const fetchError = ref<unknown>(null)

const site = computed<SiteView | null>(() => {
  const code = drawer.id.value
  if (!code) return null
  return store.sites.find((s) => s.code === code) ?? (fetched.value?.code === code ? fetched.value : null)
})

async function fetchMissing() {
  const code = drawer.id.value
  if (!code || store.sites.some((s) => s.code === code)) return
  fetchError.value = null
  try {
    const found = await store.site(code)
    // Another site may be open by now.
    if (drawer.id.value === code) fetched.value = found
  } catch (err) {
    if (drawer.id.value === code) fetchError.value = err
  }
}

watch(
  () => [drawer.id.value, store.list, store.loadError] as const,
  ([code, list, listError]) => {
    // Wait for the list first: most sites are in it.
    if (code && (list || listError) && !site.value) void fetchMissing()
  },
  { immediate: true },
)

watch(
  () => drawer.id.value,
  (code) => {
    if (code) void reloadCerts()
  },
)

const canManage = computed(() => session.can('sites.manage'))

const TYPE_ICON: Record<string, Component> = { static: Globe, php: FileCode, proxy: ArrowLeftRight, app: Box }

const items = computed<DrawerItem<Section>[]>(() => {
  const type = site.value?.type
  const list: DrawerItem<Section>[] = [
    { value: 'overview', label: t('sites.tabs.overview'), icon: LayoutDashboard },
    { value: 'domains', label: t('sites.tabs.domains'), icon: Globe },
    { value: 'https', label: t('sites.tabs.https'), icon: Lock },
  ]
  if (type === 'php') list.push({ value: 'php', label: t('sites.tabs.php'), icon: FileCode })
  if (type === 'php' || type === 'static') list.push({ value: 'rewrite', label: t('sites.tabs.rewrite'), icon: Shuffle })
  if (type === 'proxy') list.push({ value: 'proxy', label: t('sites.tabs.proxy'), icon: Waypoints })
  if (type === 'app') list.push({ value: 'app', label: t('sites.tabs.app'), icon: Box })
  if (type !== 'proxy') list.push({ value: 'dirs', label: t('sites.tabs.dirs'), icon: FolderTree })
  list.push({ value: 'logs', label: t('sites.tabs.logs'), icon: ScrollText })
  list.push({ value: 'backup', label: t('sites.tabs.backup'), icon: Archive })
  return list
})

const dangerItems = computed(() =>
  canManage.value ? [{ value: 'delete', label: t('sites.action.delete'), icon: Trash2 }] : [],
)

const section = computed<Section>(() => {
  const wanted = drawer.section.value
  return items.value.find((item) => item.value === wanted)?.value ?? 'overview'
})

const status = computed(() =>
  site.value ? { tone: SITE_STATE_TONE[siteDisplayState(site.value)], label: siteStateText(site.value) } : null,
)

const meta = computed(() => {
  const s = site.value
  if (!s) return ''
  return [siteTypeText(s), s.dir, t('sites.drawer.runner', { runner: s.runner })].join(' · ')
})

const visitUrl = computed(() => (site.value ? siteUrl(site.value, site.value.domains[0]) : null))

const loading = computed(() => drawer.id.value !== null && !site.value && !fetchError.value)
const error = computed(() => (site.value ? null : fetchError.value))

async function onDanger() {
  const current = site.value
  if (!current) return
  if (await askDeleteSite(current)) drawer.close()
}

function retry() {
  void store.load()
  void fetchMissing()
}
</script>

<template>
  <SettingsDrawer
    :open="drawer.id.value !== null"
    :title="site ? (site.domains[0] ?? site.code) : (drawer.id.value ?? '')"
    :icon="site ? TYPE_ICON[site.type] : Globe"
    :status="status"
    :meta="meta"
    :items="items"
    :danger-items="dangerItems"
    :section="section"
    :loading="loading"
    :error="error"
    @close="drawer.close()"
    @update:section="drawer.setSection"
    @danger="onDanger"
    @retry="retry"
  >
    <template v-if="site" #actions>
      <Button v-if="visitUrl" as-child variant="outline" size="sm" class="max-md:h-11">
        <a :href="visitUrl" target="_blank" rel="noopener noreferrer">
          {{ t('sites.action.visit') }}
          <ExternalLink aria-hidden="true" />
        </a>
      </Button>
      <Button
        v-if="canManage"
        variant="outline"
        size="sm"
        class="max-md:h-11"
        :disabled="site.state === 'applying'"
        @click="toggleSite(site)"
      >
        <component :is="site.enabled ? Pause : Play" aria-hidden="true" />
        {{ site.enabled ? t('sites.action.stop') : t('sites.action.start') }}
      </Button>
    </template>

    <template v-if="site">
      <SiteOverviewPane
        v-if="section === 'overview'"
        :site="site"
        :can-manage="canManage"
        :backups-known="store.list?.backups === true"
        :rewrites="store.rewrites"
        @go="drawer.setSection"
      />
      <SiteDomainsPane v-else-if="section === 'domains'" :site="site" :can-manage="canManage" :certs="certs" />
      <SiteHttpsPane v-else-if="section === 'https'" :site="site" :can-manage="canManage" :certs="certs" />
      <SitePhpPane v-else-if="section === 'php'" :site="site" :can-manage="canManage" :php-lines="store.list?.php ?? []" />
      <SiteRewritePane v-else-if="section === 'rewrite'" :site="site" :can-manage="canManage" :rewrites="store.rewrites" />
      <SiteProxyPane v-else-if="section === 'proxy'" :site="site" :can-manage="canManage" />
      <SiteAppPane v-else-if="section === 'app'" :site="site" :can-manage="canManage" :runtimes="runtimes" />
      <SiteDirsPane v-else-if="section === 'dirs'" :site="site" :can-manage="canManage" />
      <SiteLogsPane v-else-if="section === 'logs'" :key="site.code" :code="site.code" />
      <SiteBackupPane v-else-if="section === 'backup'" :site="site" :backups-known="store.list?.backups === true" />
    </template>
  </SettingsDrawer>
</template>
