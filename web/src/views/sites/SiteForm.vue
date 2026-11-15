<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import {
  NAlert,
  NButton,
  NCollapse,
  NCollapseItem,
  NDynamicTags,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NRadio,
  NRadioGroup,
  NSelect,
  NSwitch,
  NText,
  type FormInst,
  type FormItemRule,
  type FormRules,
} from 'naive-ui'
import type { CertView } from '@/api/certs'
import {
  SITE_TYPES,
  type AppRuntime,
  type AppSpec,
  type EnvVar,
  type RewriteRule,
  type SiteRequest,
  type SiteType,
  type SiteView,
} from '@/api/sites'
import { certCovers } from '@/certs/labels'
import { checkRule, listRule, requiredListRule, requiredRule } from '@/forms/rules'
import { validationMessage } from '@/i18n/errors'
import { rewriteLabel } from '@/sites/labels'
import type { InstalledRuntime } from '@/sites/useRuntimes'
import {
  MAX_DOMAINS,
  MAX_INDEX,
  MAX_ENV,
  MAX_WRITABLE,
  deriveCode,
  isAppCommand,
  isEnvName,
  isEnvValue,
  isHealthPath,
  isIndexName,
  isProxyUrl,
  isRelativePath,
  isSiteCode,
  isSiteHost,
  normalizeHost,
} from '@/sites/validate'

/**
 * The fields of a site, for creating one or changing its settings. On
 * create the type and an optional code are chosen too; both are fixed later.
 */
const props = defineProps<{
  mode: 'create' | 'edit'
  /** The site being edited. */
  initial?: SiteView | null
  /** Installed PHP lines with PHP-FPM. */
  phpLines: string[]
  rewrites: RewriteRule[]
  /** Valid certificates for HTTPS; null hides the HTTPS section (no access to certificates). */
  certs?: CertView[] | null
  /** Installed runtime lines for app sites. */
  runtimes?: InstalledRuntime[]
  /** Apps need systemd; without it the app type cannot be chosen. */
  systemd?: boolean
  disabled?: boolean
}>()

const { t } = useI18n()

const formRef = ref<FormInst | null>(null)
const form = reactive({
  type: 'static' as SiteType,
  code: '',
  domains: [] as string[],
  root: '',
  index: [] as string[],
  rewrite: null as string | null,
  php: null as string | null,
  writable: [] as string[],
  proxyUrl: '',
  webSocket: false,
  upstreamHost: false,
  tlsCert: null as string | null,
  tlsRedirect: true,
  tlsHsts: false,
  app: emptyApp(),
})

/** App fields as edited; numbers are null when left empty. */
function emptyApp() {
  return {
    /** "<software>@<line>", or "" for system tools only. */
    runtime: '',
    command: '',
    workDir: '',
    port: null as number | null,
    env: [] as EnvVar[],
    memoryMb: null as number | null,
    cpuPercent: null as number | null,
    healthPath: '',
    webSocket: false,
  }
}

function appModel(app: AppSpec | undefined) {
  if (!app) return emptyApp()
  return {
    runtime: app.runtime && app.line ? `${app.runtime}@${app.line}` : '',
    command: app.command,
    workDir: app.workDir ?? '',
    port: app.port || null,
    env: (app.env ?? []).map((e) => ({ ...e })),
    memoryMb: app.memoryMb || null,
    cpuPercent: app.cpuPercent || null,
    healthPath: app.healthPath ?? '',
    webSocket: app.webSocket === true,
  }
}

function reset() {
  const site = props.initial
  Object.assign(form, {
    type: site?.type ?? 'static',
    code: '',
    domains: [...(site?.domains ?? [])],
    root: site?.root ?? '',
    index: [...(site?.index ?? [])],
    rewrite: site?.rewrite ?? null,
    php: site?.php ?? props.phpLines[0] ?? null,
    writable: [...(site?.writable ?? [])],
    proxyUrl: site?.proxy?.url ?? '',
    webSocket: site?.proxy?.webSocket ?? false,
    upstreamHost: site?.proxy?.upstreamHost ?? false,
    tlsCert: site?.tls?.cert ?? null,
    tlsRedirect: site?.tls ? site.tls.redirect === true : true,
    tlsHsts: site?.tls?.hsts === true,
    app: appModel(site?.app),
  })
  formRef.value?.restoreValidation()
}

watch(() => props.initial, reset, { immediate: true })

/** Domains as typed are lower-cased, converted to punycode and de-duplicated. */
function onDomains(values: string[]) {
  form.domains = [...new Set(values.map(normalizeHost).filter(Boolean))]
}

function onType(type: SiteType) {
  form.type = type
  // A rewrite rule fits some types only.
  if (form.rewrite && !rewriteOptions.value.some((o) => o.value === form.rewrite)) form.rewrite = null
  formRef.value?.restoreValidation()
}

/** Certificates whose names cover every domain of the site (a wildcard covers one label). */
const certOptions = computed(() => [
  { value: '', label: t('sites.form.noTls') },
  ...(props.certs ?? [])
    .filter((cert) => form.domains.length > 0 && certCovers(cert, form.domains))
    .map((cert) => ({ value: cert.id, label: `${cert.name} (${cert.domains.join(', ')})` })),
])

function onCert(value: string) {
  form.tlsCert = value || null
}

const isProxy = computed(() => form.type === 'proxy')
const isPhp = computed(() => form.type === 'php')
const isApp = computed(() => form.type === 'app')
/** Static and PHP sites serve files: web root, index files and rewrite rules. */
const servesFiles = computed(() => form.type === 'static' || form.type === 'php')

/** Example commands per runtime, shown in the empty command field (commands, not text to translate). */
const COMMAND_EXAMPLES: Record<string, string> = {
  nodejs: 'node server.js',
  python: 'gunicorn -b 127.0.0.1:$PORT app:app',
  go: './server',
  java: 'java -jar app.jar --server.port=$PORT',
  dotnet: 'dotnet MyApp.dll --urls http://127.0.0.1:$PORT',
  '': './server',
}

const commandExample = computed(() => COMMAND_EXAMPLES[form.app.runtime.split('@')[0] ?? ''] ?? './server')

const runtimeOptions = computed(() => {
  const options = [
    { value: '', label: t('sites.app.systemTools') },
    ...(props.runtimes ?? []).map((r) => ({ value: `${r.software}@${r.line}`, label: `${r.name} ${r.line}` })),
  ]
  // A runtime the site uses but that is no longer installed stays visible.
  if (form.app.runtime && !options.some((o) => o.value === form.app.runtime)) {
    options.push({ value: form.app.runtime, label: t('sites.app.notInstalled', { runtime: form.app.runtime.replace('@', ' ') }) })
  }
  return options
})

function addEnv() {
  form.app.env.push({ name: '', value: '' })
}

function removeEnv(index: number) {
  form.app.env.splice(index, 1)
}

/**
 * Example upstream shown in the empty proxy field (an address, not text to
 * translate). Kept out of the template: a literal "http://" attribute there
 * breaks vue-tsc's type checking of the rest of the template.
 */
const PROXY_EXAMPLE = 'http://127.0.0.1:3000'

function onRewrite(value: string) {
  form.rewrite = value || null
}

const derivedCode = computed(() => (form.domains[0] ? deriveCode(form.domains[0]) : ''))

const rewriteOptions = computed(() => [
  { value: '', label: t('sites.form.noRewrite') },
  ...props.rewrites
    .filter((rule) => rule.types.includes(form.type))
    .map((rule) => ({ value: rule.id, label: rewriteLabel(rule) })),
])

const phpOptions = computed(() => props.phpLines.map((line) => ({ value: line, label: `PHP ${line}` })))

function maxRule(field: string, max: number): FormItemRule {
  return {
    trigger: 'change',
    validator: (_rule, value: unknown) => !Array.isArray(value) || value.length <= max,
    renderMessage: () => validationMessage(field, 'count'),
  }
}

const rules = computed<FormRules>(() => {
  const list: FormRules = {
    domains: [requiredListRule('domains'), listRule('domains', 'host', isSiteHost), maxRule('domains', MAX_DOMAINS)],
    code: [checkRule('siteCode', 'site_code', isSiteCode)],
    tlsCert: [
      {
        trigger: 'change',
        // The chosen certificate must still cover the domains after they changed.
        validator: () => !form.tlsCert || certOptions.value.some((o) => o.value === form.tlsCert),
        renderMessage: () => t('sites.form.certNotCovering'),
      },
    ],
  }
  if (form.type === 'proxy') {
    list.proxyUrl = [requiredRule('proxy'), checkRule('proxy', 'url', isProxyUrl)]
    return list
  }
  if (form.type === 'app') {
    const optionalMin = (field: string, min: number): FormItemRule => ({
      trigger: ['blur', 'change'],
      validator: (_rule, value: unknown) => value === null || value === 0 || (typeof value === 'number' && value >= min),
      renderMessage: () => validationMessage(field, 'range'),
    })
    list.app = {
      command: [requiredRule('app.command'), checkRule('app.command', 'command', isAppCommand)],
      workDir: [checkRule('app.workDir', 'path', isRelativePath)],
      port: [
        {
          trigger: ['blur', 'change'],
          validator: (_rule, value: unknown) =>
            value === null || (typeof value === 'number' && value >= 1024 && value <= 65535),
          renderMessage: () => validationMessage('app.port', 'port'),
        },
      ],
      env: [
        {
          trigger: ['blur', 'change'],
          validator: () => {
            const names = form.app.env.map((e) => e.name.trim())
            return (
              form.app.env.length <= MAX_ENV &&
              names.every((name) => isEnvName(name)) &&
              new Set(names).size === names.length
            )
          },
          renderMessage: () => validationMessage('app.env', 'name'),
        },
        {
          trigger: ['blur', 'change'],
          validator: () => form.app.env.every((e) => isEnvValue(e.value)),
          renderMessage: () => validationMessage('app.env', 'value'),
        },
      ],
      memoryMb: [optionalMin('app.memoryMb', 16)],
      cpuPercent: [optionalMin('app.cpuPercent', 5)],
      healthPath: [checkRule('app.healthPath', 'path', isHealthPath)],
    }
    list.writable = [
      listRule('writable', 'path', (p) => p !== '' && isRelativePath(p)),
      maxRule('writable', MAX_WRITABLE),
    ]
    return list
  }
  list.root = [checkRule('root', 'path', isRelativePath)]
  list.index = [listRule('index', 'file_name', isIndexName), maxRule('index', MAX_INDEX)]
  if (form.type === 'php') {
    list.php = [{ ...requiredRule('php'), trigger: ['blur', 'change'] }]
    list.writable = [
      listRule('writable', 'path', (p) => p !== '' && isRelativePath(p)),
      maxRule('writable', MAX_WRITABLE),
    ]
  }
  return list
})

/** Checks the form and returns the request, or null when a field is invalid. */
async function collect(): Promise<SiteRequest | null> {
  try {
    await formRef.value?.validate()
  } catch {
    return null
  }
  const request: SiteRequest = { type: form.type, domains: form.domains }
  if (props.mode === 'create' && form.code.trim()) request.code = form.code.trim()
  if (form.tlsCert) {
    request.tls = { cert: form.tlsCert }
    if (form.tlsRedirect) request.tls.redirect = true
    if (form.tlsHsts) request.tls.hsts = true
  } else if (!props.certs && props.initial?.tls) {
    // Without access to certificates, keep the HTTPS settings as they are.
    request.tls = props.initial.tls
  }
  if (form.type === 'proxy') {
    request.proxy = { url: form.proxyUrl.trim() }
    if (form.webSocket) request.proxy.webSocket = true
    if (form.upstreamHost) request.proxy.upstreamHost = true
    return request
  }
  if (form.type === 'app') {
    const [runtime, line] = form.app.runtime ? form.app.runtime.split('@') : []
    // Port 0: on create the panel picks a free one; on update the current one stays.
    const app: AppSpec = { command: form.app.command.trim(), port: form.app.port ?? 0 }
    if (runtime && line) {
      app.runtime = runtime as AppRuntime
      app.line = line
    }
    if (form.app.workDir.trim()) app.workDir = form.app.workDir.trim()
    if (form.app.env.length) app.env = form.app.env.map((e) => ({ name: e.name.trim(), value: e.value }))
    if (form.app.memoryMb) app.memoryMb = form.app.memoryMb
    if (form.app.cpuPercent) app.cpuPercent = form.app.cpuPercent
    if (form.app.healthPath.trim()) app.healthPath = form.app.healthPath.trim()
    if (form.app.webSocket) app.webSocket = true
    request.app = app
    if (form.writable.length) request.writable = form.writable
    return request
  }
  if (form.root.trim()) request.root = form.root.trim()
  if (form.index.length) request.index = form.index
  if (form.rewrite) request.rewrite = form.rewrite
  if (form.type === 'php') {
    request.php = form.php ?? undefined
    if (form.writable.length) request.writable = form.writable
  }
  return request
}

defineExpose({ collect, reset })
</script>

<template>
  <NForm ref="formRef" :model="form" :rules="rules" :disabled="disabled" class="site-form">
    <NFormItem v-if="mode === 'create'" :label="t('common.field.type')" :show-feedback="false" class="block">
      <NRadioGroup :value="form.type" class="types" @update:value="onType">
        <NRadio
          v-for="type in SITE_TYPES"
          :key="type"
          :value="type"
          :disabled="type === 'app' && systemd === false"
          class="type"
        >
          <div class="type__name">{{ t(`sites.type.${type}.name`) }}</div>
          <NText depth="3" class="type__hint">
            {{ type === 'app' && systemd === false ? t('sites.app.noSystemd') : t(`sites.type.${type}.description`) }}
          </NText>
        </NRadio>
      </NRadioGroup>
    </NFormItem>

    <NFormItem path="domains" :label="t('common.field.domains')">
      <div class="field">
        <NDynamicTags :value="form.domains" @update:value="onDomains" />
        <NText depth="3" class="hint">{{ t('sites.form.domainsHint') }}</NText>
      </div>
    </NFormItem>

    <NFormItem v-if="mode === 'create'" path="code" :label="t('common.field.siteCode')">
      <div class="field">
        <NInput
          v-model:value="form.code"
          :maxlength="20"
          :placeholder="derivedCode || t('sites.form.codePlaceholder')"
          :input-props="{ autocomplete: 'off', spellcheck: false, autocapitalize: 'none' }"
        />
        <NText depth="3" class="hint">{{ t('sites.form.codeHint') }}</NText>
      </div>
    </NFormItem>

    <template v-if="isProxy">
      <NFormItem path="proxyUrl" :label="t('common.field.proxy')">
        <NInput
          v-model:value="form.proxyUrl"
          :placeholder="PROXY_EXAMPLE"
          :input-props="{ autocomplete: 'off', spellcheck: false, type: 'url' }"
        />
      </NFormItem>
      <div class="switches">
        <label class="switch">
          <NSwitch v-model:value="form.webSocket" />
          <span>
            {{ t('sites.form.webSocket') }}
            <NText depth="3" tag="div" class="hint">{{ t('sites.form.webSocketHint') }}</NText>
          </span>
        </label>
        <label class="switch">
          <NSwitch v-model:value="form.upstreamHost" />
          <span>
            {{ t('sites.form.upstreamHost') }}
            <NText depth="3" tag="div" class="hint">{{ t('sites.form.upstreamHostHint') }}</NText>
          </span>
        </label>
      </div>
    </template>

    <template v-if="isApp">
      <NFormItem :label="t('common.field.app.runtime')">
        <div class="field">
          <NSelect v-model:value="form.app.runtime" :options="runtimeOptions" />
          <NText depth="3" class="hint">
            {{ t('sites.app.runtimeHint') }}
            <RouterLink :to="{ name: 'software' }">{{ t('sites.app.installRuntime') }}</RouterLink>
          </NText>
        </div>
      </NFormItem>
      <NFormItem path="app.command" :label="t('common.field.app.command')">
        <div class="field">
          <NInput
            v-model:value="form.app.command"
            :placeholder="commandExample"
            :input-props="{ autocomplete: 'off', spellcheck: false }"
            class="mono"
          />
          <NText depth="3" class="hint">{{ t('sites.app.commandHint') }}</NText>
        </div>
      </NFormItem>
      <div class="row">
        <NFormItem path="app.workDir" :label="t('common.field.app.workDir')" class="row__wide">
          <NInput
            v-model:value="form.app.workDir"
            :placeholder="t('sites.overview.rootDefault')"
            :input-props="{ autocomplete: 'off', spellcheck: false }"
          />
        </NFormItem>
        <NFormItem path="app.port" :label="t('common.field.app.port')" class="row__narrow">
          <NInputNumber
            v-model:value="form.app.port"
            :min="1024"
            :max="65535"
            :show-button="false"
            clearable
            :placeholder="mode === 'create' ? t('sites.app.portAuto') : t('sites.app.portKeep')"
          />
        </NFormItem>
      </div>
      <label class="switch block">
        <NSwitch v-model:value="form.app.webSocket" />
        <span>
          {{ t('sites.form.webSocket') }}
          <NText depth="3" tag="div" class="hint">{{ t('sites.form.webSocketHint') }}</NText>
        </span>
      </label>
      <NFormItem path="app.env" :label="t('common.field.app.env')">
        <div class="field">
          <div v-for="(variable, index) in form.app.env" :key="index" class="env">
            <NInput
              v-model:value="variable.name"
              :placeholder="t('sites.app.envName')"
              :input-props="{ autocomplete: 'off', spellcheck: false }"
              class="env__name mono"
            />
            <NInput
              v-model:value="variable.value"
              type="password"
              show-password-on="click"
              :placeholder="t('sites.app.envValue')"
              :input-props="{ autocomplete: 'off', spellcheck: false }"
              class="env__value mono"
            />
            <NButton quaternary size="small" @click="removeEnv(index)">{{ t('common.action.delete') }}</NButton>
          </div>
          <NButton size="small" class="env__add" :disabled="form.app.env.length >= MAX_ENV" @click="addEnv">
            {{ t('sites.app.addEnv') }}
          </NButton>
          <NText depth="3" class="hint">{{ t('sites.app.envHint') }}</NText>
        </div>
      </NFormItem>
      <NFormItem path="writable" :label="t('common.field.writable')">
        <div class="field">
          <NDynamicTags v-model:value="form.writable" />
          <NText depth="3" class="hint">{{ t('sites.app.writableHint') }}</NText>
        </div>
      </NFormItem>
      <NCollapse class="block">
        <NCollapseItem :title="t('certs.issue.advanced')" name="advanced">
          <div class="row">
            <NFormItem path="app.memoryMb" :label="t('common.field.app.memoryMb')" class="row__narrow">
              <NInputNumber v-model:value="form.app.memoryMb" :min="0" :show-button="false" clearable :placeholder="t('sites.app.noLimit')" />
            </NFormItem>
            <NFormItem path="app.cpuPercent" :label="t('common.field.app.cpuPercent')" class="row__narrow">
              <NInputNumber v-model:value="form.app.cpuPercent" :min="0" :show-button="false" clearable :placeholder="t('sites.app.noLimit')" />
            </NFormItem>
          </div>
          <NText depth="3" tag="p" class="hint para">{{ t('sites.app.limitsHint') }}</NText>
          <NFormItem path="app.healthPath" :label="t('common.field.app.healthPath')">
            <div class="field">
              <NInput v-model:value="form.app.healthPath" placeholder="/health" :input-props="{ autocomplete: 'off', spellcheck: false }" />
              <NText depth="3" class="hint">{{ t('sites.app.healthHint') }}</NText>
            </div>
          </NFormItem>
        </NCollapseItem>
      </NCollapse>
    </template>

    <template v-if="servesFiles">
      <template v-if="isPhp">
        <NAlert v-if="phpLines.length === 0" type="warning" class="block">
          {{ t('sites.form.noPhp') }}
          <RouterLink :to="{ name: 'software' }">{{ t('sites.form.installPhp') }}</RouterLink>
        </NAlert>
        <NFormItem path="php" :label="t('common.field.php')">
          <NSelect v-model:value="form.php" :options="phpOptions" :disabled="phpLines.length === 0" />
        </NFormItem>
      </template>
      <NFormItem path="rewrite" :label="t('common.field.rewrite')">
        <NSelect
          :value="form.rewrite ?? ''"
          :options="rewriteOptions"
          @update:value="onRewrite"
        />
      </NFormItem>
      <NFormItem path="root" :label="t('common.field.root')">
        <div class="field">
          <NInput
            v-model:value="form.root"
            :placeholder="t('sites.form.rootPlaceholder')"
            :input-props="{ autocomplete: 'off', spellcheck: false }"
          />
          <NText depth="3" class="hint">{{ t('sites.form.rootHint') }}</NText>
        </div>
      </NFormItem>
      <NFormItem path="index" :label="t('common.field.index')">
        <div class="field">
          <NDynamicTags v-model:value="form.index" />
          <NText depth="3" class="hint">{{ t('sites.form.indexHint') }}</NText>
        </div>
      </NFormItem>
      <NFormItem v-if="isPhp" path="writable" :label="t('common.field.writable')">
        <div class="field">
          <NDynamicTags v-model:value="form.writable" />
          <NText depth="3" class="hint">{{ t('sites.form.writableHint') }}</NText>
        </div>
      </NFormItem>
    </template>

    <template v-if="certs">
      <div class="section">{{ t('sites.form.https') }}</div>
      <NFormItem path="tlsCert" :label="t('sites.form.certificate')">
        <div class="field">
          <NSelect :value="form.tlsCert ?? ''" :options="certOptions" @update:value="onCert" />
          <NText depth="3" class="hint">{{ t('sites.form.certHint') }}</NText>
        </div>
      </NFormItem>
      <div v-if="form.tlsCert" class="switches">
        <label class="switch">
          <NSwitch v-model:value="form.tlsRedirect" />
          <span>
            {{ t('sites.form.redirect') }}
            <NText depth="3" tag="div" class="hint">{{ t('sites.form.redirectHint') }}</NText>
          </span>
        </label>
        <label class="switch">
          <NSwitch v-model:value="form.tlsHsts" />
          <span>
            {{ t('sites.form.hsts') }}
            <NText depth="3" tag="div" class="hint">{{ t('sites.form.hstsHint') }}</NText>
          </span>
        </label>
      </div>
    </template>
  </NForm>
</template>

<style scoped>
.block {
  margin-block-end: 12px;
}

.types {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(190px, 1fr));
  gap: 8px;
  inline-size: 100%;
}

.type {
  padding: 10px 12px;
  border: 1px solid rgb(128 128 128 / 25%);
  border-radius: 6px;
}

.type__name {
  font-weight: 600;
}

.type__hint {
  font-size: 12px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  inline-size: 100%;
}

.hint {
  font-size: 12px;
}

.section {
  margin-block: 8px 12px;
  padding-block-start: 12px;
  border-block-start: 1px solid rgb(128 128 128 / 15%);
  font-weight: 600;
}

.switches {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-block-end: 16px;
}

.switch {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  cursor: pointer;
}
</style>

<style scoped>
.row {
  display: flex;
  flex-wrap: wrap;
  gap: 0 16px;
}

.row__wide {
  flex: 3 1 240px;
}

.row__narrow {
  flex: 1 1 140px;
}

.env {
  display: flex;
  gap: 6px;
}

.env__name {
  flex: 1 1 40%;
}

.env__value {
  flex: 1 1 60%;
}

.env__add {
  align-self: flex-start;
}

.mono :deep(input) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}

.para {
  margin-block: -8px 12px;
}
</style>
