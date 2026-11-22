<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isApiError, isStepUpRequired } from '@/api/client'
import { NOTE_MAX, addAddress, deleteAddress, type ChangeResult, type RuleAction } from '@/api/firewall'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import OptionGroup from '@/components/common/OptionGroup.vue'
import { Input } from '@/components/ui/input'
import type { ChangeInfo } from '@/composables/useFirewallConfirm'
import { isIpOrCidr, textLength } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'

// Trusts or blocks an address (or a range) on every port.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ clientIp: string }>()
const emit = defineEmits<{ changed: [result: ChangeResult, info: ChangeInfo] }>()

const { t } = useI18n()
// OptionGroup works with plain strings; the choices are the two actions.
const form = reactive({ address: '', action: 'deny' as string, note: '' })
const errors = ref<Partial<Record<'address' | 'note', string>>>({})
const submitting = ref(false)
const error = ref<unknown>(null)

watch(open, (value) => {
  if (!value) return
  Object.assign(form, { address: '', action: 'deny', note: '' })
  errors.value = {}
  error.value = null
})

const actions = computed(() => [
  { value: 'deny', label: t('firewall.address.deny'), hint: t('firewall.address.denyHint') },
  { value: 'allow', label: t('firewall.address.allow'), hint: t('firewall.address.allowHint') },
])

/** Blocking the address this browser comes from would lock it out. */
const blocksSelf = computed(() => form.action === 'deny' && !!props.clientIp && form.address.trim() === props.clientIp)

const steps = computed(() => {
  const address = form.address.trim() || '…'
  return [t(`firewall.address.steps.${form.action}`, { address }), t('firewall.confirm.step')]
})

function validate(): boolean {
  const next: typeof errors.value = {}
  const address = form.address.trim()
  if (!address) next.address = validationMessage('address', 'required')
  else if (!isIpOrCidr(address)) next.address = validationMessage('address', 'ip')
  if (textLength(form.note.trim()) > NOTE_MAX) next.note = validationMessage('note', 'max_length', { max: NOTE_MAX })
  errors.value = next
  return Object.keys(next).length === 0
}

/** Server errors about the address are shown under it. */
function placeError(err: unknown): boolean {
  if (!isApiError(err)) return false
  const field =
    err.code === 'validation.invalid' ? err.params.field : ['firewall.self_block', 'firewall.overlap'].includes(err.code) ? 'address' : ''
  if (field !== 'address' && field !== 'note') return false
  errors.value = { ...errors.value, [field]: errorMessage(err) }
  return true
}

async function submit() {
  if (!validate() || blocksSelf.value) return
  const action: RuleAction = form.action === 'allow' ? 'allow' : 'deny'
  const body = { address: form.address.trim(), action, note: form.note.trim() }
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    const result = await addAddress(body)
    emit('changed', result, {
      summary: t(`firewall.summary.address.${body.action}`, { address: result.address.address }),
      undo: () => deleteAddress(result.address.id),
    })
    open.value = false
  } catch (err) {
    if (isStepUpRequired(err)) return
    if (!placeError(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="t('firewall.address.createTitle')"
    :description="t('firewall.address.intro')"
    :submit-label="form.action === 'deny' ? t('firewall.address.block') : t('firewall.address.trust')"
    :submitting="submitting"
    :submit-disabled="blocksSelf"
    :steps="steps"
    @submit="submit"
  >
    <FormField id="address-address" :label="t('common.field.address')" :hint="t('firewall.address.hint')" :error="errors.address">
      <template #default="field">
        <Input
          v-bind="field"
          v-model="form.address"
          autocomplete="off"
          autocapitalize="none"
          spellcheck="false"
          dir="ltr"
          class="font-mono"
          placeholder="203.0.113.0/24"
        />
      </template>
    </FormField>

    <div class="flex flex-col gap-1.5">
      <span id="address-action-label" class="text-detail font-medium">{{ t('common.field.action') }}</span>
      <OptionGroup v-model="form.action" :options="actions" aria-labelledby="address-action-label" />
    </div>

    <FormField id="address-note" :label="t('common.field.note')" :error="errors.note">
      <template #default="field">
        <Input v-bind="field" v-model="form.note" :maxlength="NOTE_MAX" autocomplete="off" />
      </template>
    </FormField>

    <Notice v-if="blocksSelf" tone="destructive">{{ t('errors.detailed.firewall.self_block', { address: clientIp }) }}</Notice>
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
