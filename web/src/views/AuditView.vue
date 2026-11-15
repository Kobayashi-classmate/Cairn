<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NDataTable,
  NH2,
  NInput,
  NSelect,
  NTag,
  NText,
  type DataTableColumns,
} from 'naive-ui'
import { isAbortError } from '@/api/client'
import { fetchAudit, type AuditEntry } from '@/api/audit'
import { roleLabel } from '@/auth/permissions'
import { localeNativeName } from '@/i18n'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'
import { itemNames, jobKindLabel } from '@/jobs/labels'

const PAGE_SIZE = 50
const FILTER_DELAY_MS = 400

const { t, te } = useI18n()

const entries = ref<AuditEntry[]>([])
const nextBefore = ref<number | null>(null)
const loading = ref(false)
const loadError = ref<unknown>(null)

const userFilter = ref('')
const categoryFilter = ref<string | null>(null)

// Action code prefixes the server filters on.
const categories = ['auth', 'users', 'settings', 'notify', 'update', 'software', 'job', 'module', 'setup'] as const
const categoryOptions = computed(() =>
  categories.map((c) => ({ value: `${c}.`, label: t(`audit.category.${c}`) })),
)

let controller: AbortController | null = null

/** Loads the newest page (reset) or the next older one. */
async function load(reset: boolean) {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  try {
    const page = await fetchAudit(
      {
        limit: PAGE_SIZE,
        before: reset ? undefined : (nextBefore.value ?? undefined),
        user: userFilter.value.trim() || undefined,
        action: categoryFilter.value ?? undefined,
      },
      current.signal,
    )
    entries.value = reset ? page.entries : [...entries.value, ...page.entries]
    nextBefore.value = page.nextBefore
    loadError.value = null
  } catch (err) {
    if (isAbortError(err)) return
    loadError.value = err
  } finally {
    if (controller === current) {
      controller = null
      loading.value = false
    }
  }
}

onMounted(() => load(true))
onBeforeUnmount(() => controller?.abort())

let filterTimer: ReturnType<typeof setTimeout> | undefined
watch(userFilter, () => {
  clearTimeout(filterTimer)
  filterTimer = setTimeout(() => void load(true), FILTER_DELAY_MS)
})
watch(categoryFilter, () => void load(true))
onBeforeUnmount(() => clearTimeout(filterTimer))

/** Parameters prepared for display: role and language codes become names. */
function displayParams(entry: AuditEntry): Record<string, string | number | boolean> {
  const params = { ...entry.params }
  if (typeof params.role === 'string') params.role = roleLabel(params.role)
  if (typeof params.locale === 'string') params.locale = localeNativeName(params.locale)
  // Jobs are named by their translated type; failed cancels may only carry the id.
  if (entry.action.startsWith('job.')) {
    params.kind = typeof params.kind === 'string' ? jobKindLabel(params.kind) : String(params.id ?? '')
  }
  if (typeof params.module === 'string') {
    const key = `modules.name.${params.module}`
    params.module = te(key) ? t(key) : params.module
  }
  // Older install/remove entries only carry the catalog id, not the product name.
  if (entry.action.startsWith('software.') && typeof params.name !== 'string') {
    params.name = String(params.software ?? '')
  }
  // Install sets list their lines ("PHP 8.4, nginx 1.26").
  const names = itemNames((entry.params as Record<string, unknown>).items)
  if (names !== null) params.names = names
  if (entry.action === 'setup.wizard' && typeof params.state === 'string') {
    const key = `wizard.state.${params.state}`
    params.state = te(key) ? t(key) : params.state
  }
  if (typeof params.downloadPolicy === 'string') {
    const key = `software.policy.${params.downloadPolicy}.short`
    params.downloadPolicy = te(key) ? t(key) : params.downloadPolicy
  }
  return params
}

/** Translated description of an action; unknown actions show their code. */
function describe(entry: AuditEntry): string {
  const key = `audit.action.${entry.action}`
  if (!te(key)) return entry.action
  const params = displayParams(entry)
  return typeof params.count === 'number' ? t(key, params, params.count) : t(key, params)
}

/** Extra attributes shown as tags next to the description. */
function details(entry: AuditEntry): string[] {
  const type = entry.params.type
  if (entry.action.startsWith('notify.') && typeof type === 'string') {
    const key = `notify.type.${type}`
    return [te(key) ? t(key) : type]
  }
  if (entry.action === 'settings.cloud') {
    const list: string[] = []
    if (typeof entry.params.enabled === 'boolean') {
      list.push(t(entry.params.enabled ? 'audit.detail.enabled' : 'audit.detail.disabled'))
    }
    if (typeof entry.params.channel === 'string') {
      const key = `updates.channel.${entry.params.channel}`
      list.push(t('audit.detail.channel', { channel: te(key) ? t(key) : entry.params.channel }))
    }
    return list
  }
  // The download source chosen for a runtime, as its candidate id.
  if (entry.action === 'software.install' && typeof entry.params.source === 'string') {
    return [t('audit.detail.source', { source: entry.params.source })]
  }
  if (entry.action !== 'users.update') return []
  const list: string[] = []
  if (typeof entry.params.role === 'string') {
    list.push(t('audit.detail.role', { role: roleLabel(entry.params.role) }))
  }
  if (typeof entry.params.disabled === 'boolean') {
    list.push(t(entry.params.disabled ? 'audit.detail.disabled' : 'audit.detail.enabled'))
  }
  return list
}

function sourceLabel(source: string): string {
  const key = `audit.source.${source}`
  return te(key) ? t(key) : source
}

const columns = computed<DataTableColumns<AuditEntry>>(() => [
  {
    key: 'ts',
    title: t('audit.column.time'),
    width: 180,
    render: (entry) => formatDateTime(entry.ts),
  },
  {
    key: 'username',
    title: t('audit.column.user'),
    width: 140,
    render: (entry) =>
      entry.username || h(NText, { depth: 3 }, () => t('audit.noUser')),
  },
  {
    key: 'action',
    title: t('audit.column.action'),
    minWidth: 260,
    render: (entry) =>
      h('div', { class: 'action' }, [
        h('span', { title: entry.action }, describe(entry)),
        ...details(entry).map((text) =>
          h(NTag, { size: 'small', bordered: false }, () => text),
        ),
      ]),
  },
  {
    key: 'result',
    title: t('audit.column.result'),
    minWidth: 180,
    render: (entry) =>
      entry.result === 'ok'
        ? h(NTag, { type: 'success', size: 'small', bordered: false }, () => t('audit.result.ok'))
        : h(
            NTag,
            { type: 'error', size: 'small', bordered: false, title: entry.result },
            () => errorText(entry.result) ?? entry.result,
          ),
  },
  {
    key: 'source',
    title: t('audit.column.source'),
    width: 120,
    render: (entry) => sourceLabel(entry.source),
  },
  {
    key: 'remote',
    title: t('audit.column.remote'),
    width: 150,
    render: (entry) => h('span', { dir: 'ltr' }, entry.remote),
  },
])
</script>

<template>
  <div class="audit">
    <NH2 class="audit__title">{{ t('audit.title') }}</NH2>

    <div class="audit__filters">
      <NInput
        v-model:value="userFilter"
        clearable
        :placeholder="t('audit.filter.user')"
        :input-props="{ 'aria-label': t('audit.filter.user'), autocapitalize: 'none', spellcheck: false }"
        class="audit__filter"
      />
      <NSelect
        v-model:value="categoryFilter"
        clearable
        :options="categoryOptions"
        :placeholder="t('audit.filter.category')"
        class="audit__filter"
      />
    </div>

    <NAlert v-if="loadError" type="error" class="audit__alert">{{ errorMessage(loadError) }}</NAlert>

    <NCard size="small">
      <NDataTable
        :columns="columns"
        :data="entries"
        :loading="loading"
        :row-key="(entry: AuditEntry) => entry.id"
        :scroll-x="1100"
        size="small"
      />
      <div v-if="nextBefore !== null" class="audit__more">
        <NButton :loading="loading" @click="load(false)">{{ t('common.action.loadMore') }}</NButton>
      </div>
    </NCard>
  </div>
</template>

<style scoped>
.audit {
  max-inline-size: 1400px;
}

.audit__title {
  margin-block: 0 16px;
}

.audit__filters {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-block-end: 16px;
}

.audit__filter {
  inline-size: 240px;
  max-inline-size: 100%;
}

.audit__alert {
  margin-block-end: 16px;
}

.audit__more {
  display: flex;
  justify-content: center;
  margin-block-start: 12px;
}

:deep(.action) {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
</style>
