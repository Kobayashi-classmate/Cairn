<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
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
} from 'naive-ui'
import { OpenOutline, RefreshOutline } from '@vicons/ionicons5'
import {
  fetchSoftware,
  isAvailable,
  providerName,
  refreshPackageIndexes,
  type Offer,
  type Repository,
  type SoftwareCatalog,
  type SoftwareEntry,
} from '@/api/software'
import { useErrorToast } from '@/composables/useErrorToast'
import { errorMessage } from '@/i18n/errors'
import { formatIsoDate } from '@/i18n/format'
import { safeHttpsUrl } from '@/markdown/parse'
import { useSessionStore } from '@/stores/session'

/** Display order of categories; unknown categories follow in name order. */
const CATEGORY_ORDER = ['web', 'runtime', 'database', 'cache']

const { t, te } = useI18n()
const router = useRouter()
const session = useSessionStore()
const showError = useErrorToast()

const catalog = ref<SoftwareCatalog | null>(null)
const loadError = ref<unknown>(null)
const refreshing = ref(false)

const canManage = computed(() => session.can('software.manage'))

onMounted(async () => {
  try {
    catalog.value = await fetchSoftware()
  } catch (err) {
    loadError.value = err
  }
})

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
      <NButton v-if="canManage" :loading="refreshing" @click="refresh">
        <template #icon>
          <NIcon><RefreshOutline /></NIcon>
        </template>
        {{ t('software.refresh') }}
      </NButton>
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
                <span v-for="offer in available(entry)" :key="offer.line" class="line">
                  <span class="line__version" dir="ltr">{{ offer.line }}</span>
                  <NTag v-if="offer.recommended" type="success" size="small" :bordered="false">
                    {{ t('software.recommended') }}
                  </NTag>
                </span>
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
                          <dt>{{ t('software.advanced.packages') }}</dt>
                          <dd class="mono" dir="ltr">{{ offer.packages.join(', ') }}</dd>
                        </template>
                        <template v-if="offer.stream">
                          <dt>{{ t('software.advanced.stream') }}</dt>
                          <dd class="mono" dir="ltr">{{ offer.stream }}</dd>
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
  flex-wrap: wrap;
  gap: 8px 16px;
}

.line {
  display: inline-flex;
  align-items: center;
  gap: 6px;
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
