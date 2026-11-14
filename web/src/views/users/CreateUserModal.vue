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
  NSelect,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { createUser, type ManagedUser } from '@/api/users'
import { ROLES, roleLabel, type Role } from '@/auth/permissions'
import {
  USERNAME_MAX,
  confirmPasswordRules,
  newPasswordRules,
  requiredRule,
  usernameRules,
} from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ created: [user: ManagedUser] }>()

const { t } = useI18n()

const formRef = ref<FormInst | null>(null)
const form = reactive({ username: '', password: '', passwordConfirm: '', role: 'operator' as Role })
const submitting = ref(false)
const error = ref<unknown>(null)

watch(show, (open) => {
  if (!open) return
  Object.assign(form, { username: '', password: '', passwordConfirm: '', role: 'operator' })
  error.value = null
})

const rules: FormRules = {
  username: usernameRules(),
  password: newPasswordRules(),
  passwordConfirm: confirmPasswordRules(() => form.password),
  role: [requiredRule('role')],
}

const roleOptions = computed(() =>
  ROLES.map((role) => ({ value: role, label: roleLabel(role) })),
)

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  error.value = null
  submitting.value = true
  try {
    const user = await createUser({
      username: form.username,
      password: form.password,
      role: form.role,
    })
    emit('created', user)
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="t('users.create.title')" class="user-modal">
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting" @submit.prevent="submit">
      <NAlert v-if="error" type="error" class="user-modal__alert">{{ errorMessage(error) }}</NAlert>
      <NFormItem path="username" :label="t('common.field.username')">
        <NInput
          v-model:value="form.username"
          :maxlength="USERNAME_MAX"
          :input-props="{ autocomplete: 'off', autocapitalize: 'none', spellcheck: false }"
        />
      </NFormItem>
      <NFormItem path="role" :label="t('common.field.role')">
        <NSelect v-model:value="form.role" :options="roleOptions" />
      </NFormItem>
      <NFormItem path="password" :label="t('common.field.password')">
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
          {{ t('users.create.submit') }}
        </NButton>
      </div>
    </NForm>
  </NModal>
</template>

<style scoped>
.user-modal {
  inline-size: min(460px, calc(100vw - 32px));
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
