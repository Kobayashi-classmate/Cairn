<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isApiError, isStepUpRequired } from '@/api/client'
import { createUser } from '@/api/users'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import OptionGroup from '@/components/common/OptionGroup.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import type { OptionItem } from '@/components/common/types'
import { Input } from '@/components/ui/input'
import { ROLES, roleLabel, type Role } from '@/auth/permissions'
import { PASSWORD_MIN, USERNAME_MAX } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'
import { notifySuccess } from '@/lib/notify'
import { useUsersStore } from '@/stores/users'
import { passwordErrors, usernameError } from './validation'

// Adding a panel account (form sheet, re-authentication): name, role and a
// first password the person changes later in their own settings.
const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const store = useUsersStore()

const form = reactive({ username: '', role: 'operator' as string, password: '', confirm: '' })
const errors = ref<{ username?: string; password?: string; confirm?: string }>({})
const submitting = ref(false)
const error = ref<unknown>(null)

watch(open, (value) => {
  if (!value) return
  Object.assign(form, { username: '', role: 'operator', password: '', confirm: '' })
  errors.value = {}
  error.value = null
})

const roleOptions = computed<OptionItem[]>(() =>
  ROLES.map((role) => ({ value: role, label: roleLabel(role), hint: t(`users.roleHelp.${role}`) })),
)

function validate(): boolean {
  const next: typeof errors.value = passwordErrors(form.password, form.confirm, 'password')
  const name = usernameError(form.username)
  if (name) next.username = name
  errors.value = next
  return Object.keys(next).length === 0
}

async function submit() {
  if (!validate()) return
  submitting.value = true
  error.value = null
  try {
    const user = await createUser({ username: form.username, password: form.password, role: form.role as Role })
    store.put(user)
    notifySuccess(t('users.create.done', { username: user.username }))
    open.value = false
  } catch (err) {
    if (isStepUpRequired(err)) return
    // A taken name belongs to its field.
    if (isApiError(err) && err.code === 'users.username_taken') errors.value = { username: errorMessage(err) }
    else error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="t('users.create.title')"
    :description="t('users.create.intro')"
    :submit-label="t('users.create.submit')"
    :submitting="submitting"
    @submit="submit"
  >
    <FormField id="user-name" :label="t('common.field.username')" :hint="t('users.create.usernameHint')" :error="errors.username">
      <template #default="field">
        <Input
          v-bind="field"
          v-model="form.username"
          :maxlength="USERNAME_MAX"
          autocomplete="off"
          autocapitalize="none"
          spellcheck="false"
          dir="ltr"
          :disabled="submitting"
        />
      </template>
    </FormField>

    <div class="flex flex-col gap-1.5">
      <span id="user-role-label" class="text-detail font-medium">{{ t('common.field.role') }}</span>
      <OptionGroup v-model="form.role" :options="roleOptions" aria-labelledby="user-role-label" :disabled="submitting" />
    </div>

    <FormField id="user-password" :label="t('common.field.password')" :hint="t('users.password.rule', { min: PASSWORD_MIN })" :error="errors.password">
      <template #default="field">
        <PasswordInput v-bind="field" v-model="form.password" autocomplete="new-password" :disabled="submitting" />
      </template>
    </FormField>
    <FormField id="user-password-confirm" :label="t('common.field.passwordConfirm')" :error="errors.confirm">
      <template #default="field">
        <PasswordInput v-bind="field" v-model="form.confirm" autocomplete="new-password" :disabled="submitting" />
      </template>
    </FormField>

    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
