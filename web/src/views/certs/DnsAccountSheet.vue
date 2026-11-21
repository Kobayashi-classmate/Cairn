<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ExternalLink } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import { createDnsAccount, updateDnsAccount, type DnsAccount, type DnsProvider } from '@/api/dns'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import SelectField from '@/components/common/SelectField.vue'
import type { SelectOption } from '@/components/common/types'
import { Input } from '@/components/ui/input'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { notifySuccess } from '@/lib/notify'

// Add or change a DNS provider account. Secrets are written, never shown:
// on change, an empty secret field keeps the stored value. Saving asks for
// the password again (step-up).
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{
  providers: readonly DnsProvider[]
  /** The account to change, or null to add one. */
  account: DnsAccount | null
}>()
const emit = defineEmits<{ saved: [] }>()

const { t, te } = useI18n()

const form = reactive({ name: '', provider: '', fields: {} as Record<string, string> })
const errors = ref<Record<string, string>>({})
const submitting = ref(false)
const error = ref<unknown>(null)

const editing = computed(() => props.account !== null)
const provider = computed(() => props.providers.find((p) => p.id === form.provider) ?? null)

watch(open, (value) => {
  if (!value) return
  const account = props.account
  form.name = account?.name ?? ''
  form.provider = account?.provider ?? ''
  // Secrets are never sent back; their fields start empty.
  form.fields = { ...(account?.fields ?? {}) }
  fillFields()
  errors.value = {}
  error.value = null
})

/** Every field of the chosen provider has a value to bind to. */
function fillFields() {
  for (const field of provider.value?.fields ?? []) {
    if (form.fields[field.name] === undefined) form.fields[field.name] = ''
  }
}

watch(provider, fillFields, { immediate: true })

function fieldLabel(name: string): string {
  const key = `dns.field.${name}`
  return te(key) ? t(key) : name
}

function secretStored(name: string): boolean {
  return props.account?.secretSet.includes(name) === true
}

const providerOptions = computed<SelectOption[]>(() => props.providers.map((p) => ({ value: p.id, label: p.name })))

function validate(): boolean {
  const next: Record<string, string> = {}
  if (!form.provider) next.provider = validationMessage('provider', 'required')
  if (!form.name.trim()) next.name = validationMessage('name', 'required')
  for (const field of provider.value?.fields ?? []) {
    if (field.optional || (field.secret && secretStored(field.name))) continue
    if (!(form.fields[field.name] ?? '').trim()) {
      next[`fields.${field.name}`] = t('errors.validation.rule.required', { field: fieldLabel(field.name) })
    }
  }
  errors.value = next
  return Object.keys(next).length === 0
}

async function submit() {
  if (!validate()) return
  // Only the provider's own fields; empty ones are left out (a stored secret is then kept).
  const fields: Record<string, string> = {}
  for (const field of provider.value?.fields ?? []) {
    const value = (form.fields[field.name] ?? '').trim()
    if (value !== '') fields[field.name] = value
  }
  const body = { name: form.name.trim(), provider: form.provider, fields }
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    if (props.account) await updateDnsAccount(props.account.id, body)
    else await createDnsAccount(body)
    notifySuccess(t('dns.saved', { name: body.name }))
    emit('saved')
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
    :title="editing ? t('dns.dialog.editTitle') : t('dns.dialog.createTitle')"
    :description="t('dns.dialog.description')"
    :submit-label="editing ? t('common.action.save') : t('dns.dialog.create')"
    :submitting="submitting"
    @submit="submit"
  >
    <FormField id="dns-provider" :label="t('common.field.provider')" :error="errors.provider">
      <template #default="field">
        <SelectField
          v-bind="field"
          v-model="form.provider"
          :options="providerOptions"
          :placeholder="t('dns.dialog.pickProvider')"
          :disabled="editing"
        />
      </template>
    </FormField>
    <a
      v-if="provider?.homepage"
      :href="provider.homepage"
      target="_blank"
      rel="noopener noreferrer"
      class="-mt-3 inline-flex items-center gap-1 self-start text-detail text-link underline-offset-2 hover:underline"
    >
      {{ t('dns.dialog.providerSite', { name: provider.name }) }}
      <ExternalLink class="size-3" aria-hidden="true" />
    </a>

    <FormField id="dns-name" :label="t('common.field.name')" :error="errors.name">
      <template #default="field">
        <Input v-bind="field" v-model="form.name" maxlength="100" autocomplete="off" />
      </template>
    </FormField>

    <template v-if="provider">
      <FormField
        v-for="field in provider.fields"
        :id="`dns-field-${field.name}`"
        :key="field.name"
        :label="field.optional ? t('dns.dialog.optional', { field: fieldLabel(field.name) }) : fieldLabel(field.name)"
        :hint="field.secret && secretStored(field.name) ? t('dns.dialog.secretStored') : undefined"
        :error="errors[`fields.${field.name}`]"
      >
        <template #default="control">
          <PasswordInput
            v-if="field.secret"
            v-bind="control"
            v-model="form.fields[field.name]!"
            autocomplete="new-password"
            spellcheck="false"
            dir="ltr"
            class="[&_input]:font-mono"
          />
          <Input
            v-else
            v-bind="control"
            v-model="form.fields[field.name]!"
            autocomplete="off"
            spellcheck="false"
            dir="ltr"
            class="font-mono"
          />
        </template>
      </FormField>
    </template>

    <Notice tone="info">{{ t('dns.dialog.stepUp') }}</Notice>
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
