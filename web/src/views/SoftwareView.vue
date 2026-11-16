<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NCard,
  NCollapse,
  NCollapseItem,
  NH2,
  NH3,
  NIcon,
  NSpin,
  NTag,
  NText,
  useMessage,
} from 'naive-ui'
import { OpenOutline, RefreshOutline } from '@vicons/ionicons5'
import type { Job } from '@/api/jobs'
import {
  fetchSoftware,
  isAvailable,
  isManageable,
  providerName,
  refreshPackageIndexes,
  removeSoftware,
  runtimePaths,
  type Offer,
  type Repository,
  type SoftwareCatalog,
  type SoftwareEntry,
} from '@/api/software'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { useSoftwareJobs } from '@/composables/useSoftwareJobs'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatIsoDate } from '@/i18n/format'
import { jobKindLabel, jobStateLabel } from '@/jobs/labels'
import { safeHttpsUrl } from '@/markdown/parse'
import { useSessionStore } from '@/stores/session'
import InstallDialog from './software/InstallDialog.vue'

/** Display order of categories; unknown categories follow in name order. */
const CATEGORY_ORDER = ['web', 'runtime', 'database', 'cache', 'tool']

const { t, te } = useI18n()
const router = useRouter()
const message = useMessage()
const confirm = useConfirm()
const session = useSessionStore()
const showError = useErrorToast()

const catalog = ref<SoftwareCatalog | null>(null)
const loadError = ref<unknown>(null)
const refreshing = ref(false)
/** "<software>/<line>" while its install or remove request is being sent. */
const starting = ref<string | null>(null)
/** The last install or remove job that failed, shown until dismissed. */
const failure = ref<{ jobId: string; task: string; text: string } | null>(null)

const canManage = computed(() => session.can('software.manage'))

async function loadCatalog() {
  try {
    catalog.value = await fetchSoftware()
    loadError.value = null
  } catch (err) {
    loadError.value = err
  }
}

onMounted(loadCatalog)

/** A software job ended: reload what is installed and tell the user. */
function onJobFinished(job: Job) {
  void loadCatalog()
  const name = String(job.params.name ?? job.params.software ?? '')
  const line = String(job.params.line ?? '')
  if (job.state === 'succeeded') {
    const key = job.kind === 'software.remove' ? 'software.done.removed' : 'software.done.installed'
    message.success(t(key, { name, line }))
  } else if (job.state === 'failed') {
    failure.value = {
      jobId: job.id,
      task: jobKindLabel(job.kind, job.params),
      text: job.error ? (errorText(job.error.code, job.error.params) ?? job.error.code) : '',
    }
  } else {
    message.warning(
      t('software.done.ended', { task: jobKindLabel(job.kind, job.params), state: jobStateLabel(job.state) }),
    )
  }
}

const { track, runningFor } = useSoftwareJobs(onJobFinished)

const repos = computed(() => new Map((catalog.value?.repos ?? []).map((repo) => [repo.id, repo])))

const groups = computed(() => {
  const byCategory = new Map<string, SoftwareEntry[]>()
  for (const entry of catalog.value?.software ?? []) {
    const list = byCategory.get(entry.category) ?? []
    list.push(entry)
    byCategory.set(entry.category, list)
  }
  const rank = (category: string) => {
    const index = CATEGORY_ORDER.indexOf(category)
    return index === -1 ? CATEGORY_ORDER.length : index
  }
  return [...byCategory.entries()]
    .sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b))
    .map(([category, software]) => ({
      category,
      software: [...software].sort((a, b) => a.name.localeCompare(b.name)),
    }))
})

function label(prefix: string, value: string): string {
  const key = `${prefix}.${value}`
  return te(key) ? t(key) : value
}

function available(entry: SoftwareEntry): Offer[] {
  return entry.offers.filter(isAvailable)
}

function repoOf(offer: Offer): Repository | null {
  return offer.repo ? (repos.value.get(offer.repo) ?? null) : null
}

function methodText(offer: Offer): string {
  switch (offer.method) {
    case 'system':
      return t('software.method.system')
    case 'vendor':
      return t('software.method.vendor', { repo: repoOf(offer)?.name ?? offer.repo ?? '' })
    case 'standalone':
      // Not "official": a provider is not necessarily the upstream project.
      return offer.provider
        ? t('software.method.standaloneFrom', { provider: providerName(offer.provider) })
        : t('software.method.standalone')
    default:
      return offer.method ?? ''
  }
}

function reasonText(offer: Offer): string {
  return offer.reason && te(`software.reason.${offer.reason}`)
    ? t(`software.reason.${offer.reason}`)
    : t('software.reason.unknown')
}

/** Exact installed version of the line's first package, for example "8.4.27-1~deb13u1". */
function installedVersion(offer: Offer): string {
  return offer.installedPackages?.[0]?.version ?? ''
}

/** Whether buttons of this entry are locked by a running or starting job. */
function busy(entry: SoftwareEntry): boolean {
  return runningFor(entry.id) !== null || starting.value?.startsWith(`${entry.id}/`) === true
}

/** The running job acting on this line, if any. */
function runningOn(entry: SoftwareEntry, offer: Offer) {
  const job = runningFor(entry.id)
  return job && job.line === offer.line ? job : null
}

/** The line the install dialog is open for. */
const installTarget = ref<{ entry: SoftwareEntry; offer: Offer } | null>(null)
const showInstall = ref(false)

/** Opens the install dialog, which checks the download sources first. */
function install(entry: SoftwareEntry, offer: Offer) {
  failure.value = null
  installTarget.value = { entry, offer }
  showInstall.value = true
}

function onInstallStarted(job: Job) {
  track(job)
}

async function uninstall(entry: SoftwareEntry, offer: Offer) {
  const named = { name: entry.name, line: offer.line }
  const ok = await confirm({
    title: t('software.confirmRemove.title', named),
    // Runtimes are plain files; packages leave configuration and data behind.
    content: t(
      offer.method === 'standalone' ? 'software.confirmRemove.runtimeContent' : 'software.confirmRemove.content',
      named,
    ),
    confirmText: t('software.action.remove'),
    danger: true,
  })
  if (!ok) return
  starting.value = `${entry.id}/${offer.line}`
  failure.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    track(await removeSoftware(entry.id, offer.line))
  } catch (err) {
    showError(err)
  } finally {
    starting.value = null
  }
}

async function refresh() {
  refreshing.value = true
  try {
    const job = await refreshPackageIndexes()
    // Show the live log of the new job.
    await router.push({ name: 'job', params: { id: job.id } })
  } catch (err) {
    showError(err)
  } finally {
    refreshing.value = false
  }
}
</script>

<template>
  <div class="software">
    <header class="software__header">
      <NH2 class="software__title">{{ t('software.title') }}</NH2>
      <span class="software__actions">
        <NButton v-if="canManage" @click="router.push({ name: 'welcome', query: { step: 'software' } })">
          {{ t('wizard.open') }}
        </NButton>
        <NButton v-if="canManage" :loading="refreshing" @click="refresh">
          <template #icon>
            <NIcon><RefreshOutline /></NIcon>
          </template>
          {{ t('software.refresh') }}
        </NButton>
      </span>
    </header>

    <NAlert v-if="loadError" type="error" class="software__block">{{ errorMessage(loadError) }}</NAlert>

    <NSpin :show="!catalog && !loadError">
      <template v-if="catalog">
        <NText depth="3" tag="p" class="software__intro">
          {{ t('software.intro') }}
          <NTag v-if="catalog.release" size="small" :bordered="false" class="software__system">
            <span dir="ltr">{{ catalog.release }} · {{ catalog.arch }}</span>
          </NTag>
        </NText>
        <NAlert v-if="!catalog.supported" type="warning" class="software__block">
          {{ t('software.unsupported') }}
        </NAlert>
        <NAlert v-else-if="!catalog.statusAvailable" type="info" class="software__block">
          {{ t('software.statusUnavailable') }}
        </NAlert>
        <NAlert
          v-if="failure"
          type="error"
          closable
          :title="t('software.failed', { task: failure.task })"
          class="software__block"
          @close="failure = null"
        >
          <div v-if="failure.text">{{ failure.text }}</div>
          <RouterLink :to="{ name: 'job', params: { id: failure.jobId } }">
            {{ t('software.viewOutput') }}
          </RouterLink>
        </NAlert>

        <section v-for="group in groups" :key="group.category" class="software__group">
          <NH3 class="software__category">{{ label('software.category', group.category) }}</NH3>
          <div class="software__grid">
            <NCard v-for="entry in group.software" :key="entry.id" size="small">
              <template #header>
                <span class="product">{{ entry.name }}</span>
              </template>
              <template #header-extra>
                <a
                  v-if="safeHttpsUrl(entry.homepage)"
                  :href="safeHttpsUrl(entry.homepage) ?? undefined"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="homepage"
                >
                  {{ t('software.homepage') }}
                  <NIcon :size="14"><OpenOutline /></NIcon>
                </a>
              </template>

              <div v-if="available(entry).length > 0" class="lines">
                <div v-for="offer in available(entry)" :key="offer.line" class="line">
                  <span class="line__version" dir="ltr">{{ offer.line }}</span>
                  <NTag v-if="offer.recommended" type="success" size="small" :bordered="false">
                    {{ t('software.recommended') }}
                  </NTag>
                  <NTag v-if="offer.installed" type="info" size="small" :bordered="false">
                    {{ t('software.installed') }}
                  </NTag>
                  <NText v-if="offer.installed && installedVersion(offer)" depth="3" class="mono version" dir="ltr">
                    {{ installedVersion(offer) }}
                  </NText>
                  <span v-if="canManage && isManageable(offer)" class="line__actions">
                    <span v-if="runningOn(entry, offer)" class="line__progress">
                      <NSpin :size="14" />
                      {{
                        runningOn(entry, offer)?.kind === 'software.remove'
                          ? t('software.progress.remove')
                          : t('software.progress.install')
                      }}
                      <RouterLink :to="{ name: 'job', params: { id: runningOn(entry, offer)?.id } }">
                        {{ t('software.viewOutput') }}
                      </RouterLink>
                    </span>
                    <NButton
                      v-else-if="offer.installed"
                      size="tiny"
                      type="error"
                      ghost
                      :disabled="busy(entry)"
                      :loading="starting === `${entry.id}/${offer.line}`"
                      @click="uninstall(entry, offer)"
                    >
                      {{ t('software.action.remove') }}
                    </NButton>
                    <NButton
                      v-else
                      size="tiny"
                      type="primary"
                      :disabled="busy(entry)"
                      :loading="starting === `${entry.id}/${offer.line}`"
                      @click="install(entry, offer)"
                    >
                      {{ t('software.action.install') }}
                    </NButton>
                  </span>
                </div>
              </div>
              <NText v-else depth="3">{{ t('software.noLines') }}</NText>

              <NCollapse class="advanced">
                <NCollapseItem :title="t('software.advanced.title')" :name="entry.id">
                  <div class="offers">
                    <div v-for="offer in entry.offers" :key="offer.line" class="offer">
                      <div class="offer__head">
                        <span class="line__version" dir="ltr">{{ offer.line }}</span>
                        <NText v-if="!isAvailable(offer)" depth="3">{{ t('software.advanced.notAvailable') }}</NText>
                      </div>
                      <dl v-if="isAvailable(offer)" class="offer__facts">
                        <dt>{{ t('software.advanced.method') }}</dt>
                        <dd>{{ methodText(offer) }}</dd>
                        <template v-if="offer.packages?.length">
                          <!-- Prebuilt runtimes list the system libraries they need. -->
                          <dt>
                            {{
                              offer.method === 'standalone'
                                ? t('software.advanced.systemLibraries')
                                : t('software.advanced.packages')
                            }}
                          </dt>
                          <dd class="mono" dir="ltr">{{ offer.packages.join(', ') }}</dd>
                        </template>
                        <template v-if="offer.stream">
                          <dt>{{ t('software.advanced.stream') }}</dt>
                          <dd class="mono" dir="ltr">{{ offer.stream }}</dd>
                        </template>
                        <template v-if="offer.installedPackages?.length">
                          <dt>{{ t('software.advanced.installedPackages') }}</dt>
                          <dd class="mono" dir="ltr">
                            <div v-for="pkg in offer.installedPackages" :key="pkg.name">
                              {{ pkg.name }} {{ pkg.version }}
                            </div>
                          </dd>
                        </template>
                        <template v-if="runtimePaths(entry.id, offer)">
                          <dt>{{ t('software.advanced.location') }}</dt>
                          <dd class="mono" dir="ltr">{{ runtimePaths(entry.id, offer)?.line }}</dd>
                          <dt>{{ t('software.advanced.buildDirectory') }}</dt>
                          <dd class="mono" dir="ltr">{{ runtimePaths(entry.id, offer)?.build }}</dd>
                        </template>
                        <template v-if="offer.services?.length">
                          <dt>{{ t('software.advanced.services') }}</dt>
                          <dd class="mono" dir="ltr">{{ offer.services.join(', ') }}</dd>
                        </template>
                        <template v-if="offer.eol">
                          <dt>{{ t('software.advanced.eol') }}</dt>
                          <dd>
                            {{
                              offer.endOfLife
                                ? t('software.advanced.eolEnded', { date: formatIsoDate(offer.eol) })
                                : formatIsoDate(offer.eol)
                            }}
                          </dd>
                        </template>
                      </dl>
                      <NText v-else depth="3" tag="p" class="offer__reason">{{ reasonText(offer) }}</NText>
                      <NText
                        v-if="isAvailable(offer) && repoOf(offer) && !repoOf(offer)?.official"
                        depth="3"
                        tag="p"
                        class="offer__note"
                      >
                        {{ t('software.advanced.unofficialRepo', { repo: repoOf(offer)?.name ?? '', software: entry.name }) }}
                      </NText>
                    </div>
                  </div>
                </NCollapseItem>
              </NCollapse>
            </NCard>
          </div>
        </section>
      </template>
    </NSpin>
    <InstallDialog
      v-model:show="showInstall"
      :entry="installTarget?.entry ?? null"
      :offer="installTarget?.offer ?? null"
      :repos="repos"
      @started="onInstallStarted"
    />
  </div>
</template>

<style scoped>
.software {
  max-inline-size: 1300px;
}

.software__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 16px;
}

.software__title {
  margin: 0;
}

.software__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.software__intro {
  margin-block: 4px 16px;
}

.software__system {
  margin-inline-start: 8px;
}

.software__block {
  margin-block-end: 16px;
}

.software__group {
  margin-block-end: 24px;
}

.software__category {
  margin-block: 0 12px;
}

.software__grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 16px;
  align-items: start;
}

.product {
  font-weight: 600;
}

.homepage {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: inherit;
  font-size: 13px;
}

.lines {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.line {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  min-block-size: 28px;
}

.line__actions {
  display: inline-flex;
  align-items: center;
  margin-inline-start: auto;
}

.line__progress {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
}

.version {
  font-size: 12px;
}

.line__version {
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.advanced {
  margin-block-start: 12px;
}

.offers {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.offer__head {
  display: flex;
  align-items: baseline;
  gap: 8px;
}

.offer__facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 2px 12px;
  margin: 4px 0 0;
  font-size: 13px;
}

.offer__facts dt {
  opacity: 0.7;
}

.offer__facts dd {
  margin: 0;
  overflow-wrap: anywhere;
}

.offer__reason,
.offer__note {
  margin: 4px 0 0;
  font-size: 13px;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
