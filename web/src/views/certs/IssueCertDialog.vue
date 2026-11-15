<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCollapse,
  NCollapseItem,
  NDynamicTags,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NRadio,
  NRadioGroup,
  NSelect,
  NSwitch,
  NText,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import {
  KEY_TYPES,
  fetchAcmeSettings,
  issueCert,
  type AcmeSettings,
  type Challenge,
  type IssueRequest,
  type KeyType,
} from '@/api/certs'
import { fetchDnsAccounts, type DnsAccount } from '@/api/dns'
import type { Job } from '@/api/jobs'
import { caAgreementUrl, caLabel } from '@/certs/labels'
import JobProgress from '@/components/JobProgress.vue'
import { listRule, requiredListRule } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { isSiteHost, normalizeHost } from '@/sites/validate'

/** Most names one certificate can have. */
const MAX_DOMAINS = 100
const IPV4 = /^\d{1,3}(\.\d{1,3}){3}$/

const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ changed: []; addDnsAccount: [] }>()

const { t } = useI18n()

const settings = ref<AcmeSettings | null>(null)
const accounts = ref<DnsAccount[]>([])
const formRef = ref<FormInst | null>(null)
const form = reactive({
  name: '',
  domains: [] as string[],
  ca: null as string | null,
  challenge: 'http-01' as Challenge,
  dnsAccount: null as string | null,
  keyType: 'ec256' as KeyType,
  autoRenew: true,
})
const submitting = ref(false)
const error = ref<unknown>(null)
const jobId = ref<string | null>(null)

watch(show, async (open) => {
  if (!open) return
  Object.assign(form, {
    name: '',
    domains: [],
    ca: null,
    challenge: 'http-01',
    dnsAccount: null,
    keyType: 'ec256',
    autoRenew: true,
  })
  error.value = null
  jobId.value = null
  try {
    const [loaded, list] = await Promise.all([fetchAcmeSettings(), fetchDnsAccounts()])
    settings.value = loaded
    accounts.value = list
    form.ca = loaded.defaultCa
  } catch (err) {
    error.value = err
  }
})

/** Lower-case, punycode, no duplicates. */
function onDomains(values: string[]) {
  form.domains = [...new Set(values.map(normalizeHost).filter(Boolean))]
}

const hasWildcard = computed(() => form.domains.some((d) => d.startsWith('*.')))

const caOptions = computed(() =>
  (settings.value?.cas ?? []).map((ca) => ({
    value: ca.name,
    label: ca.ready ? caLabel(ca.name) : t('certs.issue.caNotReady', { ca: caLabel(ca.name) }),
    disabled: !ca.ready,
  })),
)

const accountOptions = computed(() => accounts.value.map((a) => ({ value: a.id, label: a.name })))
const agreement = computed(() => (form.ca ? caAgreementUrl(form.ca) : null))

const rules = computed<FormRules>(() => ({
  domains: [
    requiredListRule('domains'),
    listRule('domains', 'host', (d) => isSiteHost(d) && !IPV4.test(d)),
    {
      trigger: 'change',
      validator: (_rule, value: unknown) => !Array.isArray(value) || value.length <= MAX_DOMAINS,
      renderMessage: () => validationMessage('domains', 'count'),
    },
    {
      trigger: 'change',
      // Wildcards can only be validated through DNS.
      validator: () => !hasWildcard.value || form.challenge === 'dns-01',
      renderMessage: () => validationMessage('domains', 'wildcard_needs_dns'),
    },
  ],
  dnsAccount:
    form.challenge === 'dns-01'
      ? [{ required: true, trigger: ['blur', 'change'], renderMessage: () => validationMessage('dnsAccount', 'required') }]
      : [],
}))

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  const request: IssueRequest = {
    domains: form.domains,
    challenge: form.challenge,
    keyType: form.keyType,
    autoRenew: form.autoRenew,
  }
  if (form.name.trim()) request.name = form.name.trim()
  if (form.ca) request.ca = form.ca
  if (form.challenge === 'dns-01' && form.dnsAccount) request.dnsAccount = form.dnsAccount
  submitting.value = true
  error.value = null
  try {
    jobId.value = (await issueCert(request)).id
    emit('changed')
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}

function onFinished(job: Job) {
  emit('changed')
  if (job.state === 'succeeded') show.value = false
}

function addAccount() {
  show.value = false
  emit('addDnsAccount')
}
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="t('certs.issue.title')" class="cert-dialog">
    <template v-if="!jobId">
      <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting">
        <NFormItem path="domains" :label="t('common.field.domains')">
          <div class="field">
            <NDynamicTags :value="form.domains" @update:value="onDomains" />
            <NText depth="3" class="hint">{{ t('certs.issue.domainsHint') }}</NText>
          </div>
        </NFormItem>
        <NFormItem path="name" :label="t('common.field.name')">
          <NInput v-model:value="form.name" :placeholder="form.domains[0] ?? ''" :input-props="{ autocomplete: 'off' }" />
        </NFormItem>
        <NFormItem path="ca" :label="t('common.field.ca.self')">
          <div class="field">
            <NSelect v-model:value="form.ca" :options="caOptions" />
            <NText depth="3" class="hint">
              {{ t('certs.issue.agreement') }}
              <a v-if="agreement" :href="agreement" target="_blank" rel="noopener noreferrer">
                {{ t('certs.issue.agreementLink') }}
              </a>
            </NText>
          </div>
        </NFormItem>
        <NFormItem path="challenge" :label="t('common.field.challenge')">
          <NRadioGroup v-model:value="form.challenge" class="challenges">
            <NRadio value="http-01">
              <div class="challenge__name">{{ t('certs.challenge.http01') }}</div>
              <NText depth="3" class="hint">{{ t('certs.challenge.http01Hint') }}</NText>
            </NRadio>
            <NRadio value="dns-01">
              <div class="challenge__name">{{ t('certs.challenge.dns01') }}</div>
              <NText depth="3" class="hint">{{ t('certs.challenge.dns01Hint') }}</NText>
            </NRadio>
          </NRadioGroup>
        </NFormItem>
        <NFormItem v-if="form.challenge === 'dns-01'" path="dnsAccount" :label="t('common.field.dnsAccount')">
          <div class="field">
            <NSelect v-model:value="form.dnsAccount" :options="accountOptions" />
            <NButton text type="primary" class="add-account" @click="addAccount">
              {{ t('certs.issue.addAccount') }}
            </NButton>
          </div>
        </NFormItem>
        <NFormItem :label="t('certs.issue.autoRenew')" :show-feedback="false">
          <NSwitch v-model:value="form.autoRenew" />
        </NFormItem>
        <NCollapse class="advanced">
          <NCollapseItem :title="t('certs.issue.advanced')" name="advanced">
            <NFormItem path="keyType" :label="t('common.field.keyType')" :show-feedback="false">
              <NSelect
                v-model:value="form.keyType"
                :options="KEY_TYPES.map((value) => ({ value, label: t(`certs.keyType.${value}`) }))"
              />
            </NFormItem>
          </NCollapseItem>
        </NCollapse>
      </NForm>
      <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
      <div class="actions">
        <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
        <NButton type="primary" :loading="submitting" @click="submit">{{ t('certs.issue.submit') }}</NButton>
      </div>
    </template>
    <template v-else>
      <JobProgress :job-id="jobId" @finished="onFinished" />
      <div class="actions">
        <NButton @click="show = false">{{ t('sites.create.close') }}</NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.cert-dialog {
  inline-size: min(640px, calc(100vw - 32px));
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

.challenges {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.challenge__name {
  font-weight: 500;
}

.add-account {
  align-self: flex-start;
}

.advanced {
  margin-block: 4px 12px;
}

.block {
  margin-block: 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
