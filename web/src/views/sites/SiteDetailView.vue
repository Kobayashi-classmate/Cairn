<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NCard,
  NH2,
  NIcon,
  NSpin,
  NTabPane,
  NTabs,
  NTag,
  NText,
} from 'naive-ui'
import { ArrowBackOutline } from '@vicons/ionicons5'
import { isApiError, isStepUpRequired } from '@/api/client'
import type { Job } from '@/api/jobs'
import {
  fetchRewrites,
  fetchSite,
  fetchSites,
  requestOf,
  startSite,
  stopSite,
  updateSite,
  type RewriteRule,
  type SiteView,
} from '@/api/sites'
import { useCertChoices } from '@/certs/useCertChoices'
import { useInstalledRuntimes } from '@/sites/useRuntimes'
import AppProcess from './AppProcess.vue'
import JobProgress from '@/components/JobProgress.vue'
import { useErrorToast } from '@/composables/useErrorToast'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'
import { rewriteLabel, siteDisplayState, siteStateTagType, siteTypeLabel } from '@/sites/labels'
import { useSessionStore } from '@/stores/session'
import DeleteSiteDialog from './DeleteSiteDialog.vue'
import SiteForm from './SiteForm.vue'
import SiteLogs from './SiteLogs.vue'

/** How often the site is reloaded while a change is applied elsewhere. */
const REFRESH_MS = 3000

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const showError = useErrorToast()
const { certs } = useCertChoices()
const { runtimes } = useInstalledRuntimes()
const systemd = ref(true)

const code = computed(() => String(route.params.code ?? ''))
const site = ref<SiteView | null>(null)
const phpLines = ref<string[]>([])
const rewrites = ref<RewriteRule[]>([])
const loadError = ref<unknown>(null)
const tab = ref<'overview' | 'process' | 'settings' | 'logs'>('overview')

/** The job of the change started on this page, followed inline. */
const actionJob = ref<string | null>(null)
const starting = ref(false)
/** The inline job has not finished yet. */
const jobRunning = ref(false)
const showDelete = ref(false)
const formRef = ref<InstanceType<typeof SiteForm> | null>(null)
const formError = ref<unknown>(null)

const canManage = computed(() => session.can('sites.manage'))
const state = computed(() => (site.value ? siteDisplayState(site.value) : null))
const applying = computed(() => site.value?.state === 'applying' || jobRunning.value || starting.value)

let timer: ReturnType<typeof setTimeout> | undefined
let unmounted = false

async function load() {
  clearTimeout(timer)
  try {
    site.value = await fetchSite(code.value)
    loadError.value = null
  } catch (err) {
    loadError.value = err
  }
  // A change started elsewhere: watch it finish.
  if (!unmounted && site.value?.state === 'applying' && actionJob.value === null) {
    timer = setTimeout(() => void load(), REFRESH_MS)
  }
}

async function loadContext() {
  try {
    const [list, rules] = await Promise.all([fetchSites(), fetchRewrites()])
    phpLines.value = list.php
    systemd.value = list.systemd
    rewrites.value = rules
  } catch {
    // The settings form then offers fewer choices; the server still checks.
  }
}

watch(
  code,
  () => {
    site.value = null
    actionJob.value = null
    void load()
    void loadContext()
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  unmounted = true
  clearTimeout(timer)
})

const rewriteName = computed(() => {
  const id = site.value?.rewrite
  if (!id) return ''
  const rule = rewrites.value.find((r) => r.id === id)
  return rule ? rewriteLabel(rule) : id
})

/** Domains that can be opened in a browser (wildcards cannot), over HTTPS when the site has it. */
function domainUrl(domain: string): string | null {
  if (domain.startsWith('*.')) return null
  return `${site.value?.tls ? 'https' : 'http'}://${domain}/`
}

/** Starts a change job and follows it inline. */
async function run(action: () => Promise<Job>) {
  starting.value = true
  formError.value = null
  try {
    actionJob.value = (await action()).id
    jobRunning.value = true
  } catch (err) {
    if (isStepUpRequired(err)) return
    if (tab.value === 'settings') formError.value = err
    else showError(err)
  } finally {
    starting.value = false
  }
}

function toggle() {
  const current = site.value
  if (!current) return
  void run(() => (current.enabled ? stopSite(current.code) : startSite(current.code)))
}

/** Applies the stored spec again, for example after a failed change. */
function reapply() {
  const current = site.value
  if (current) void run(() => updateSite(current.code, requestOf(current)))
}

async function saveSettings() {
  const current = site.value
  const request = await formRef.value?.collect()
  if (!current || !request) return
  await run(() => updateSite(current.code, request))
}

function onJobFinished() {
  jobRunning.value = false
  void load()
}

function dismissJob() {
  actionJob.value = null
  jobRunning.value = false
}

function onDeleted() {
  void router.replace({ name: 'sites' })
}

const failureText = computed(() => {
  const errorCode = site.value?.errorCode
  return errorCode ? (errorText(errorCode) ?? errorCode) : ''
})

/** A certificate's name when it is in the list of choices, else its id. */
function certName(id: string): string {
  return certs.value?.find((cert) => cert.id === id)?.name ?? id
}

const notFound = computed(() => isApiError(loadError.value) && loadError.value.status === 404)
</script>

<template>
  <div class="site">
    <RouterLink :to="{ name: 'sites' }" class="site__back">
      <NIcon><ArrowBackOutline /></NIcon>
      {{ t('sites.title') }}
    </RouterLink>

    <NAlert v-if="loadError" :type="notFound ? 'warning' : 'error'" class="block">
      {{ errorMessage(loadError) }}
    </NAlert>

    <NSpin :show="!site && !loadError">
      <template v-if="site && state">
        <header class="site__header">
          <NH2 class="site__title">{{ site.code }}</NH2>
          <NTag :type="siteStateTagType(state)" :bordered="false">{{ t(`sites.state.${state}`) }}</NTag>
          <NTag :bordered="false">{{ siteTypeLabel(site.type) }}</NTag>
          <span class="site__spacer" />
          <template v-if="canManage">
            <NButton :disabled="applying" :loading="starting && tab !== 'settings'" @click="toggle">
              {{ site.enabled ? t('sites.action.stop') : t('sites.action.start') }}
            </NButton>
            <NButton type="error" ghost :disabled="applying" @click="showDelete = true">
              {{ t('common.action.delete') }}
            </NButton>
          </template>
        </header>
        <div class="site__domains">
          <template v-for="domain in site.domains" :key="domain">
            <a v-if="domainUrl(domain)" :href="domainUrl(domain)!" target="_blank" rel="noopener noreferrer" dir="ltr">
              {{ domain }}
            </a>
            <span v-else dir="ltr">{{ domain }}</span>
          </template>
        </div>

        <NCard v-if="actionJob" size="small" class="block">
          <JobProgress :job-id="actionJob" @finished="onJobFinished" />
          <div class="actions">
            <NButton size="small" quaternary @click="dismissJob">{{ t('sites.create.close') }}</NButton>
          </div>
        </NCard>

        <NAlert
          v-else-if="site.state === 'failed' || site.drift"
          type="warning"
          :title="t('sites.drift.title')"
          class="block"
        >
          <p class="para">{{ failureText || t('sites.drift.text') }}</p>
          <div class="actions actions--start">
            <NButton v-if="canManage" size="small" :loading="starting" @click="reapply">
              {{ t('sites.drift.reapply') }}
            </NButton>
            <RouterLink v-if="site.lastJob" :to="{ name: 'job', params: { id: site.lastJob } }">
              {{ t('sites.lastChange') }}
            </RouterLink>
          </div>
        </NAlert>
        <div v-else-if="site.lastJob" class="site__last">
          <RouterLink :to="{ name: 'job', params: { id: site.lastJob } }">{{ t('sites.lastChange') }}</RouterLink>
        </div>

        <NTabs v-model:value="tab" type="line">
          <NTabPane name="overview" :tab="t('sites.tabs.overview')">
            <NCard size="small">
              <dl class="facts">
                <dt>{{ t('sites.overview.dir') }}</dt>
                <dd class="mono" dir="ltr">{{ site.dir }}</dd>
                <dt>{{ t('sites.overview.owner') }}</dt>
                <dd><span class="mono" dir="ltr">{{ site.owner }}</span></dd>
                <dt>{{ t('sites.overview.runner') }}</dt>
                <dd><span class="mono" dir="ltr">{{ site.runner }}</span></dd>
                <dt>{{ t('common.field.type') }}</dt>
                <dd>{{ siteTypeLabel(site.type) }}</dd>
                <template v-if="site.php">
                  <dt>{{ t('common.field.php') }}</dt>
                  <dd>PHP {{ site.php }}</dd>
                </template>
                <template v-if="site.type === 'static' || site.type === 'php'">
                  <dt>{{ t('common.field.rewrite') }}</dt>
                  <dd>{{ rewriteName || t('sites.form.noRewrite') }}</dd>
                  <dt>{{ t('common.field.root') }}</dt>
                  <dd class="mono" dir="ltr">{{ site.root || t('sites.overview.rootDefault') }}</dd>
                </template>
                <template v-if="site.index?.length">
                  <dt>{{ t('common.field.index') }}</dt>
                  <dd class="mono" dir="ltr">{{ site.index.join(', ') }}</dd>
                </template>
                <template v-if="site.writable?.length">
                  <dt>{{ t('common.field.writable') }}</dt>
                  <dd class="mono" dir="ltr">{{ site.writable.join(', ') }}</dd>
                </template>
                <template v-if="site.app">
                  <dt>{{ t('common.field.app.runtime') }}</dt>
                  <dd>{{ site.app.runtime ? `${site.app.runtime} ${site.app.line ?? ''}` : t('sites.app.systemTools') }}</dd>
                  <dt>{{ t('common.field.app.command') }}</dt>
                  <dd class="mono" dir="ltr">{{ site.app.command }}</dd>
                  <dt>{{ t('common.field.app.workDir') }}</dt>
                  <dd class="mono" dir="ltr">{{ site.app.workDir || t('sites.overview.rootDefault') }}</dd>
                  <dt>{{ t('common.field.app.port') }}</dt>
                  <dd class="mono" dir="ltr">127.0.0.1:{{ site.app.port }}</dd>
                  <dt>{{ t('common.field.app.env') }}</dt>
                  <dd>{{ t('sites.app.envCount', { count: site.app.env?.length ?? 0 }, site.app.env?.length ?? 0) }}</dd>
                  <dt>{{ t('sites.app.limits') }}</dt>
                  <dd>
                    {{
                      t('sites.app.limitsValue', {
                        memory: site.app.memoryMb ? `${site.app.memoryMb} MB` : t('sites.app.noLimit'),
                        cpu: site.app.cpuPercent ? `${site.app.cpuPercent} %` : t('sites.app.noLimit'),
                      })
                    }}
                  </dd>
                  <template v-if="site.app.healthPath">
                    <dt>{{ t('common.field.app.healthPath') }}</dt>
                    <dd class="mono" dir="ltr">{{ site.app.healthPath }}</dd>
                  </template>
                </template>
                <template v-if="site.proxy">
                  <dt>{{ t('common.field.proxy') }}</dt>
                  <dd>
                    <span class="mono" dir="ltr">{{ site.proxy.url }}</span>
                    <NTag v-if="site.proxy.webSocket" size="small" :bordered="false" class="tag">
                      {{ t('sites.form.webSocket') }}
                    </NTag>
                    <NTag v-if="site.proxy.upstreamHost" size="small" :bordered="false" class="tag">
                      {{ t('sites.form.upstreamHost') }}
                    </NTag>
                  </dd>
                </template>
                <dt>{{ t('sites.form.https') }}</dt>
                <dd>
                  <template v-if="site.tls">
                    <RouterLink :to="{ name: 'cert', params: { id: site.tls.cert } }">{{ certName(site.tls.cert) }}</RouterLink>
                    <NTag v-if="site.tls.redirect" size="small" :bordered="false" class="tag">
                      {{ t('sites.form.redirect') }}
                    </NTag>
                    <NTag v-if="site.tls.hsts" size="small" :bordered="false" class="tag">HSTS</NTag>
                  </template>
                  <template v-else>{{ t('sites.form.noTls') }}</template>
                </dd>
                <dt>{{ t('sites.overview.created') }}</dt>
                <dd>{{ formatDateTime(site.createdAt) }}</dd>
                <dt>{{ t('sites.overview.updated') }}</dt>
                <dd>{{ formatDateTime(site.updatedAt) }}</dd>
              </dl>
              <NText depth="3" tag="p" class="explain">
                {{ t('sites.overview.users', { owner: site.owner, runner: site.runner }) }}
              </NText>
            </NCard>
          </NTabPane>

          <NTabPane v-if="site.type === 'app'" name="process" :tab="t('sites.tabs.process')">
            <AppProcess :code="site.code" :can-manage="canManage" />
          </NTabPane>

          <NTabPane v-if="canManage" name="settings" :tab="t('sites.tabs.settings')">
            <NCard size="small">
              <SiteForm
                ref="formRef"
                mode="edit"
                :initial="site"
                :php-lines="phpLines"
                :rewrites="rewrites"
                :certs="certs"
                :runtimes="runtimes"
                :systemd="systemd"
                :disabled="applying"
              />
              <NAlert v-if="formError" type="error" class="block">{{ errorMessage(formError) }}</NAlert>
              <div class="actions">
                <NButton type="primary" :loading="starting" :disabled="applying" @click="saveSettings">
                  {{ t('sites.settings.apply') }}
                </NButton>
              </div>
            </NCard>
          </NTabPane>

          <NTabPane name="logs" :tab="t('sites.tabs.logs')">
            <NCard size="small">
              <SiteLogs :code="site.code" />
            </NCard>
          </NTabPane>
        </NTabs>
      </template>
    </NSpin>

    <DeleteSiteDialog v-model:show="showDelete" :site="site" @deleted="onDeleted" />
  </div>
</template>

<style scoped>
.site {
  max-inline-size: 1100px;
}

.site__back {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-block-end: 8px;
  color: inherit;
  text-decoration: none;
  opacity: 0.75;
}

.site__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
}

.site__title {
  margin: 0;
}

.site__spacer {
  flex: 1;
}

.site__domains {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
  margin-block: 8px 16px;
}

.site__last {
  margin-block-end: 12px;
  font-size: 13px;
}

.block {
  margin-block-end: 16px;
}

.para {
  margin: 0 0 8px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  align-items: center;
  gap: 12px;
  margin-block-start: 12px;
}

.actions--start {
  justify-content: flex-start;
}

.facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 8px 20px;
  margin: 0;
}

.facts dt {
  opacity: 0.7;
}

.facts dd {
  margin: 0;
  overflow-wrap: anywhere;
}

.tag {
  margin-inline-start: 6px;
}

.explain {
  margin: 16px 0 0;
  font-size: 13px;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
