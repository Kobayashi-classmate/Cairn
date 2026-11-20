<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { LoaderCircle } from '@lucide/vue'
import { isApiError } from '@/api/client'
import FormField from '@/components/common/FormField.vue'
import LocaleSelect from '@/components/common/LocaleSelect.vue'
import Notice from '@/components/common/Notice.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  PASSWORD_MIN,
  USERNAME_MAX,
  USERNAME_MIN,
  USERNAME_PATTERN,
  textLength,
} from '@/forms/rules'
import AuthLayout from '@/layouts/AuthLayout.vue'
import { homeLocation } from '@/layouts/navigation'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'
import { wizardDue } from '@/wizard/open'

type Field = 'token' | 'username' | 'password' | 'passwordConfirm'
interface Failure {
  rule: string
  params?: Record<string, number>
}

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()

// The installer prints a setup link carrying a one-time token; without it the
// token has to be pasted by hand.
const linkToken = typeof route.query.token === 'string' ? route.query.token.trim() : ''

const form = reactive({ token: linkToken, username: '', password: '', passwordConfirm: '' })
const submitting = ref(false)
const error = ref<unknown>(null)

// Failed checks per field, mirroring the server's rules; messages are
// rendered on display, so they follow language switches.
const invalid = reactive<Partial<Record<Field, Failure>>>({})

function fieldError(field: Field): string | null {
  const failure = invalid[field]
  return failure ? validationMessage(field, failure.rule, failure.params) : null
}

for (const field of ['token', 'username', 'password', 'passwordConfirm'] as const) {
  watch(
    () => form[field],
    () => {
      delete invalid[field]
    },
  )
}

function check(field: Field): Failure | undefined {
  const value = form[field]
  if (value.trim() === '') return { rule: 'required' }
  switch (field) {
    case 'username':
      if (textLength(value) < USERNAME_MIN) return { rule: 'min_length', params: { min: USERNAME_MIN } }
      if (textLength(value) > USERNAME_MAX) return { rule: 'max_length', params: { max: USERNAME_MAX } }
      if (!USERNAME_PATTERN.test(value)) return { rule: 'pattern' }
      return undefined
    case 'password':
      if (textLength(value) < PASSWORD_MIN) return { rule: 'min_length', params: { min: PASSWORD_MIN } }
      return undefined
    case 'passwordConfirm':
      return value === form.password ? undefined : { rule: 'mismatch' }
    default:
      return undefined
  }
}

const FIELD_IDS: Record<Field, string> = {
  token: 'setup-token',
  username: 'setup-username',
  password: 'setup-password',
  passwordConfirm: 'setup-password-confirm',
}

function validate(): boolean {
  const fields: Field[] = linkToken
    ? ['username', 'password', 'passwordConfirm']
    : ['token', 'username', 'password', 'passwordConfirm']
  let first: Field | null = null
  for (const field of fields) {
    const failure = check(field)
    if (failure) {
      invalid[field] = failure
      first ??= field
    }
  }
  if (first) document.getElementById(FIELD_IDS[first])?.focus()
  return first === null
}

async function submit() {
  if (submitting.value || !validate()) return
  error.value = null
  submitting.value = true
  try {
    await session.completeSetup({
      token: form.token.trim(),
      username: form.username,
      password: form.password,
      locale: locale.value,
    })
    await router.replace((await wizardDue()) ? { name: 'welcome' } : homeLocation(router))
  } catch (err) {
    if (isApiError(err) && err.code === 'setup.already_completed') {
      session.setupAlreadyCompleted(err)
      await router.replace({ name: 'login' })
      return
    }
    error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <AuthLayout :title="t('setup.title')" :subtitle="t('setup.intro')" :locale-switcher="false">
    <form class="flex flex-col gap-4" novalidate @submit.prevent="submit">
      <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>

      <FormField id="setup-locale" :label="t('common.field.locale')" :hint="t('setup.localeHint')">
        <template #default="field">
          <LocaleSelect
            :id="field.id"
            :described-by="field['aria-describedby']"
            class="w-full data-[size=default]:h-10 max-md:data-[size=default]:h-11"
          />
        </template>
      </FormField>
      <FormField
        v-if="!linkToken"
        :id="FIELD_IDS.token"
        :label="t('common.field.token')"
        :hint="t('setup.tokenHint')"
        :error="fieldError('token')"
      >
        <template #default="field">
          <Input
            v-bind="field"
            v-model="form.token"
            class="h-10 font-mono max-md:h-11"
            autocomplete="off"
            :spellcheck="false"
            :disabled="submitting"
          />
        </template>
      </FormField>
      <FormField
        :id="FIELD_IDS.username"
        :label="t('common.field.username')"
        :hint="t('setup.usernameHint', { min: USERNAME_MIN, max: USERNAME_MAX })"
        :error="fieldError('username')"
      >
        <template #default="field">
          <Input
            v-bind="field"
            v-model="form.username"
            class="h-10 max-md:h-11"
            :maxlength="USERNAME_MAX"
            autocomplete="username"
            autocapitalize="none"
            :spellcheck="false"
            :disabled="submitting"
          />
        </template>
      </FormField>
      <FormField
        :id="FIELD_IDS.password"
        :label="t('common.field.password')"
        :hint="t('setup.passwordHint', { min: PASSWORD_MIN }, PASSWORD_MIN)"
        :error="fieldError('password')"
      >
        <template #default="field">
          <PasswordInput
            v-bind="field"
            v-model="form.password"
            size="lg"
            autocomplete="new-password"
            :disabled="submitting"
          />
        </template>
      </FormField>
      <FormField
        :id="FIELD_IDS.passwordConfirm"
        :label="t('common.field.passwordConfirm')"
        :error="fieldError('passwordConfirm')"
      >
        <template #default="field">
          <PasswordInput
            v-bind="field"
            v-model="form.passwordConfirm"
            size="lg"
            autocomplete="new-password"
            :disabled="submitting"
          />
        </template>
      </FormField>
      <Button type="submit" size="lg" class="mt-1 w-full font-semibold max-md:h-11" :disabled="submitting">
        <LoaderCircle v-if="submitting" class="animate-spin" aria-hidden="true" />
        {{ t('setup.submit') }}
      </Button>
    </form>
  </AuthLayout>
</template>
