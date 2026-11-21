<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight, ExternalLink } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import { KEY_TYPES, fetchAcmeSettings, issueCert, type AcmeSettings, type Challenge, type IssueRequest, type KeyType } from '@/api/certs'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import OptionGroup from '@/components/common/OptionGroup.vue'
import SelectField from '@/components/common/SelectField.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import TagInput from '@/components/common/TagInput.vue'
import type { OptionItem, SelectOption } from '@/components/common/types'
import { Input } from '@/components/ui/input'
import { caAgreementUrl, caLabel } from '@/certs/labels'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { isSiteHost, normalizeHost } from '@/sites/validate'
import { useCertsStore } from '@/stores/certs'
import { runCertJob } from './actions'

// Issue a certificate through ACME (HTTP-01 or DNS-01 with a DNS account).
const MAX_DOMAINS = 100
const IPV4 = /^\d{1,3}(\.\d{1,3}){3}$/

const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{
  /** Names to start with (from a site's HTTPS settings). */
  initialDomains?: readonly string[]
}>()
const emit = defineEmits<{ addDnsAccount: [] }>()

const { t } = useI18n()
const store = useCertsStore()

const settings = ref<AcmeSettings | null>(null)
const settingsError = ref<unknown>(null)
const form = reactive({
  name: '',
  domains: [] as string[],
  ca: '',
  challenge: 'http-01' as Challenge,
  dnsAccount: '',
  keyType: 'ec256' as KeyType,
  autoRenew: true,
})
const errors = ref<Partial<Record<'domains' | 'dnsAccount' | 'ca', string>>>({})
const submitting = ref(false)
const error = ref<unknown>(null)
const domainsInput = ref<InstanceType<typeof TagInput> | null>(null)

// Immediate: the page may mount the sheet already open (?issue= from a site).
watch(open, async (value) => {
  if (!value) return
  Object.assign(form, {
    name: '',
    domains: [...new Set((props.initialDomains ?? []).map(normalizeHost).filter(Boolean))],
    ca: settings.value?.defaultCa ?? '',
    challenge: (props.initialDomains ?? []).some((d) => d.startsWith('*.')) ? 'dns-01' : 'http-01',
    dnsAccount: '',
    keyType: 'ec256',
    autoRenew: true,
  })
  errors.value = {}
  error.value = null
  settingsError.value = null
  try {
    const [loaded] = await Promise.all([fetchAcmeSettings(), store.accounts ? Promise.resolve() : store.loadAccounts()])
    settings.value = loaded
    if (!form.ca) form.ca = loaded.defaultCa
  } catch (err) {
    settingsError.value = err
  }
}, { immediate: true })

const hasWildcard = computed(() => form.domains.some((d) => d.startsWith('*.')))

const caOptions = computed<SelectOption[]>(() =>
  (settings.value?.cas ?? []).map((ca) => ({
    value: ca.name,
    label: ca.ready ? caLabel(ca.name) : t('certs.issue.caNotReady', { ca: caLabel(ca.name) }),
    disabled: !ca.ready,
  })),
)

const challengeOptions = computed<OptionItem[]>(() => [
  { value: 'http-01', label: t('certs.challenge.http01'), hint: t('certs.challenge.http01Hint') },
  { value: 'dns-01', label: t('certs.challenge.dns01'), hint: t('certs.challenge.dns01Hint') },
])

const challenge = computed({
  get: () => form.challenge,
  set: (value: string) => {
    form.challenge = value as Challenge
  },
})

const accountOptions = computed<SelectOption[]>(() => (store.accounts ?? []).map((a) => ({ value: a.id, label: a.name })))
const keyOptions = computed<SelectOption[]>(() => KEY_TYPES.map((value) => ({ value, label: t(`certs.keyType.${value}`) })))
const keyType = computed({
  get: () => form.keyType,
  set: (value: string) => {
    form.keyType = value as KeyType
  },
})

const agreement = computed(() => (form.ca ? caAgreementUrl(form.ca) : null))

const steps = computed(() => {
  const domains = form.domains.length > 0 ? form.domains.join(', ') : t('certs.steps.domainsPlaceholder')
  const ca = form.ca ? caLabel(form.ca) : t('certs.steps.caPlaceholder')
  const account = store.accounts?.find((a) => a.id === form.dnsAccount)?.name ?? t('certs.steps.accountPlaceholder')
  const list = [
    t('certs.steps.order', { ca, domains }),
    form.challenge === 'dns-01' ? t('certs.steps.dns01', { account }) : t('certs.steps.http01'),
    t('certs.steps.store', { key: t(`certs.keyType.${form.keyType}`) }),
  ]
  if (form.autoRenew) list.push(t('certs.steps.renew'))
  return list
})

function hostValid(host: string): boolean {
  return isSiteHost(host) && !IPV4.test(host)
}

function validate(): boolean {
  const next: typeof errors.value = {}
  if (form.domains.length === 0) next.domains = validationMessage('domains', 'required')
  else if (form.domains.length > MAX_DOMAINS) next.domains = validationMessage('domains', 'count')
  else if (!form.domains.every(hostValid)) next.domains = validationMessage('domains', 'host')
  else if (hasWildcard.value && form.challenge !== 'dns-01') next.domains = validationMessage('domains', 'wildcard_needs_dns')
  if (form.challenge === 'dns-01' && !form.dnsAccount) next.dnsAccount = validationMessage('dnsAccount', 'required')
  errors.value = next
  return Object.keys(next).length === 0
}

async function submit() {
  domainsInput.value?.commit()
  if (!validate()) return
  const request: IssueRequest = {
    domains: [...form.domains],
    challenge: form.challenge,
    keyType: form.keyType,
    autoRenew: form.autoRenew,
  }
  if (form.name.trim()) request.name = form.name.trim()
  if (form.ca) request.ca = form.ca
  if (form.challenge === 'dns-01') request.dnsAccount = form.dnsAccount
  submitting.value = true
  error.value = null
  try {
    await runCertJob(() => issueCert(request), { rethrow: true })
    open.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}

function addAccount() {
  open.value = false
  emit('addDnsAccount')
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="t('certs.issue.title')"
    :description="t('certs.issue.description')"
    :submit-label="t('certs.issue.submit')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <Notice v-if="settingsError" tone="destructive">{{ errorMessage(settingsError) }}</Notice>

    <FormField id="issue-domains" :label="t('common.field.domains')" :hint="t('certs.issue.domainsHint')" :error="errors.domains">
      <template #default="field">
        <TagInput
          ref="domainsInput"
          v-bind="field"
          v-model="form.domains"
          :normalize="normalizeHost"
          :valid="hostValid"
          :max="MAX_DOMAINS"
          placeholder="example.com"
        />
      </template>
    </FormField>

    <FormField id="issue-name" :label="t('common.field.name')" :hint="t('certs.issue.nameHint')">
      <template #default="field">
        <Input v-bind="field" v-model="form.name" maxlength="100" autocomplete="off" :placeholder="form.domains[0] ?? ''" />
      </template>
    </FormField>

    <FormField id="issue-ca" :label="t('common.field.ca.self')" :error="errors.ca">
      <template #default="field">
        <SelectField v-bind="field" v-model="form.ca" :options="caOptions" :disabled="!settings" />
      </template>
    </FormField>
    <p class="-mt-3 text-xs text-muted-foreground">
      {{ t('certs.issue.agreement') }}
      <a
        v-if="agreement"
        :href="agreement"
        target="_blank"
        rel="noopener noreferrer"
        class="inline-flex items-center gap-0.5 text-link underline-offset-2 hover:underline"
      >
        {{ t('certs.issue.agreementLink') }}
        <ExternalLink class="size-3" aria-hidden="true" />
      </a>
    </p>

    <div class="flex flex-col gap-1.5">
      <span id="issue-challenge-label" class="text-detail font-medium">{{ t('common.field.challenge') }}</span>
      <OptionGroup v-model="challenge" :options="challengeOptions" aria-labelledby="issue-challenge-label" />
    </div>

    <template v-if="form.challenge === 'dns-01'">
      <FormField id="issue-account" :label="t('common.field.dnsAccount')" :error="errors.dnsAccount">
        <template #default="field">
          <SelectField
            v-bind="field"
            v-model="form.dnsAccount"
            :options="accountOptions"
            :placeholder="accountOptions.length === 0 ? t('certs.issue.noAccounts') : t('certs.issue.pickAccount')"
            :disabled="accountOptions.length === 0"
          />
        </template>
      </FormField>
      <button type="button" class="-mt-3 self-start text-detail text-link underline-offset-2 hover:underline" @click="addAccount">
        {{ t('certs.issue.addAccount') }}
      </button>
    </template>

    <SwitchField v-model="form.autoRenew" :label="t('certs.issue.autoRenew')" :hint="t('certs.drawer.autoRenewHint')" />

    <details class="group rounded-md border px-3 py-2 open:pb-4">
      <summary
        class="flex cursor-pointer list-none items-center gap-1.5 rounded-sm text-detail font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden"
      >
        <ChevronRight class="size-4 transition-transform group-open:rotate-90" aria-hidden="true" />
        {{ t('certs.issue.advanced') }}
      </summary>
      <div class="mt-4">
        <FormField id="issue-key" :label="t('common.field.keyType')">
          <template #default="field">
            <SelectField v-bind="field" v-model="keyType" :options="keyOptions" />
          </template>
        </FormField>
      </div>
    </details>

    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
