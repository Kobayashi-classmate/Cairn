<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NForm, NFormItem, NInput, type FormInst, type FormRules } from 'naive-ui'
import AuthLayout from '@/layouts/AuthLayout.vue'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { safeRedirect } from '@/router/redirect'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()

const formRef = ref<FormInst | null>(null)
const form = reactive({ username: '', password: '' })
const submitting = ref(false)
const error = ref<unknown>(null)
// Why the user was sent here (for example an expired session).
const notice = ref(session.takeNotice())

// Messages are rendered on display, so they follow language switches.
const rules: FormRules = {
  username: {
    required: true,
    trigger: 'blur',
    renderMessage: () => validationMessage('username', 'required'),
  },
  password: {
    required: true,
    trigger: 'blur',
    renderMessage: () => validationMessage('password', 'required'),
  },
}

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  error.value = null
  notice.value = null
  submitting.value = true
  try {
    await session.signIn({ username: form.username, password: form.password })
    await router.replace(safeRedirect(route.query.redirect) ?? { name: 'overview' })
  } catch (err) {
    error.value = err
    form.password = ''
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <AuthLayout :title="t('auth.login.title')" :subtitle="t('auth.login.subtitle')">
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting" @submit.prevent="submit">
      <NAlert v-if="error" type="error" class="login__alert">{{ errorMessage(error) }}</NAlert>
      <NAlert v-else-if="notice" type="warning" class="login__alert">
        {{ errorMessage(notice) }}
      </NAlert>
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
  </AuthLayout>
</template>

<style scoped>
.login__alert {
  margin-block-end: 16px;
}
</style>
