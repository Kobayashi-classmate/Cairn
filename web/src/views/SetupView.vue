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
  type FormRules,
} from 'naive-ui'
import { isApiError } from '@/api/client'
import {
  PASSWORD_MIN,
  USERNAME_MAX,
  USERNAME_MIN,
  confirmPasswordRules,
  newPasswordRules,
  requiredRule,
  usernameRules,
} from '@/forms/rules'
import AuthLayout from '@/layouts/AuthLayout.vue'
import { homeLocation } from '@/layouts/navigation'
import { useLocaleSwitch } from '@/composables/useLocaleSwitch'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

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

const rules: FormRules = {
  token: [requiredRule('token')],
  username: usernameRules(),
  password: newPasswordRules(),
  passwordConfirm: confirmPasswordRules(() => form.password),
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
    await router.replace(homeLocation(session.can))
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
