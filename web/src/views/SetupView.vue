<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NForm,
  NFormItem,
  NInput,
  NSelect,
  NText,
  type FormInst,
  type FormItemRule,
  type FormRules,
} from 'naive-ui'
import { isApiError } from '@/api/client'
import AuthLayout from '@/layouts/AuthLayout.vue'
import { useLocaleSwitch } from '@/composables/useLocaleSwitch'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

// Must match the server's validation rules.
const USERNAME_MIN = 3
const USERNAME_MAX = 32
const USERNAME_PATTERN = /^[a-z][a-z0-9_.-]*$/
const PASSWORD_MIN = 12

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const { locale, options: localeOptions, renderLabel, switching, select } = useLocaleSwitch()

// The installer prints a setup link carrying a one-time token; without it the
// token has to be pasted by hand.
const linkToken = typeof route.query.token === 'string' ? route.query.token.trim() : ''

const formRef = ref<FormInst | null>(null)
const form = reactive({ token: linkToken, username: '', password: '', passwordConfirm: '' })
const submitting = ref(false)
const error = ref<unknown>(null)

/** Length in Unicode code points, the way the server counts it. */
const length = (value: string) => [...value].length

function required(field: string): FormItemRule {
  return {
    required: true,
    trigger: 'blur',
    renderMessage: () => validationMessage(field, 'required'),
  }
}

function check(
  field: string,
  rule: string,
  test: (value: string) => boolean,
  params: Record<string, number> = {},
): FormItemRule {
  return {
    trigger: ['blur', 'input'],
    // Empty values are reported by the "required" rule instead.
    validator: (_rule, value: string) => value === '' || test(value),
    renderMessage: () => validationMessage(field, rule, params),
  }
}

const rules: FormRules = {
  token: [required('token')],
  username: [
    required('username'),
    check('username', 'min_length', (v) => length(v) >= USERNAME_MIN, { min: USERNAME_MIN }),
    check('username', 'max_length', (v) => length(v) <= USERNAME_MAX, { max: USERNAME_MAX }),
    check('username', 'pattern', (v) => USERNAME_PATTERN.test(v)),
  ],
  password: [
    required('password'),
    check('password', 'min_length', (v) => length(v) >= PASSWORD_MIN, { min: PASSWORD_MIN }),
  ],
  passwordConfirm: [
    required('passwordConfirm'),
    check('passwordConfirm', 'mismatch', (v) => v === form.password),
  ],
}

function onLocaleChange(value: string) {
  void select(value)
}

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  error.value = null
  submitting.value = true
  try {
    await session.completeSetup({
      token: form.token.trim(),
      username: form.username,
      password: form.password,
      locale: locale.value,
    })
    await router.replace({ name: 'overview' })
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
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting" @submit.prevent="submit">
      <NAlert v-if="error" type="error" class="setup__alert">{{ errorMessage(error) }}</NAlert>
      <NFormItem path="locale" :label="t('common.field.locale')">
        <div class="setup__field">
          <NSelect
            :value="locale"
            :options="localeOptions"
            :render-label="renderLabel"
            :loading="switching"
            @update:value="onLocaleChange"
          />
          <NText depth="3" class="setup__hint">{{ t('setup.localeHint') }}</NText>
        </div>
      </NFormItem>
      <NFormItem v-if="!linkToken" path="token" :label="t('common.field.token')">
        <div class="setup__field">
          <NInput
            v-model:value="form.token"
            :input-props="{ autocomplete: 'off', spellcheck: false }"
          />
          <NText depth="3" class="setup__hint">{{ t('setup.tokenHint') }}</NText>
        </div>
      </NFormItem>
      <NFormItem path="username" :label="t('common.field.username')">
        <div class="setup__field">
          <NInput
            v-model:value="form.username"
            :maxlength="USERNAME_MAX"
            :input-props="{ autocomplete: 'username', autocapitalize: 'none', spellcheck: false }"
          />
          <NText depth="3" class="setup__hint">
            {{ t('setup.usernameHint', { min: USERNAME_MIN, max: USERNAME_MAX }) }}
          </NText>
        </div>
      </NFormItem>
      <NFormItem path="password" :label="t('common.field.password')">
        <div class="setup__field">
          <NInput
            v-model:value="form.password"
            type="password"
            show-password-on="click"
            :input-props="{ autocomplete: 'new-password' }"
          />
          <NText depth="3" class="setup__hint">
            {{ t('setup.passwordHint', { min: PASSWORD_MIN }, PASSWORD_MIN) }}
          </NText>
        </div>
      </NFormItem>
      <NFormItem path="passwordConfirm" :label="t('common.field.passwordConfirm')">
        <NInput
          v-model:value="form.passwordConfirm"
          type="password"
          show-password-on="click"
          :input-props="{ autocomplete: 'new-password' }"
        />
      </NFormItem>
      <NButton type="primary" attr-type="submit" block :loading="submitting">
        {{ t('setup.submit') }}
      </NButton>
    </NForm>
  </AuthLayout>
</template>

<style scoped>
.setup__alert {
  margin-block-end: 16px;
}

.setup__field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  inline-size: 100%;
}

.setup__hint {
  font-size: 12px;
}
</style>
