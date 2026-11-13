<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NForm,
  NFormItem,
  NInput,
  NSelect,
  NText,
  useMessage,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { changePassword } from '@/api/account'
import { roleLabel } from '@/auth/permissions'
import { confirmPasswordRules, newPasswordRules, requiredRule } from '@/auth/rules'
import { useLocaleSwitch } from '@/composables/useLocaleSwitch'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const message = useMessage()
const session = useSessionStore()
const { locale, options: localeOptions, renderLabel, switching, select } = useLocaleSwitch()

const formRef = ref<FormInst | null>(null)
const form = reactive({ currentPassword: '', newPassword: '', passwordConfirm: '' })
const submitting = ref(false)
const error = ref<unknown>(null)

const rules: FormRules = {
  currentPassword: [requiredRule('currentPassword')],
  newPassword: newPasswordRules('newPassword'),
  passwordConfirm: confirmPasswordRules(() => form.newPassword),
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
    await changePassword(form.currentPassword, form.newPassword)
    form.currentPassword = ''
    form.newPassword = ''
    form.passwordConfirm = ''
    formRef.value?.restoreValidation()
    message.success(t('settings.account.passwordChanged'))
  } catch (err) {
    error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NCard v-if="session.user" :title="t('settings.account.profile')" size="small">
    <dl class="profile">
      <div>
        <dt><NText depth="3">{{ t('common.field.username') }}</NText></dt>
        <dd>{{ session.user.username }}</dd>
      </div>
      <div>
        <dt><NText depth="3">{{ t('common.field.role') }}</NText></dt>
        <dd>{{ roleLabel(session.user.role) }}</dd>
      </div>
    </dl>
  </NCard>

  <NCard :title="t('settings.account.language')" size="small">
    <div class="field">
      <NSelect
        :value="locale"
        :options="localeOptions"
        :render-label="renderLabel"
        :loading="switching"
        class="field__control"
        :aria-label="t('common.field.locale')"
        @update:value="onLocaleChange"
      />
      <NText depth="3" class="field__hint">{{ t('settings.account.languageHint') }}</NText>
    </div>
  </NCard>

  <NCard :title="t('settings.account.password')" size="small">
    <NText depth="3" tag="p" class="intro">{{ t('settings.account.passwordHint') }}</NText>
    <NForm
      ref="formRef"
      :model="form"
      :rules="rules"
      :disabled="submitting"
      class="password-form"
      @submit.prevent="submit"
    >
      <NAlert v-if="error" type="error" class="alert">{{ errorMessage(error) }}</NAlert>
      <!-- Lets password managers associate the new password with this account. -->
      <input
        type="text"
        :value="session.user?.username"
        autocomplete="username"
        hidden
        readonly
      />
      <NFormItem path="currentPassword" :label="t('common.field.currentPassword')">
        <NInput
          v-model:value="form.currentPassword"
          type="password"
          show-password-on="click"
          :input-props="{ autocomplete: 'current-password' }"
        />
      </NFormItem>
      <NFormItem path="newPassword" :label="t('common.field.newPassword')">
        <NInput
          v-model:value="form.newPassword"
          type="password"
          show-password-on="click"
          :input-props="{ autocomplete: 'new-password' }"
        />
      </NFormItem>
      <NFormItem path="passwordConfirm" :label="t('common.field.passwordConfirm')">
        <NInput
          v-model:value="form.passwordConfirm"
          type="password"
          show-password-on="click"
          :input-props="{ autocomplete: 'new-password' }"
        />
      </NFormItem>
      <NButton type="primary" attr-type="submit" :loading="submitting">
        {{ t('settings.account.passwordSubmit') }}
      </NButton>
    </NForm>
  </NCard>
</template>

<style scoped>
.profile {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 12px 24px;
  margin: 0;
}

.profile dd {
  margin: 2px 0 0;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.field__control {
  max-inline-size: 320px;
}

.field__hint {
  font-size: 13px;
}

.intro {
  margin-block: 0 16px;
}

.alert {
  margin-block-end: 16px;
}

.password-form {
  max-inline-size: 420px;
}
</style>
