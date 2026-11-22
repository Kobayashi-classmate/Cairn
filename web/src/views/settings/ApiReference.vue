<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight, Download, Search, SearchX, X } from '@lucide/vue'
import { fetchOpenApi, type JsonSchema, type OpenApiDocument, type Operation } from '@/api/openapi'
import Card from '@/components/common/Card.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { permissionLabel } from '@/auth/permissions'
import type { Tone } from '@/lib/tone'
import { downloadText } from '@/utils/download'

// The panel's HTTP API as its OpenAPI description lists it, grouped by tag
// and filterable; each operation opens to show its permission, whether it
// asks to re-authenticate, the request body and the responses.
const { t } = useI18n()

const METHODS = ['get', 'post', 'put', 'patch', 'delete'] as const
type Method = (typeof METHODS)[number]

const METHOD_TONE: Record<Method, Tone> = {
  get: 'progress',
  post: 'success',
  put: 'warning',
  patch: 'warning',
  delete: 'destructive',
}

interface OperationView {
  key: string
  method: Method
  path: string
  operation: Operation
}

interface TagGroup {
  tag: string
  operations: OperationView[]
}

const doc = ref<OpenApiDocument | null>(null)
const error = ref<unknown>(null)
const query = ref('')

async function load() {
  error.value = null
  try {
    doc.value = await fetchOpenApi()
  } catch (err) {
    error.value = err
  }
}

onMounted(load)

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/** Every operation of the document, grouped by its first tag in document order. */
const groups = computed<TagGroup[]>(() => {
  const paths = doc.value?.paths ?? {}
  const byTag = new Map<string, OperationView[]>()
  for (const [path, item] of Object.entries(paths)) {
    if (!isObject(item)) continue
    for (const method of METHODS) {
      const operation = item[method]
      if (!isObject(operation)) continue
      const op = operation as Operation
      const tag = op.tags?.[0] ?? ''
      const list = byTag.get(tag) ?? []
      list.push({ key: `${method} ${path}`, method, path, operation: op })
      byTag.set(tag, list)
    }
  }
  const declared = (doc.value?.tags ?? []).map((tag) => tag.name)
  const order = (tag: string) => {
    const index = declared.indexOf(tag)
    return index === -1 ? declared.length : index
  }
  return [...byTag.entries()]
    .map(([tag, operations]) => ({ tag, operations }))
    .sort((a, b) => order(a.tag) - order(b.tag) || a.tag.localeCompare(b.tag))
})

const filtered = computed<TagGroup[]>(() => {
  const needle = query.value.trim().toLowerCase()
  if (!needle) return groups.value
  return groups.value
    .map((group) => ({
      tag: group.tag,
      operations: group.operations.filter(
        (op) =>
          op.path.toLowerCase().includes(needle) ||
          (op.operation.summary ?? '').toLowerCase().includes(needle) ||
          op.method.includes(needle) ||
          (op.operation['x-cairn-permission'] ?? '').includes(needle),
      ),
    }))
    .filter((group) => group.operations.length > 0)
})

const total = computed(() => groups.value.reduce((sum, group) => sum + group.operations.length, 0))

const SCHEMA_REF = '#/components/schemas/'
const MAX_DEPTH = 24

/**
 * Inlines `$ref`s into components.schemas so a schema reads on its own.
 * A schema already being expanded (recursion) is left as its `$ref`.
 */
function resolveSchema(value: unknown, trail: string[] = []): unknown {
  if (Array.isArray(value)) return value.map((item) => resolveSchema(item, trail))
  if (!isObject(value)) return value
  const ref = value.$ref
  if (typeof ref === 'string' && ref.startsWith(SCHEMA_REF)) {
    const name = decodeURIComponent(ref.slice(SCHEMA_REF.length))
      .replace(/~1/g, '/')
      .replace(/~0/g, '~')
    const target = doc.value?.components?.schemas?.[name]
    if (!target || trail.includes(name) || trail.length >= MAX_DEPTH) return value
    const { $ref: _ref, ...siblings } = value
    const resolved = resolveSchema(target, [...trail, name])
    return isObject(resolved) ? { ...resolved, ...(resolveSchema(siblings, trail) as object) } : resolved
  }
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, resolveSchema(item, trail)]))
}

function schemaText(schema: JsonSchema | undefined): string | null {
  return schema ? JSON.stringify(resolveSchema(schema), null, 2) : null
}

function requestSchema(op: Operation): string | null {
  return schemaText(op.requestBody?.content?.['application/json']?.schema)
}

function responses(op: Operation) {
  return Object.entries(op.responses ?? {}).map(([status, response]) => ({
    status,
    description: response.description ?? '',
    schema: schemaText(response.content?.['application/json']?.schema),
  }))
}

function download() {
  if (!doc.value) return
  downloadText('openapi.json', `${JSON.stringify(doc.value, null, 2)}\n`, 'application/json')
}
</script>

<template>
  <p class="m-0 -mt-2 text-muted-foreground">{{ t('apidocs.description') }}</p>

  <ErrorState v-if="error" :error="error" class="rounded-lg border bg-card" @retry="load" />
  <div v-else-if="!doc" class="flex flex-col gap-3" role="status">
    <span class="sr-only">{{ t('common.state.loading') }}</span>
    <Skeleton class="h-9 w-80 max-w-full" />
    <Skeleton class="h-40 w-full rounded-lg" />
    <Skeleton class="h-40 w-full rounded-lg" />
  </div>
  <template v-else>
    <div class="flex flex-wrap items-center gap-3">
      <label
        class="flex h-9 min-w-0 flex-[1_1_240px] items-center gap-2 rounded-md border border-input bg-card px-2.5 text-muted-foreground focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/50 sm:max-w-96 max-md:h-11"
      >
        <Search class="size-4 shrink-0" aria-hidden="true" />
        <input
          v-model="query"
          type="search"
          :aria-label="t('apidocs.filter')"
          :placeholder="t('apidocs.filter')"
          spellcheck="false"
          class="h-full min-w-0 flex-1 bg-transparent text-base text-foreground outline-none placeholder:text-muted-foreground md:text-sm [&::-webkit-search-cancel-button]:hidden"
        />
        <button
          v-if="query"
          type="button"
          class="inline-flex size-6 shrink-0 items-center justify-center rounded-sm outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 max-md:-me-2.5 max-md:size-11"
          :aria-label="t('common.table.clearSearch')"
          @click="query = ''"
        >
          <X class="size-3.5" aria-hidden="true" />
        </button>
      </label>
      <span class="text-detail text-muted-foreground">{{ t('apidocs.operations', { count: total }, total) }}</span>
      <Button variant="outline" class="ms-auto max-md:h-11" @click="download">
        <Download aria-hidden="true" />
        {{ t('apidocs.download') }}
      </Button>
    </div>

    <EmptyState v-if="filtered.length === 0" :icon="SearchX" :title="t('apidocs.empty')" class="rounded-lg border bg-card" />

    <Card v-for="group in filtered" :key="group.tag" :level="3" flush>
      <template #title>
        {{ group.tag || t('apidocs.untagged') }}
        <span class="ms-1.5 text-xs font-normal text-muted-foreground">
          {{ t('apidocs.operations', { count: group.operations.length }, group.operations.length) }}
        </span>
      </template>
      <ul class="m-0 list-none divide-y p-0">
        <li v-for="op in group.operations" :key="op.key">
          <details class="group">
            <summary
              class="flex cursor-pointer list-none flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5 outline-none hover:bg-accent/50 focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset md:px-[18px] [&::-webkit-details-marker]:hidden"
            >
              <ChevronRight class="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-90" aria-hidden="true" />
              <StatusPill :tone="METHOD_TONE[op.method]" class="min-w-14 justify-center font-mono">{{ op.method.toUpperCase() }}</StatusPill>
              <code class="min-w-0 font-mono text-[12.5px] [overflow-wrap:anywhere]" dir="ltr">{{ op.path }}</code>
              <span class="min-w-0 flex-1 text-detail text-muted-foreground">{{ op.operation.summary }}</span>
              <span class="flex flex-wrap gap-1">
                <StatusPill v-if="op.operation['x-cairn-step-up']" tone="warning">{{ t('apidocs.stepUp') }}</StatusPill>
                <StatusPill v-if="op.operation['x-cairn-permission']" :title="op.operation['x-cairn-permission']">
                  {{ permissionLabel(op.operation['x-cairn-permission']) }}
                </StatusPill>
              </span>
            </summary>
            <div class="flex flex-col gap-3 border-t bg-muted/40 px-4 py-3 md:px-[18px] md:ps-11">
              <p v-if="op.operation.description" class="m-0">{{ op.operation.description }}</p>
              <div class="flex flex-col gap-1.5">
                <h4 class="m-0 text-detail font-semibold">{{ t('apidocs.requestBody') }}</h4>
                <pre
                  v-if="requestSchema(op.operation)"
                  tabindex="0"
                  class="m-0 max-h-80 overflow-auto rounded-md border bg-card px-3 py-2 font-mono text-xs leading-relaxed"
                  dir="ltr"
                >{{ requestSchema(op.operation) }}</pre>
                <p v-else class="m-0 text-detail text-muted-foreground">{{ t('apidocs.noBody') }}</p>
              </div>
              <div class="flex flex-col gap-1.5">
                <h4 class="m-0 text-detail font-semibold">{{ t('apidocs.responses') }}</h4>
                <div v-for="response in responses(op.operation)" :key="response.status" class="flex flex-col gap-1">
                  <div class="flex flex-wrap items-baseline gap-2 text-detail">
                    <code class="font-mono font-medium" dir="ltr">{{ response.status }}</code>
                    <span class="text-muted-foreground">{{ response.description }}</span>
                  </div>
                  <pre
                    v-if="response.schema"
                    tabindex="0"
                    class="m-0 max-h-80 overflow-auto rounded-md border bg-card px-3 py-2 font-mono text-xs leading-relaxed"
                    dir="ltr"
                  >{{ response.schema }}</pre>
                </div>
              </div>
            </div>
          </details>
        </li>
      </ul>
    </Card>
  </template>
</template>
