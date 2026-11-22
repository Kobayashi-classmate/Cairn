<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isStepUpRequired } from '@/api/client'
import { updateUser, type ManagedUser } from '@/api/users'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import OptionGroup from '@/components/common/OptionGroup.vue'
import type { OptionItem } from '@/components/common/types'
import { ROLES, roleLabel, type Role } from '@/auth/permissions'
import { errorMessage } from '@/i18n/errors'
import { notifySuccess } from '@/lib/notify'
import { useUsersStore } from '@/stores/users'

// Changing an account's role (form sheet, re-authentication). The account
// keeps its sessions; what it may do changes with its next request.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ user: ManagedUser | null }>()

const { t } = useI18n()
const store = useUsersStore()

const role = ref<string>('operator')
const submitting = ref(false)
const error = ref<unknown>(null)

watch(open, (value) => {
  if (!value || !props.user) return
  role.value = props.user.role
  error.value = null
})

const options = computed<OptionItem[]>(() =>
  ROLES.map((value) => ({ value, label: roleLabel(value), hint: t(`users.roleHelp.${value}`) })),
)

async function submit() {
  const user = props.user
  if (!user) return
  if (role.value === user.role) {
    open.value = false
    return
  }
  submitting.value = true
  error.value = null
  try {
    const updated = await updateUser(user.id, { role: role.value as Role })
    store.put(updated)
    notifySuccess(t('users.role.done', { username: updated.username }))
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
    :title="t('users.role.title', { username: user?.username ?? '' })"
    :submit-label="t('users.role.submit')"
    :submitting="submitting"
    :submit-disabled="role === user?.role"
    @submit="submit"
  >
    <OptionGroup v-model="role" :options="options" :label="t('common.field.role')" :disabled="submitting" />
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
