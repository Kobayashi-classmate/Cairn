<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isStepUpRequired } from '@/api/client'
import { setUserPassword, type ManagedUser } from '@/api/users'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import { PASSWORD_MIN } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'
import { notifySuccess } from '@/lib/notify'
import { passwordErrors } from './validation'

// Setting a new password for someone else's account (form sheet,
// re-authentication). The account is signed out everywhere.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ user: ManagedUser | null }>()

const { t } = useI18n()

const form = reactive({ password: '', confirm: '' })
const errors = ref<{ password?: string; confirm?: string }>({})
const submitting = ref(false)
const error = ref<unknown>(null)

watch(open, (value) => {
  if (!value) return
  Object.assign(form, { password: '', confirm: '' })
  errors.value = {}
  error.value = null
})

async function submit() {
  const user = props.user
  if (!user) return
  errors.value = passwordErrors(form.password, form.confirm, 'newPassword')
  if (Object.keys(errors.value).length > 0) return
  submitting.value = true
  error.value = null
  try {
    await setUserPassword(user.id, form.password)
    notifySuccess(t('users.password.done', { username: user.username }))
    open.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="t('users.password.title', { username: user?.username ?? '' })"
    :description="t('users.password.hint', { username: user?.username ?? '' })"
    :submit-label="t('users.password.submit')"
    :submitting="submitting"
    @submit="submit"
  >
    <!-- Lets password managers file the new password under this account, not yours. -->
    <input type="text" :value="user?.username" autocomplete="username" hidden readonly />
    <FormField
      id="user-new-password"
      :label="t('common.field.newPassword')"
      :hint="t('users.password.rule', { min: PASSWORD_MIN })"
      :error="errors.password"
    >
      <template #default="field">
        <PasswordInput v-bind="field" v-model="form.password" autocomplete="new-password" :disabled="submitting" />
      </template>
    </FormField>
    <FormField id="user-new-password-confirm" :label="t('common.field.passwordConfirm')" :error="errors.confirm">
      <template #default="field">
        <PasswordInput v-bind="field" v-model="form.confirm" autocomplete="new-password" :disabled="submitting" />
      </template>
    </FormField>
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
