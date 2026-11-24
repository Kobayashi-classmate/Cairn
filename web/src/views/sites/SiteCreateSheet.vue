<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { ChevronRight } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import { SITE_TYPES, createSite, type SiteType } from '@/api/sites'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import OptionGroup from '@/components/common/OptionGroup.vue'
import SelectField from '@/components/common/SelectField.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import TagInput from '@/components/common/TagInput.vue'
import type { OptionItem, SelectOption } from '@/components/common/types'
import { Input } from '@/components/ui/input'
import { useCertChoices } from '@/certs/useCertChoices'
import { useNavRoutes } from '@/composables/useNavRoutes'
import { errorMessage } from '@/i18n/errors'
import {
  coveringCerts,
  emptySiteForm,
  hasWritable,
  servesFiles,
  siteRequest,
  validateSiteForm,
  type FieldErrors,
} from '@/sites/form'
import { rewriteLabel } from '@/sites/labels'
import { domainsOnHosts, panelHosts } from '@/sites/panelHost'
import { useInstalledRuntimes } from '@/sites/useRuntimes'
import { MAX_DOMAINS, deriveCode, isIndexName, isRelativePath, isSiteHost, normalizeHost } from '@/sites/validate'
import { useSitesStore } from '@/stores/sites'
import { runSiteJob } from './actions'
import AppFields from './AppFields.vue'

// "New site" (interface spec, section 6: form sheet with "Will do").

/**
 * Example upstream for the empty proxy field. Kept out of the template: a
 * literal URL attribute there breaks vue-tsc's checking of the rest.
 */
const PROXY_EXAMPLE = 'http://127.0.0.1:3000'
const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const store = useSitesStore()
const navRoutes = useNavRoutes()
const { certs, reload: reloadCerts } = useCertChoices()
const { runtimes } = useInstalledRuntimes()

const form = reactive(emptySiteForm())
const errors = ref<FieldErrors>({})
const submitting = ref(false)
const error = ref<unknown>(null)
const domainsInput = ref<InstanceType<typeof TagInput> | null>(null)
const advancedOpen = ref(false)

/** Fields inside "Advanced"; an error there opens it. */
const ADVANCED_FIELDS = ['index', 'writable', 'app.workDir', 'app.memoryMb', 'app.cpuPercent', 'app.healthPath'] as const

function onToggleAdvanced(event: Event) {
  advancedOpen.value = (event.target as HTMLDetailsElement).open
}

const phpLines = computed(() => store.list?.php ?? [])
const systemd = computed(() => store.list?.systemd !== false)

// Mounted on first use (the page mounts it already open), hence immediate.
watch(
  open,
  (value) => {
    if (!value) return
    Object.assign(form, emptySiteForm('static', phpLines.value[0] ?? ''))
    errors.value = {}
    error.value = null
    advancedOpen.value = false
    void reloadCerts()
  },
  { immediate: true },
)

const typeOptions = computed<OptionItem[]>(() =>
  SITE_TYPES.map((type) => ({
    value: type,
    label: t(`sites.type.${type}.name`),
    hint: type === 'app' && !systemd.value ? t('sites.app.noSystemd') : t(`sites.type.${type}.description`),
    disabled: type === 'app' && !systemd.value,
  })),
)

const siteType = computed({
  get: () => form.type,
  set: (value: string) => {
    form.type = value as SiteType
    // A rewrite rule fits some types only.
    if (form.rewrite && !store.rewrites.some((rule) => rule.id === form.rewrite && rule.types.includes(form.type))) {
      form.rewrite = ''
    }
    errors.value = {}
  },
})

const phpOptions = computed<SelectOption[]>(() => phpLines.value.map((line) => ({ value: line, label: `PHP ${line}` })))

const rewriteOptions = computed<SelectOption[]>(() => [
  { value: '', label: t('sites.form.noRewrite') },
  ...store.rewrites.filter((rule) => rule.types.includes(form.type)).map((rule) => ({ value: rule.id, label: rewriteLabel(rule) })),
])

const covering = computed(() => coveringCerts(certs.value, form.domains))
/** Domains that are the panel's own host name: allowed, but warned about. */
const onPanelHost = computed(() => domainsOnHosts(form.domains, panelHosts(certs.value)))
const certOptions = computed<SelectOption[]>(() => [
  { value: '', label: t('sites.form.noTls') },
  ...covering.value.map((cert) => ({ value: cert.id, label: `${cert.name} (${cert.domains.join(', ')})` })),
])

// A certificate stops being offered when the domains change; so does the choice.
watch(covering, (list) => {
  if (form.tlsCert && !list.some((cert) => cert.id === form.tlsCert)) form.tlsCert = ''
})

const code = computed(() => form.code.trim() || (form.domains[0] ? deriveCode(form.domains[0]) : ''))

const steps = computed(() => {
  const c = code.value || t('sites.steps.codePlaceholder')
  const owner = `cw-${c}`
  const runner = `cw-${c}-run`
  const list = [t('sites.steps.users', { owner, runner }), t('sites.steps.dir', { dir: `/srv/www/${c}` })]
  if (form.type === 'php') list.push(t('sites.steps.php', { version: form.php || '?', runner }))
  if (form.type === 'app') {
    const port = form.app.port.trim()
    list.push(port ? t('sites.steps.app', { runner, port }) : t('sites.steps.appAuto', { runner }))
  }
  list.push(
    t('sites.steps.nginx', {
      domains: form.domains.length > 0 ? form.domains.join(', ') : t('sites.steps.domainsPlaceholder'),
    }),
  )
  const cert = covering.value.find((c) => c.id === form.tlsCert)
  if (cert) list.push(t('sites.steps.tls', { name: cert.name }))
  return list
})

async function submit() {
  domainsInput.value?.commit()
  errors.value = validateSiteForm(form, 'create', certs.value)
  if (Object.keys(errors.value).length > 0) {
    if (ADVANCED_FIELDS.some((field) => errors.value[field])) advancedOpen.value = true
    return
  }
  submitting.value = true
  error.value = null
  try {
    const request = siteRequest(form, 'create')
    await runSiteJob(() => createSite(request), { rethrow: true })
    open.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="t('sites.create.title')"
    :description="t('sites.create.description')"
    :submit-label="t('sites.create.submit')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <div class="flex flex-col gap-1.5">
      <span class="text-detail font-medium" id="site-type-label">{{ t('common.field.type') }}</span>
      <OptionGroup v-model="siteType" :options="typeOptions" aria-labelledby="site-type-label" />
    </div>

    <FormField id="site-domains" :label="t('common.field.domains')" :hint="t('sites.form.domainsHint')" :error="errors.domains">
      <template #default="field">
        <TagInput
          ref="domainsInput"
          v-bind="field"
          v-model="form.domains"
          :normalize="normalizeHost"
          :valid="isSiteHost"
          :max="MAX_DOMAINS"
          placeholder="example.com"
        />
      </template>
    </FormField>
    <Notice v-if="onPanelHost.length > 0" tone="warning">
      {{ t('sites.domains.panelHost', { domains: onPanelHost.join(', ') }, onPanelHost.length) }}
    </Notice>

    <FormField id="site-code" :label="t('common.field.siteCode')" :hint="t('sites.form.codeHint')" :error="errors.code">
      <template #default="field">
        <Input
          v-bind="field"
          v-model="form.code"
          maxlength="20"
          autocomplete="off"
          spellcheck="false"
          autocapitalize="none"
          dir="ltr"
          class="font-mono"
          :placeholder="code || t('sites.form.codePlaceholder')"
        />
      </template>
    </FormField>

    <template v-if="form.type === 'php'">
      <Notice v-if="phpLines.length === 0" tone="warning">
        {{ t('sites.form.noPhp') }}
        <template v-if="navRoutes.has('software')" #action>
          <RouterLink :to="{ name: 'software' }" class="underline underline-offset-2">{{ t('sites.form.installPhp') }}</RouterLink>
        </template>
      </Notice>
      <FormField id="site-php" :label="t('common.field.php')" :error="errors.php">
        <template #default="field">
          <SelectField v-bind="field" v-model="form.php" :options="phpOptions" :disabled="phpLines.length === 0" />
        </template>
      </FormField>
    </template>

    <template v-if="form.type === 'proxy'">
      <FormField id="site-proxy" :label="t('common.field.proxy')" :hint="t('sites.form.proxyHint')" :error="errors.proxyUrl">
        <template #default="field">
          <Input
            v-bind="field"
            v-model="form.proxyUrl"
            type="url"
            :placeholder="PROXY_EXAMPLE"
            autocomplete="off"
            spellcheck="false"
            dir="ltr"
            class="font-mono"
          />
        </template>
      </FormField>
      <SwitchField v-model="form.webSocket" :label="t('sites.form.webSocket')" :hint="t('sites.form.webSocketHint')" />
      <SwitchField
        v-model="form.upstreamHost"
        :label="t('sites.form.upstreamHost')"
        :hint="t('sites.form.upstreamHostHint')"
      />
    </template>

    <AppFields v-if="form.type === 'app'" :app="form.app" :errors="errors" :runtimes="runtimes" mode="create" part="main" id-prefix="site-app" />

    <template v-if="servesFiles(form.type)">
      <FormField id="site-rewrite" :label="t('common.field.rewrite')" :hint="t('sites.form.rewriteHint')">
        <template #default="field">
          <SelectField v-bind="field" v-model="form.rewrite" :options="rewriteOptions" />
        </template>
      </FormField>
      <FormField id="site-root" :label="t('common.field.root')" :hint="t('sites.form.rootHint')" :error="errors.root">
        <template #default="field">
          <Input
            v-bind="field"
            v-model="form.root"
            :placeholder="t('sites.form.rootPlaceholder')"
            autocomplete="off"
            spellcheck="false"
            dir="ltr"
            class="font-mono"
          />
        </template>
      </FormField>
    </template>

    <section class="flex flex-col gap-4 border-t pt-4" :aria-label="t('sites.form.https')">
      <h3 class="m-0 text-detail font-semibold">{{ t('sites.form.https') }}</h3>
      <template v-if="covering.length > 0">
        <FormField id="site-cert" :label="t('sites.form.certificate')" :hint="t('sites.form.certHint')" :error="errors.tlsCert">
          <template #default="field">
            <SelectField v-bind="field" v-model="form.tlsCert" :options="certOptions" />
          </template>
        </FormField>
        <template v-if="form.tlsCert">
          <SwitchField v-model="form.tlsRedirect" :label="t('sites.form.redirect')" :hint="t('sites.form.redirectHint')" />
          <SwitchField v-model="form.tlsHsts" :label="t('sites.form.hsts')" :hint="t('sites.form.hstsHint')" />
        </template>
      </template>
      <p v-else class="m-0 text-detail text-muted-foreground">{{ t('sites.create.httpsLater') }}</p>
    </section>

    <details
      v-if="form.type !== 'proxy'"
      :open="advancedOpen"
      class="group rounded-md border px-3 py-2 open:pb-4"
      @toggle="onToggleAdvanced"
    >
      <summary
        class="flex cursor-pointer list-none items-center gap-1.5 rounded-sm text-detail font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden"
      >
        <ChevronRight class="size-4 transition-transform group-open:rotate-90" aria-hidden="true" />
        {{ t('sites.create.advanced') }}
      </summary>
      <div class="mt-4 flex flex-col gap-5">
        <AppFields v-if="form.type === 'app'" :app="form.app" :errors="errors" :runtimes="runtimes" mode="create" part="advanced" id-prefix="site-app" />
        <FormField
          v-if="servesFiles(form.type)"
          id="site-index"
          :label="t('common.field.index')"
          :hint="t('sites.form.indexHint')"
          :error="errors.index"
        >
          <template #default="field">
            <TagInput v-bind="field" v-model="form.index" :valid="isIndexName" mono placeholder="index.html" />
          </template>
        </FormField>
        <FormField
          v-if="hasWritable(form.type)"
          id="site-writable"
          :label="t('common.field.writable')"
          :hint="form.type === 'app' ? t('sites.app.writableHint') : t('sites.form.writableHint')"
          :error="errors.writable"
        >
          <template #default="field">
            <TagInput
              v-bind="field"
              v-model="form.writable"
              :valid="(path: string) => path !== '' && isRelativePath(path)"
              mono
              placeholder="uploads"
            />
          </template>
        </FormField>
      </div>
    </details>

    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
