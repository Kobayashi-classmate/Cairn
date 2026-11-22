<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { changePassword } from '@/api/account'
import { isApiError } from '@/api/client'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import { PASSWORD_MIN } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { notifySuccess } from '@/lib/notify'
import { useSessionStore } from '@/stores/session'
import { passwordErrors } from '@/views/users/validation'

// Changing your own password (form sheet). The server signs out every other
// session of the account.
const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const session = useSessionStore()

const form = reactive({ current: '', next: '', confirm: '' })
const errors = ref<{ current?: string; next?: string; confirm?: string }>({})
const submitting = ref(false)
const error = ref<unknown>(null)

watch(open, (value) => {
  if (!value) return
  Object.assign(form, { current: '', next: '', confirm: '' })
  errors.value = {}
  error.value = null
})

function validate(): boolean {
  const checked = passwordErrors(form.next, form.confirm, 'newPassword')
  const next: typeof errors.value = { next: checked.password, confirm: checked.confirm }
  if (!form.current) next.current = validationMessage('currentPassword', 'required')
  for (const key of Object.keys(next) as (keyof typeof next)[]) if (!next[key]) delete next[key]
  errors.value = next
  return Object.keys(next).length === 0
}

async function submit() {
  if (!validate()) return
  submitting.value = true
  error.value = null
  try {
    await changePassword(form.current, form.next)
    notifySuccess(t('settings.account.passwordChanged'))
    open.value = false
  } catch (err) {
    // A wrong current password belongs to its field.
    if (isApiError(err) && err.code === 'auth.wrong_password') errors.value = { current: errorMessage(err) }
    else error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="t('settings.account.password')"
    :description="t('settings.account.passwordHint')"
    :submit-label="t('settings.account.passwordSubmit')"
    :submitting="submitting"
    @submit="submit"
  >
    <!-- Lets password managers associate the new password with this account. -->
    <input type="text" :value="session.user?.username" autocomplete="username" hidden readonly />
    <FormField id="password-current" :label="t('common.field.currentPassword')" :error="errors.current">
      <template #default="field">
        <PasswordInput v-bind="field" v-model="form.current" autocomplete="current-password" :disabled="submitting" />
      </template>
    </FormField>
    <FormField
      id="password-new"
      :label="t('common.field.newPassword')"
      :hint="t('settings.account.passwordRule', { min: PASSWORD_MIN })"
      :error="errors.next"
    >
      <template #default="field">
        <PasswordInput v-bind="field" v-model="form.next" autocomplete="new-password" :disabled="submitting" />
      </template>
    </FormField>
    <FormField id="password-confirm" :label="t('common.field.passwordConfirm')" :error="errors.confirm">
      <template #default="field">
        <PasswordInput v-bind="field" v-model="form.confirm" autocomplete="new-password" :disabled="submitting" />
      </template>
    </FormField>
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
