<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, LoaderCircle } from '@lucide/vue'
import { isApiError } from '@/api/client'
import FormField from '@/components/common/FormField.vue'
import Notice from '@/components/common/Notice.vue'
import OtpInput from '@/components/common/OtpInput.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { TOTP_CODE_PATTERN } from '@/forms/rules'
import AuthLayout from '@/layouts/AuthLayout.vue'
import { homeLocation } from '@/layouts/navigation'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { safeRedirect } from '@/router/redirect'
import { wizardDue } from '@/wizard/open'
import { useSessionStore } from '@/stores/session'

type Field = 'username' | 'password' | 'code' | 'recoveryCode'
type Rule = 'required' | 'pattern'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()

/** "password": username and password; "mfa": second factor for 2FA accounts. */
const step = ref<'password' | 'mfa'>('password')
const mfaToken = ref('')
const useRecoveryCode = ref(false)

const form = reactive({ username: '', password: '', code: '', recoveryCode: '' })
const submitting = ref(false)
const error = ref<unknown>(null)
// Why the user was sent here (for example an expired session).
const notice = ref(session.takeNotice())

// Failed checks per field; messages are rendered on display, so they follow
// language switches.
const invalid = reactive<Partial<Record<Field, Rule>>>({})

function fieldError(field: Field): string | null {
  const rule = invalid[field]
  return rule ? validationMessage(field, rule) : null
}

// Editing a field clears its error.
for (const field of ['username', 'password', 'code', 'recoveryCode'] as const) {
  watch(
    () => form[field],
    () => {
      delete invalid[field]
    },
  )
}

const usernameInput = ref<InstanceType<typeof Input> | null>(null)
const passwordInput = ref<InstanceType<typeof PasswordInput> | null>(null)
const otpInput = ref<InstanceType<typeof OtpInput> | null>(null)
const recoveryInput = ref<InstanceType<typeof Input> | null>(null)

function focusInput(component: { $el?: unknown } | null) {
  const el = component?.$el
  if (el instanceof HTMLInputElement) el.focus()
}

const subtitle = computed(() => {
  if (step.value === 'password') return t('auth.login.subtitle')
  return useRecoveryCode.value ? t('auth.mfa.recoverySubtitle') : t('auth.mfa.subtitle')
})

async function finish() {
  // The first-login wizard comes first while nobody has finished or skipped it.
  if (await wizardDue()) {
    await router.replace({ name: 'welcome' })
    return
  }
  await router.replace(safeRedirect(route.query.redirect) ?? homeLocation(router))
}

function validatePassword(): boolean {
  if (form.username === '') invalid.username = 'required'
  if (form.password === '') invalid.password = 'required'
  if (invalid.username) focusInput(usernameInput.value)
  else if (invalid.password) passwordInput.value?.focus()
  return !invalid.username && !invalid.password
}

/** Moves focus once the fields are enabled again after a request. */
async function focusAfter(target: () => void) {
  await nextTick()
  target()
}

async function submitPassword() {
  if (submitting.value || !validatePassword()) return
  error.value = null
  notice.value = null
  submitting.value = true
  let next: (() => void) | null = null
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
    next = () => void otpInput.value?.focus()
  } catch (err) {
    error.value = err
    form.password = ''
    next = () => passwordInput.value?.focus()
  } finally {
    submitting.value = false
  }
  if (next) await focusAfter(next)
}

function validateMfa(): boolean {
  if (useRecoveryCode.value) {
    if (form.recoveryCode.trim() === '') invalid.recoveryCode = 'required'
    if (invalid.recoveryCode) focusInput(recoveryInput.value)
    return !invalid.recoveryCode
  }
  if (form.code === '') invalid.code = 'required'
  else if (!TOTP_CODE_PATTERN.test(form.code)) invalid.code = 'pattern'
  if (invalid.code) void otpInput.value?.focus()
  return !invalid.code
}

async function submitMfa() {
  if (submitting.value || !validateMfa()) return
  error.value = null
  submitting.value = true
  let next: (() => void) | null = null
  try {
    await session.signInMfa(
      useRecoveryCode.value
        ? { mfaToken: mfaToken.value, recoveryCode: form.recoveryCode.trim() }
        : { mfaToken: mfaToken.value, code: form.code },
    )
    await finish()
  } catch (err) {
    if (isApiError(err) && err.code === 'auth.mfa_challenge_expired') {
      // The challenge is gone; start again with the password.
      void backToPassword()
    } else if (useRecoveryCode.value) {
      next = () => focusInput(recoveryInput.value)
    } else {
      form.code = ''
      next = () => void otpInput.value?.focus()
    }
    error.value = err
  } finally {
    submitting.value = false
  }
  if (next) await focusAfter(next)
}

async function toggleRecoveryCode() {
  useRecoveryCode.value = !useRecoveryCode.value
  error.value = null
  delete invalid.code
  delete invalid.recoveryCode
  await nextTick()
  if (useRecoveryCode.value) focusInput(recoveryInput.value)
  else void otpInput.value?.focus()
}

async function backToPassword() {
  step.value = 'password'
  mfaToken.value = ''
  form.password = ''
  error.value = null
  delete invalid.password
  await nextTick()
  passwordInput.value?.focus()
}
</script>

<template>
  <AuthLayout
    :title="step === 'password' ? t('auth.login.title') : t('auth.mfa.title')"
    :subtitle="subtitle"
  >
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
    <Notice v-else-if="notice" tone="warning">{{ errorMessage(notice) }}</Notice>

    <form
      v-if="step === 'password'"
      class="flex flex-col gap-4"
      novalidate
      @submit.prevent="submitPassword"
    >
      <FormField id="login-username" :label="t('common.field.username')" :error="fieldError('username')">
        <template #default="field">
          <Input
            ref="usernameInput"
            v-bind="field"
            v-model="form.username"
            class="h-10 max-md:h-11"
            autocomplete="username"
            autocapitalize="none"
            :spellcheck="false"
            autofocus
            :disabled="submitting"
          />
        </template>
      </FormField>
      <FormField id="login-password" :label="t('common.field.password')" :error="fieldError('password')">
        <template #default="field">
          <PasswordInput
            ref="passwordInput"
            v-bind="field"
            v-model="form.password"
            size="lg"
            autocomplete="current-password"
            :disabled="submitting"
          />
        </template>
      </FormField>
      <Button type="submit" size="lg" class="w-full font-semibold max-md:h-11" :disabled="submitting">
        <LoaderCircle v-if="submitting" class="animate-spin" aria-hidden="true" />
        {{ t('auth.login.submit') }}
      </Button>
      <i18n-t keypath="auth.login.forgot" tag="p" class="m-0 text-detail text-muted-foreground">
        <template #command>
          <code class="rounded-sm bg-muted px-1.5 py-px font-mono text-xs whitespace-nowrap text-foreground">cairn reset-password</code>
        </template>
      </i18n-t>
    </form>

    <form v-else class="flex flex-col gap-4" novalidate @submit.prevent="submitMfa">
      <FormField
        v-if="!useRecoveryCode"
        id="login-code"
        :label="t('common.field.code')"
        :error="fieldError('code')"
      >
        <template #default="field">
          <OtpInput
            ref="otpInput"
            v-model="form.code"
            :id="field.id"
            :described-by="field['aria-describedby']"
            :invalid="field['aria-invalid']"
            :disabled="submitting"
            @complete="submitMfa"
          />
        </template>
      </FormField>
      <FormField v-else id="login-recovery" :label="t('common.field.recoveryCode')" :error="fieldError('recoveryCode')">
        <template #default="field">
          <Input
            ref="recoveryInput"
            v-bind="field"
            v-model="form.recoveryCode"
            class="h-10 font-mono max-md:h-11"
            autocomplete="off"
            autocapitalize="none"
            :spellcheck="false"
            :disabled="submitting"
          />
        </template>
      </FormField>
      <Button type="submit" size="lg" class="w-full font-semibold max-md:h-11" :disabled="submitting">
        <LoaderCircle v-if="submitting" class="animate-spin" aria-hidden="true" />
        {{ t('auth.mfa.submit') }}
      </Button>
      <div class="flex flex-wrap items-center justify-between gap-3 text-detail">
        <button
          type="button"
          class="inline-flex items-center gap-1 rounded-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-50"
          :disabled="submitting"
          @click="backToPassword"
        >
          <ArrowLeft class="size-3.5" aria-hidden="true" />
          {{ t('auth.mfa.back') }}
        </button>
        <button
          type="button"
          class="rounded-sm font-medium text-link outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-50"
          :disabled="submitting"
          @click="toggleRecoveryCode"
        >
          {{ useRecoveryCode ? t('auth.mfa.useAuthenticator') : t('auth.mfa.useRecoveryCode') }}
        </button>
      </div>
      <i18n-t keypath="auth.mfa.lost" tag="p" class="m-0 text-detail text-muted-foreground">
        <template #command>
          <code class="rounded-sm bg-muted px-1.5 py-px font-mono text-xs whitespace-nowrap text-foreground">cairn reset-2fa</code>
        </template>
      </i18n-t>
    </form>
  </AuthLayout>
</template>
