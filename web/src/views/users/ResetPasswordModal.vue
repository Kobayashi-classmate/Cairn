<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
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
import { isStepUpRequired } from '@/api/client'
import { setUserPassword, type ManagedUser } from '@/api/users'
import { confirmPasswordRules, newPasswordRules } from '@/auth/rules'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ user: ManagedUser | null }>()
const emit = defineEmits<{ done: [user: ManagedUser] }>()

const { t } = useI18n()

const formRef = ref<FormInst | null>(null)
const form = reactive({ password: '', passwordConfirm: '' })
const submitting = ref(false)
const error = ref<unknown>(null)

watch(show, (open) => {
  if (!open) return
  form.password = ''
  form.passwordConfirm = ''
  error.value = null
})

const rules: FormRules = {
  password: newPasswordRules('newPassword'),
  passwordConfirm: confirmPasswordRules(() => form.password),
}

async function submit() {
  if (!props.user) return
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  error.value = null
  submitting.value = true
  try {
    await setUserPassword(props.user.id, form.password)
    emit('done', props.user)
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    :title="t('users.password.title', { username: user?.username ?? '' })"
    class="user-modal"
  >
    <NText depth="3" tag="p" class="user-modal__intro">
      {{ t('users.password.hint', { username: user?.username ?? '' }) }}
    </NText>
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting" @submit.prevent="submit">
      <NAlert v-if="error" type="error" class="user-modal__alert">{{ errorMessage(error) }}</NAlert>
      <NFormItem path="password" :label="t('common.field.newPassword')">
        <NInput
          v-model:value="form.password"
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
      <div class="user-modal__actions">
        <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
        <NButton type="primary" attr-type="submit" :loading="submitting">
          {{ t('users.password.submit') }}
        </NButton>
      </div>
    </NForm>
  </NModal>
</template>

<style scoped>
.user-modal {
  inline-size: min(460px, calc(100vw - 32px));
}

.user-modal__intro {
  margin-block: 0 16px;
}

.user-modal__alert {
  margin-block-end: 16px;
}

.user-modal__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
