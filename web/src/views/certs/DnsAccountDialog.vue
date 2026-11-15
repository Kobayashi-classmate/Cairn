<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NForm, NFormItem, NInput, NModal, NSelect, NText, type FormInst, type FormRules } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { createDnsAccount, updateDnsAccount, type DnsAccount, type DnsProvider } from '@/api/dns'
import { requiredRule } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{
  providers: DnsProvider[]
  /** The account to edit, or null to add one. */
  account: DnsAccount | null
}>()
const emit = defineEmits<{ saved: [] }>()

const { t, te } = useI18n()

const formRef = ref<FormInst | null>(null)
const form = reactive({ name: '', provider: null as string | null, fields: {} as Record<string, string> })
const submitting = ref(false)
const error = ref<unknown>(null)

const editing = computed(() => props.account !== null)
const provider = computed(() => props.providers.find((p) => p.id === form.provider) ?? null)

watch(show, (open) => {
  if (!open) return
  const account = props.account
  form.name = account?.name ?? ''
  form.provider = account?.provider ?? null
  // Secrets are never sent back; their fields start empty.
  form.fields = { ...(account?.fields ?? {}) }
  error.value = null
})

function fieldLabel(name: string): string {
  const key = `dns.field.${name}`
  return te(key) ? t(key) : name
}

/** A stored secret can stay as it is: its field may be left empty. */
function secretStored(name: string): boolean {
  return props.account?.secretSet.includes(name) === true
}

const providerOptions = computed(() => props.providers.map((p) => ({ value: p.id, label: p.name })))

const rules = computed<FormRules>(() => {
  const list: FormRules = {
    name: [requiredRule('name')],
    provider: [{ ...requiredRule('provider'), trigger: ['blur', 'change'] }],
  }
  for (const field of provider.value?.fields ?? []) {
    if (field.optional || (field.secret && secretStored(field.name))) continue
    list[`fields.${field.name}`] = [
      {
        required: true,
        trigger: 'blur',
        renderMessage: () => t('errors.validation.rule.required', { field: fieldLabel(field.name) }),
      },
    ]
  }
  return list
})

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  if (!form.provider) return
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
    if (props.account) await updateDnsAccount(props.account.id, body)
    else await createDnsAccount(body)
    emit('saved')
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    :title="editing ? t('dns.dialog.editTitle') : t('dns.dialog.createTitle')"
    class="dns-dialog"
  >
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting">
      <NFormItem path="provider" :label="t('common.field.provider')">
        <NSelect v-model:value="form.provider" :options="providerOptions" :disabled="editing" filterable />
      </NFormItem>
      <NFormItem path="name" :label="t('common.field.name')">
        <NInput v-model:value="form.name" :input-props="{ autocomplete: 'off' }" />
      </NFormItem>
      <template v-if="provider">
        <NText v-if="provider.homepage" depth="3" tag="p" class="hint">
          <a :href="provider.homepage" target="_blank" rel="noopener noreferrer">{{ t('dns.dialog.providerSite', { name: provider.name }) }}</a>
        </NText>
        <NFormItem
          v-for="field in provider.fields"
          :key="field.name"
          :path="`fields.${field.name}`"
          :label="field.optional ? t('dns.dialog.optional', { field: fieldLabel(field.name) }) : fieldLabel(field.name)"
        >
          <NInput
            v-model:value="form.fields[field.name]"
            :type="field.secret ? 'password' : 'text'"
            :show-password-on="field.secret ? 'click' : undefined"
            :placeholder="field.secret && secretStored(field.name) ? t('dns.dialog.secretStored') : ''"
            :input-props="{ autocomplete: field.secret ? 'new-password' : 'off', spellcheck: false }"
          />
        </NFormItem>
      </template>
    </NForm>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" @click="submit">{{ t('common.action.save') }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.dns-dialog {
  inline-size: min(560px, calc(100vw - 32px));
}

.hint {
  margin-block: 0 12px;
  font-size: 13px;
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
