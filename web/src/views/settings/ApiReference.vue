<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NCollapse,
  NCollapseItem,
  NEmpty,
  NH3,
  NIcon,
  NInput,
  NSpin,
  NTag,
  NText,
} from 'naive-ui'
import { DownloadOutline, SearchOutline } from '@vicons/ionicons5'
import { fetchOpenApi, type JsonSchema, type OpenApiDocument, type Operation } from '@/api/openapi'
import { permissionLabel } from '@/auth/permissions'
import { errorMessage } from '@/i18n/errors'
import { downloadText } from '@/utils/download'

const { t } = useI18n()

const METHODS = ['get', 'post', 'put', 'patch', 'delete'] as const
type Method = (typeof METHODS)[number]

const METHOD_TAG: Record<Method, 'info' | 'success' | 'warning' | 'error'> = {
  get: 'info',
  post: 'success',
  put: 'warning',
  patch: 'warning',
  delete: 'error',
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
const loading = ref(true)
const error = ref<unknown>(null)
const query = ref('')

onMounted(async () => {
  try {
    doc.value = await fetchOpenApi()
  } catch (err) {
    error.value = err
  } finally {
    loading.value = false
  }
})

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
          op.method.includes(needle),
      ),
    }))
    .filter((group) => group.operations.length > 0)
})

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
    return isObject(resolved) ? { ...resolved, ...resolveSchema(siblings, trail) as object } : resolved
  }
  return Object.fromEntries(
    Object.entries(value).map(([key, item]) => [key, resolveSchema(item, trail)]),
  )
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
  <NCard :title="t('apidocs.title')" size="small">
    <template #header-extra>
      <NButton size="small" :disabled="!doc" @click="download">
        <template #icon>
          <NIcon><DownloadOutline /></NIcon>
        </template>
        {{ t('apidocs.download') }}
      </NButton>
    </template>

    <NText depth="3" tag="p" class="intro">{{ t('apidocs.description') }}</NText>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>

    <NSpin :show="loading">
      <template v-if="doc">
        <NInput
          v-model:value="query"
          clearable
          :placeholder="t('apidocs.filter')"
          :input-props="{ 'aria-label': t('apidocs.filter'), spellcheck: false }"
          class="filter"
        >
          <template #prefix>
            <NIcon><SearchOutline /></NIcon>
          </template>
        </NInput>

        <NEmpty v-if="filtered.length === 0" :description="t('apidocs.empty')" class="block" />

        <section v-for="group in filtered" :key="group.tag" class="group">
          <NH3 class="group__title">
            {{ group.tag || t('apidocs.untagged') }}
            <NText depth="3" class="group__count">
              {{ t('apidocs.operations', { count: group.operations.length }, group.operations.length) }}
            </NText>
          </NH3>
          <NCollapse :accordion="false">
            <NCollapseItem v-for="op in group.operations" :key="op.key" :name="op.key">
              <template #header>
                <span class="op">
                  <NTag size="small" :type="METHOD_TAG[op.method]" :bordered="false" class="op__method">
                    {{ op.method.toUpperCase() }}
                  </NTag>
                  <code class="op__path" dir="ltr">{{ op.path }}</code>
                  <NText depth="3" class="op__summary">{{ op.operation.summary }}</NText>
                </span>
              </template>
              <template #header-extra>
                <span class="op__badges">
                  <NTag
                    v-if="op.operation['x-cairn-step-up']"
                    size="small"
                    type="warning"
                    :bordered="false"
                  >
                    {{ t('apidocs.stepUp') }}
                  </NTag>
                  <NTag
                    v-if="op.operation['x-cairn-permission']"
                    size="small"
                    :bordered="false"
                    :title="op.operation['x-cairn-permission']"
                  >
                    {{ permissionLabel(op.operation['x-cairn-permission']) }}
                  </NTag>
                </span>
              </template>

              <div class="detail">
                <p v-if="op.operation.description" class="detail__description">
                  {{ op.operation.description }}
                </p>
                <div class="detail__section">
                  <NText strong>{{ t('apidocs.requestBody') }}</NText>
                  <pre v-if="requestSchema(op.operation)" class="schema" dir="ltr">{{
                    requestSchema(op.operation)
                  }}</pre>
                  <NText v-else depth="3" tag="div">{{ t('apidocs.noBody') }}</NText>
                </div>
                <div class="detail__section">
                  <NText strong>{{ t('apidocs.responses') }}</NText>
                  <div v-for="response in responses(op.operation)" :key="response.status" class="response">
                    <div class="response__head">
                      <code dir="ltr">{{ response.status }}</code>
                      <NText depth="3">{{ response.description }}</NText>
                    </div>
                    <pre v-if="response.schema" class="schema" dir="ltr">{{ response.schema }}</pre>
                  </div>
                </div>
              </div>
            </NCollapseItem>
          </NCollapse>
        </section>
      </template>
    </NSpin>
  </NCard>
</template>

<style scoped>
.intro {
  margin-block: 0 12px;
}

.block {
  margin-block-end: 12px;
}

.filter {
  max-inline-size: 360px;
  margin-block-end: 16px;
}

.group {
  margin-block-end: 20px;
}

.group__title {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-block: 0 8px;
}

.group__count {
  font-size: 13px;
  font-weight: normal;
}

.op {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 8px;
  min-inline-size: 0;
}

.op__method {
  min-inline-size: 56px;
  justify-content: center;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}

.op__path {
  font-size: 13px;
  overflow-wrap: anywhere;
}

.op__summary {
  font-size: 13px;
}

.op__badges {
  display: inline-flex;
  gap: 4px;
}

.detail {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding-inline-start: 4px;
}

.detail__description {
  margin: 0;
}

.detail__section {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.response {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.response__head {
  display: flex;
  gap: 8px;
}

.schema {
  max-block-size: 360px;
  margin: 0;
  padding: 8px 10px;
  overflow: auto;
  border-radius: 4px;
  background: rgb(128 128 128 / 10%);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
}
</style>
