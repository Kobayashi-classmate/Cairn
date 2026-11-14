<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NForm,
  NFormItem,
  NInput,
  type FormInst,
  type FormRules,
  type InputInst,
} from 'naive-ui'
import { isApiError } from '@/api/client'
import { requiredRule, totpCodeRules } from '@/forms/rules'
import AuthLayout from '@/layouts/AuthLayout.vue'
import { homeLocation } from '@/layouts/navigation'
import { errorMessage } from '@/i18n/errors'
import { safeRedirect } from '@/router/redirect'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()

/** "password": username and password; "mfa": second factor for 2FA accounts. */
const step = ref<'password' | 'mfa'>('password')
const mfaToken = ref('')
const useRecoveryCode = ref(false)

const passwordForm = ref<FormInst | null>(null)
const mfaForm = ref<FormInst | null>(null)
const codeInput = ref<InputInst | null>(null)
const form = reactive({ username: '', password: '', code: '', recoveryCode: '' })
const submitting = ref(false)
const error = ref<unknown>(null)
// Why the user was sent here (for example an expired session).
const notice = ref(session.takeNotice())

// Messages are rendered on display, so they follow language switches.
const passwordRules: FormRules = {
  username: [requiredRule('username')],
  password: [requiredRule('password')],
}
const mfaRules = computed<FormRules>(() => ({
  code: useRecoveryCode.value ? [] : totpCodeRules(),
  recoveryCode: useRecoveryCode.value ? [requiredRule('recoveryCode')] : [],
}))

const subtitle = computed(() => {
  if (step.value === 'password') return t('auth.login.subtitle')
  return useRecoveryCode.value ? t('auth.mfa.recoverySubtitle') : t('auth.mfa.subtitle')
})

async function finish() {
  await router.replace(safeRedirect(route.query.redirect) ?? homeLocation(session.can))
}

async function submitPassword() {
  try {
    await passwordForm.value?.validate()
  } catch {
    return
  }
  error.value = null
  notice.value = null
  submitting.value = true
  try {
    const token = await session.signIn({ username: form.username, password: form.password })
    if (token === null) {
      await finish()
      return
    }
    mfaToken.value = token
    form.code = ''
    form.recoveryCode = ''
    useRecoveryCode.value = false
    step.value = 'mfa'
    await nextTick()
    codeInput.value?.focus()
  } catch (err) {
    error.value = err
    form.password = ''
  } finally {
    submitting.value = false
  }
}

async function submitMfa() {
  try {
    await mfaForm.value?.validate()
  } catch {
    return
  }
  error.value = null
  submitting.value = true
  try {
    await session.signInMfa(
      useRecoveryCode.value
        ? { mfaToken: mfaToken.value, recoveryCode: form.recoveryCode.trim() }
        : { mfaToken: mfaToken.value, code: form.code },
    )
    await finish()
  } catch (err) {
    error.value = err
    if (isApiError(err) && err.code === 'auth.mfa_challenge_expired') {
      // The challenge is gone; start again with the password.
      backToPassword()
      error.value = err
    }
  } finally {
    submitting.value = false
  }
}

function toggleRecoveryCode() {
  useRecoveryCode.value = !useRecoveryCode.value
  error.value = null
  mfaForm.value?.restoreValidation()
}

function backToPassword() {
  step.value = 'password'
  mfaToken.value = ''
  form.password = ''
  error.value = null
}
</script>

<template>
  <AuthLayout
    :title="step === 'password' ? t('auth.login.title') : t('auth.mfa.title')"
    :subtitle="subtitle"
  >
    <NAlert v-if="error" type="error" class="login__alert">{{ errorMessage(error) }}</NAlert>
    <NAlert v-else-if="notice" type="warning" class="login__alert">
      {{ errorMessage(notice) }}
    </NAlert>

    <NForm
      v-if="step === 'password'"
      ref="passwordForm"
      :model="form"
      :rules="passwordRules"
      :disabled="submitting"
      @submit.prevent="submitPassword"
    >
      <NFormItem path="username" :label="t('common.field.username')">
        <NInput
          v-model:value="form.username"
          :input-props="{ autocomplete: 'username', autocapitalize: 'none', spellcheck: false }"
          autofocus
        />
      </NFormItem>
      <NFormItem path="password" :label="t('common.field.password')">
        <NInput
          v-model:value="form.password"
          type="password"
          show-password-on="click"
          :input-props="{ autocomplete: 'current-password' }"
        />
      </NFormItem>
      <NButton type="primary" attr-type="submit" block :loading="submitting">
        {{ t('auth.login.submit') }}
      </NButton>
    </NForm>

    <NForm
      v-else
      ref="mfaForm"
      :model="form"
      :rules="mfaRules"
      :disabled="submitting"
      @submit.prevent="submitMfa"
    >
      <NFormItem v-if="!useRecoveryCode" path="code" :label="t('common.field.code')">
        <NInput
          ref="codeInput"
          v-model:value="form.code"
          :maxlength="6"
          :input-props="{ autocomplete: 'one-time-code', inputmode: 'numeric' }"
        />
      </NFormItem>
      <NFormItem v-else path="recoveryCode" :label="t('common.field.recoveryCode')">
        <NInput
          v-model:value="form.recoveryCode"
          :input-props="{ autocomplete: 'off', autocapitalize: 'none', spellcheck: false }"
        />
      </NFormItem>
      <NButton type="primary" attr-type="submit" block :loading="submitting">
        {{ t('auth.mfa.submit') }}
      </NButton>
      <div class="login__links">
        <NButton text type="primary" :disabled="submitting" @click="toggleRecoveryCode">
          {{ useRecoveryCode ? t('auth.mfa.useAuthenticator') : t('auth.mfa.useRecoveryCode') }}
        </NButton>
        <NButton text :disabled="submitting" @click="backToPassword">
          {{ t('auth.mfa.back') }}
        </NButton>
      </div>
    </NForm>
  </AuthLayout>
</template>

<style scoped>
.login__alert {
  margin-block-end: 16px;
}

.login__links {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
