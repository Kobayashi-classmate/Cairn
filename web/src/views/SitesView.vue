<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NEmpty,
  NH2,
  NIcon,
  NSpace,
  NTag,
  NText,
  NTooltip,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { AddOutline } from '@vicons/ionicons5'
import { fetchRewrites, fetchSites, startSite, stopSite, type RewriteRule, type SiteList, type SiteView } from '@/api/sites'
import { useErrorToast } from '@/composables/useErrorToast'
import { errorMessage, errorText } from '@/i18n/errors'
import { siteDisplayState, siteStateTagType, siteTypeLabel } from '@/sites/labels'
import { useSessionStore } from '@/stores/session'
import CreateSiteDialog from './sites/CreateSiteDialog.vue'
import DeleteSiteDialog from './sites/DeleteSiteDialog.vue'

const { t } = useI18n()
const router = useRouter()
const session = useSessionStore()
const showError = useErrorToast()
const message = useMessage()

/** How often the list refreshes while a site change is being applied. */
const REFRESH_MS = 3000
let timer: ReturnType<typeof setTimeout> | undefined
let unmounted = false

const list = ref<SiteList | null>(null)
const rewrites = ref<RewriteRule[]>([])
const loadError = ref<unknown>(null)
const busy = ref<string | null>(null)
const showCreate = ref(false)
const deleting = ref<SiteView | null>(null)
const showDelete = ref(false)

const canManage = computed(() => session.can('sites.manage'))
/** Creating needs a supported system with nginx installed. */
const canCreate = computed(() => canManage.value && !!list.value?.supported && list.value.nginx.installed)

async function load() {
  clearTimeout(timer)
  try {
    const [sites, rules] = await Promise.all([fetchSites(), fetchRewrites()])
    list.value = sites
    rewrites.value = rules
    loadError.value = null
  } catch (err) {
    loadError.value = err
  }
  // Keep the states current while changes are being applied.
  if (!unmounted && list.value?.sites.some((site) => site.state === 'applying')) {
    timer = setTimeout(() => void load(), REFRESH_MS)
  }
}

onMounted(load)
onBeforeUnmount(() => {
  unmounted = true
  clearTimeout(timer)
})

async function toggle(site: SiteView) {
  busy.value = site.code
  try {
    if (site.enabled) await stopSite(site.code)
    else await startSite(site.code)
    message.info(t(site.enabled ? 'sites.action.stopping' : 'sites.action.starting', { site: site.code }))
    await load()
  } catch (err) {
    showError(err)
  } finally {
    busy.value = null
  }
}

function askDelete(site: SiteView) {
  deleting.value = site
  showDelete.value = true
}

function target(site: SiteView): string {
  if (site.type === 'php') return site.php ? `PHP ${site.php}` : ''
  if (site.type === 'proxy') return site.proxy?.url ?? ''
  return ''
}

const columns = computed<DataTableColumns<SiteView>>(() => [
  {
    key: 'code',
    title: t('common.field.siteCode'),
    width: 170,
    render: (site) =>
      h(RouterLink, { to: { name: 'site', params: { code: site.code } }, class: 'link' }, () => site.code),
  },
  {
    key: 'domains',
    title: t('common.field.domains'),
    minWidth: 220,
    render: (site) =>
      h(NSpace, { size: 6, align: 'center', wrap: false }, () => [
        h('span', { dir: 'ltr' }, site.domains[0] ?? ''),
        site.domains.length > 1
          ? h(NText, { depth: 3 }, () => t('sites.list.moreDomains', { count: site.domains.length - 1 }))
          : null,
      ]),
  },
  {
    key: 'type',
    title: t('common.field.type'),
    width: 130,
    render: (site) => h(NTag, { size: 'small', bordered: false }, () => siteTypeLabel(site.type)),
  },
  {
    key: 'state',
    title: t('sites.list.state'),
    width: 130,
    render: (site) => {
      const state = siteDisplayState(site)
      const tag = h(NTag, { size: 'small', bordered: false, type: siteStateTagType(state) }, () =>
        t(`sites.state.${state}`),
      )
      // A failed change names its reason on hover.
      if (state === 'failed' && site.errorCode) {
        return h(NTooltip, null, { trigger: () => tag, default: () => errorText(site.errorCode!) ?? site.errorCode })
      }
      return site.drift ? h(NSpace, { size: 4 }, () => [tag, h(NTag, { size: 'small', type: 'warning', bordered: false }, () => t('sites.drift.tag'))]) : tag
    },
  },
  {
    key: 'target',
    title: t('sites.list.target'),
    minWidth: 180,
    render: (site) => h('span', { dir: 'ltr', class: 'mono' }, target(site)),
  },
  {
    key: 'actions',
    title: '',
    width: 230,
    render: (site) =>
      h(NSpace, { size: 4, wrap: false }, () => [
        h(NButton, { size: 'small', quaternary: true, onClick: () => router.push({ name: 'site', params: { code: site.code } }) }, () =>
          t('sites.list.open'),
        ),
        canManage.value
          ? h(
              NButton,
              {
                size: 'small',
                quaternary: true,
                loading: busy.value === site.code,
                disabled: site.state === 'applying' || (busy.value !== null && busy.value !== site.code),
                onClick: () => void toggle(site),
              },
              () => (site.enabled ? t('sites.action.stop') : t('sites.action.start')),
            )
          : null,
        canManage.value
          ? h(
              NButton,
              { size: 'small', quaternary: true, type: 'error', disabled: site.state === 'applying', onClick: () => askDelete(site) },
              () => t('common.action.delete'),
            )
          : null,
      ]),
  },
])
</script>

<template>
  <div class="sites">
    <header class="sites__header">
      <NH2 class="sites__title">{{ t('sites.title') }}</NH2>
      <NButton v-if="canManage && list?.supported" type="primary" :disabled="!canCreate" @click="showCreate = true">
        <template #icon>
          <NIcon><AddOutline /></NIcon>
        </template>
        {{ t('sites.create.title') }}
      </NButton>
    </header>

    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>

    <template v-if="list">
      <NAlert v-if="!list.supported" type="warning" class="block">
        {{ list.reason === 'selinux_pending' ? t('sites.unsupported.selinux') : t('errors.site.unsupported') }}
      </NAlert>
      <template v-else>
        <NAlert v-if="!list.statusAvailable" type="info" class="block">{{ t('sites.statusUnavailable') }}</NAlert>
        <NAlert v-else-if="!list.nginx.installed" type="warning" class="block">
          {{ t('sites.nginxMissing') }}
          <RouterLink :to="{ name: 'software' }">{{ t('sites.installNginx') }}</RouterLink>
        </NAlert>
        <NAlert v-else-if="!list.nginx.running" type="warning" class="block">{{ t('sites.nginxStopped') }}</NAlert>
      </template>

      <NCard v-if="list.sites.length > 0" size="small">
        <NDataTable
          :columns="columns"
          :data="list.sites"
          :row-key="(site: SiteView) => site.code"
          :scroll-x="1000"
          size="small"
        />
      </NCard>
      <NCard v-else-if="list.supported" size="small">
        <NEmpty :description="t('sites.empty.title')" class="empty">
          <template #extra>
            <NText depth="3" tag="p" class="empty__text">{{ t('sites.empty.text') }}</NText>
            <NButton v-if="canCreate" type="primary" @click="showCreate = true">{{ t('sites.create.title') }}</NButton>
          </template>
        </NEmpty>
      </NCard>
    </template>

    <CreateSiteDialog
      v-if="list"
      v-model:show="showCreate"
      :php-lines="list.php"
      :rewrites="rewrites"
      @changed="load"
    />
    <DeleteSiteDialog v-model:show="showDelete" :site="deleting" @deleted="load" />
  </div>
</template>

<style scoped>
.sites {
  max-inline-size: 1200px;
}

.sites__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 16px;
  margin-block-end: 16px;
}

.sites__title {
  margin: 0;
}

.block {
  margin-block-end: 16px;
}

.empty {
  padding-block: 32px;
}

.empty__text {
  max-inline-size: 520px;
  margin: 0 auto 16px;
  text-align: center;
}

:deep(.link) {
  color: inherit;
  font-weight: 500;
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
}
</style>
