<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Download, Trash2 } from '@lucide/vue'
import { isAbortError } from '@/api/client'
import { changeExtensions, fetchExtensions, type ExtensionsResponse, type Offer, type PhpExtension, type SoftwareEntry } from '@/api/software'
import Card from '@/components/common/Card.vue'
import DataTable from '@/components/common/DataTable.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { confirmAction } from '@/composables/useConfirmDialog'
import type { Tone } from '@/lib/tone'
import { useSoftwareStore } from '@/stores/software'

// PHP extensions of the line: what is installed, loaded or built in, and
// what can be added here. Several can be installed or removed in one job,
// which reloads the line's PHP-FPM; the modules PHP loads are listed below.
const props = defineProps<{ entry: SoftwareEntry; offer: Offer; canManage: boolean }>()

const { t } = useI18n()
const store = useSoftwareStore()

const data = ref<ExtensionsResponse | null>(null)
const error = ref<unknown>(null)
const loading = ref(false)
const selection = ref<string[]>([])
const search = ref('')
let controller: AbortController | null = null

async function load() {
  controller?.abort()
  const request = new AbortController()
  controller = request
  loading.value = true
  error.value = null
  try {
    data.value = await fetchExtensions(props.entry.id, props.offer.line, request.signal)
  } catch (err) {
    if (isAbortError(err)) return
    error.value = err
  } finally {
    if (controller === request) {
      controller = null
      loading.value = false
    }
  }
}

onMounted(load)
onBeforeUnmount(() => controller?.abort())

const busy = computed(() => store.runningFor(props.entry.id) !== null)
const extensions = computed(() => data.value?.extensions ?? [])

/** Extensions that can be installed or removed here. */
function changeable(ext: PhpExtension): boolean {
  return props.canManage && ext.available && !ext.builtin
}

function stateOf(ext: PhpExtension): { tone: Tone; text: string } {
  if (ext.builtin) return { tone: 'neutral', text: t('software.extensions.state.builtin') }
  if (ext.installed && ext.loaded) return { tone: 'success', text: t('software.extensions.state.loaded') }
  if (ext.installed) return { tone: 'warning', text: t('software.extensions.state.notLoaded') }
  if (!ext.available) return { tone: 'neutral', text: t('software.extensions.state.unavailable') }
  return { tone: 'neutral', text: t('software.extensions.state.available') }
}

const columns = computed<ColumnDef<PhpExtension>[]>(() => [
  { id: 'name', header: t('software.extensions.column.name'), accessorFn: (ext) => ext.name.toLowerCase(), meta: { class: 'min-w-[180px]' } },
  { id: 'modules', header: t('software.extensions.column.modules'), enableSorting: false, meta: { class: 'min-w-[160px]' } },
  {
    id: 'state',
    header: t('software.extensions.column.state'),
    accessorFn: (ext) => (ext.builtin ? 3 : ext.installed ? 0 : ext.available ? 1 : 2),
    meta: { class: 'w-[150px]' },
  },
])

function searchText(ext: PhpExtension): string {
  return [ext.id, ext.name, ...ext.modules].join(' ')
}

function picked(rows: PhpExtension[]) {
  return {
    install: rows.filter((ext) => changeable(ext) && !ext.installed).map((ext) => ext.id),
    remove: rows.filter((ext) => changeable(ext) && ext.installed).map((ext) => ext.id),
  }
}

function names(ids: string[]): string {
  return ids.map((id) => extensions.value.find((ext) => ext.id === id)?.name ?? id).join(t('software.steps.separator'))
}

async function apply(kind: 'install' | 'remove', ids: string[], clear: () => void) {
  if (ids.length === 0) return
  const change = kind === 'install' ? { install: ids } : { remove: ids }
  const start = () => changeExtensions(props.entry.id, props.offer.line, change)
  const done = () => void load()
  const ok = await confirmAction({
    title:
      kind === 'install'
        ? t('software.extensions.installTitle', { count: ids.length, line: props.offer.line }, ids.length)
        : t('software.extensions.removeTitle', { count: ids.length, line: props.offer.line }, ids.length),
    description:
      kind === 'install'
        ? t('software.extensions.installText', { names: names(ids), line: props.offer.line })
        : t('software.extensions.removeText', { names: names(ids), line: props.offer.line }),
    confirmLabel: kind === 'install' ? t('software.extensions.install') : t('software.extensions.remove'),
    destructive: kind === 'remove',
    run: async () => {
      await store.runJob(start, { rethrow: true, onFinished: done })
    },
  })
  if (ok) clear()
}
</script>

<template>
  <p class="m-0 text-muted-foreground">{{ t('software.extensions.intro', { line: offer.line }) }}</p>

  <DataTable
    v-model:search="search"
    v-model:selection="selection"
    :data="extensions"
    :columns="columns"
    :row-key="(ext) => ext.id"
    :label="t('software.drawer.extensions')"
    :loading="loading"
    :error="error"
    :search-placeholder="t('software.extensions.search')"
    :search-text="searchText"
    :selectable="canManage"
    :can-select="changeable"
    :empty-title="t('software.extensions.empty')"
    :min-width="560"
    :initial-sort="[{ id: 'state', desc: false }]"
    @retry="load"
  >
    <template #cell-name="{ row }">
      <div class="flex min-w-0 flex-col">
        <span class="font-medium">{{ row.name }}</span>
        <span v-if="row.id !== row.name" class="font-mono text-xs text-muted-foreground" dir="ltr">{{ row.id }}</span>
      </div>
    </template>
    <template #cell-modules="{ row }">
      <span class="font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]" dir="ltr">{{ row.modules.join(', ') }}</span>
    </template>
    <template #cell-state="{ row }">
      <StatusPill :tone="stateOf(row).tone">{{ stateOf(row).text }}</StatusPill>
    </template>
    <template #bulk="{ rows, clear }">
      <Button
        size="sm"
        variant="outline"
        :disabled="busy || picked(rows).install.length === 0"
        @click="apply('install', picked(rows).install, clear)"
      >
        <Download aria-hidden="true" />
        {{ t('software.extensions.installCount', { count: picked(rows).install.length }) }}
      </Button>
      <Button
        size="sm"
        variant="outline"
        class="text-destructive hover:text-destructive"
        :disabled="busy || picked(rows).remove.length === 0"
        @click="apply('remove', picked(rows).remove, clear)"
      >
        <Trash2 aria-hidden="true" />
        {{ t('software.extensions.removeCount', { count: picked(rows).remove.length }) }}
      </Button>
    </template>
  </DataTable>

  <Card :title="t('software.extensions.modules', { count: data?.modules.length ?? 0 })" :level="3">
    <div v-if="!data && loading" class="flex flex-wrap gap-1.5" aria-hidden="true">
      <Skeleton v-for="index in 12" :key="index" class="h-5 w-16 rounded-sm" />
    </div>
    <p v-else-if="!data || data.modules.length === 0" class="m-0 text-detail text-muted-foreground">{{ t('software.extensions.noModules') }}</p>
    <ul v-else class="m-0 flex list-none flex-wrap gap-1.5 p-0">
      <li v-for="module in data.modules" :key="module" class="rounded-sm bg-muted px-1.5 py-0.5 font-mono text-xs" dir="ltr">{{ module }}</li>
    </ul>
  </Card>
</template>
