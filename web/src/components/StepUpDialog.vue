<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NText,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { isApiError } from '@/api/client'
import { requiredRule, totpCodeRules } from '@/forms/rules'
import { finishStepUp, stepUpOpen } from '@/auth/stepUp'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const session = useSessionStore()

const formRef = ref<FormInst | null>(null)
const form = reactive({ password: '', code: '' })
const askCode = ref(false)
const submitting = ref(false)
const error = ref<unknown>(null)

// Immediate: the component may be loaded only when the first request arrives.
watch(
  stepUpOpen,
  (open) => {
    if (!open) return
    form.password = ''
    form.code = ''
    error.value = null
    askCode.value = session.user?.totpEnabled === true
  },
  { immediate: true },
)

// Pending requests cannot be retried once the session is gone.
watch(
  () => session.user,
  (user) => {
    if (!user && stepUpOpen.value) finishStepUp(false)
  },
)

const rules = computed<FormRules>(() => ({
  password: [requiredRule('password')],
  code: askCode.value ? totpCodeRules() : [],
}))

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  error.value = null
  submitting.value = true
  try {
    await session.elevate({
      password: form.password,
      code: askCode.value ? form.code : undefined,
    })
    finishStepUp(true)
  } catch (err) {
    // The server wants a code even though the profile said 2FA was off.
    if (isApiError(err) && err.code === 'auth.mfa_required') askCode.value = true
    error.value = err
  } finally {
    submitting.value = false
  }
}

function onShowChange(show: boolean) {
  if (!show) finishStepUp(false)
}
</script>

<template>
  <NModal
    :show="stepUpOpen"
    preset="card"
    :title="t('auth.stepUp.title')"
    :mask-closable="false"
    class="step-up"
    @update:show="onShowChange"
  >
    <NText depth="3" tag="p" class="step-up__intro">{{ t('auth.stepUp.intro') }}</NText>
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting" @submit.prevent="submit">
      <NAlert v-if="error" type="error" class="step-up__alert">{{ errorMessage(error) }}</NAlert>
      <NFormItem path="password" :label="t('common.field.password')">
        <NInput
          v-model:value="form.password"
          type="password"
          show-password-on="click"
          :input-props="{ autocomplete: 'current-password' }"
          autofocus
        />
      </NFormItem>
      <NFormItem v-if="askCode" path="code" :label="t('common.field.code')">
        <NInput
          v-model:value="form.code"
          :maxlength="6"
          :input-props="{ autocomplete: 'one-time-code', inputmode: 'numeric' }"
        />
      </NFormItem>
      <div class="step-up__actions">
        <NButton :disabled="submitting" @click="finishStepUp(false)">
          {{ t('common.action.cancel') }}
        </NButton>
        <NButton type="primary" attr-type="submit" :loading="submitting">
          {{ t('common.action.confirm') }}
        </NButton>
      </div>
    </NForm>
  </NModal>
</template>

<style scoped>
.step-up {
  inline-size: min(420px, calc(100vw - 32px));
}

.step-up__intro {
  margin-block: 0 16px;
}

.step-up__alert {
  margin-block-end: 16px;
}

.step-up__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
