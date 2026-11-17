<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Trusts or blocks an address (or range) on every port.
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NRadio,
  NRadioGroup,
  NText,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { NOTE_MAX, addAddress, type ChangeResult, type RuleAction } from '@/api/firewall'
import { checkRule, isIpOrCidr, requiredRule } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ clientIp: string }>()
const emit = defineEmits<{ changed: [result: ChangeResult] }>()

const { t } = useI18n()
const formRef = ref<FormInst | null>(null)
const form = reactive({ address: '', action: 'deny' as RuleAction, note: '' })
const submitting = ref(false)
const error = ref<unknown>(null)

watch(show, async (open) => {
  if (!open) return
  Object.assign(form, { address: '', action: 'deny', note: '' })
  error.value = null
  await nextTick()
  formRef.value?.restoreValidation()
})

/** Blocking the address this browser comes from would lock it out. */
const blocksSelf = computed(() => form.action === 'deny' && !!props.clientIp && form.address.trim() === props.clientIp)

const rules = computed<FormRules>(() => ({
  address: [requiredRule('address'), checkRule('address', 'ip', (v) => isIpOrCidr(v.trim()))],
  note: [checkRule('note', 'max_length', (v) => [...v].length <= NOTE_MAX, { max: NOTE_MAX })],
}))

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    emit('changed', await addAddress({ address: form.address.trim(), action: form.action, note: form.note.trim() }))
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="t('firewall.address.createTitle')" class="address-dialog" :mask-closable="false">
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting" label-placement="top">
      <NFormItem path="address" :label="t('common.field.address')">
        <div class="field">
          <NInput v-model:value="form.address" :input-props="{ autocomplete: 'off', spellcheck: false }" />
          <NText depth="3" class="hint">{{ t('firewall.address.hint') }}</NText>
        </div>
      </NFormItem>
      <NFormItem :label="t('common.field.action')" :show-feedback="false" class="gap">
        <NRadioGroup v-model:value="form.action" class="choices">
          <NRadio value="deny">
            <span class="choice">
              <span>{{ t('firewall.address.deny') }}</span>
              <NText depth="3" class="hint">{{ t('firewall.address.denyHint') }}</NText>
            </span>
          </NRadio>
          <NRadio value="allow">
            <span class="choice">
              <span>{{ t('firewall.address.allow') }}</span>
              <NText depth="3" class="hint">{{ t('firewall.address.allowHint') }}</NText>
            </span>
          </NRadio>
        </NRadioGroup>
      </NFormItem>
      <NFormItem path="note" :label="t('common.field.note')">
        <NInput v-model:value="form.note" :maxlength="NOTE_MAX" />
      </NFormItem>
    </NForm>
    <NAlert v-if="blocksSelf" type="error" class="block">{{ t('errors.firewall.self_block') }}</NAlert>
    <NText depth="3" tag="p" class="hint">{{ t('firewall.confirm.explain') }}</NText>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton :type="form.action === 'deny' ? 'error' : 'primary'" :loading="submitting" :disabled="blocksSelf" @click="submit">
        {{ t('common.action.save') }}
      </NButton>
    </div>
  </NModal>
</template>

<style scoped>
.address-dialog {
  inline-size: min(520px, calc(100vw - 32px));
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

.gap {
  margin-block-end: 16px;
}

.choices {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.choice {
  display: flex;
  flex-direction: column;
}

.block {
  margin-block: 8px 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 12px;
}
</style>
