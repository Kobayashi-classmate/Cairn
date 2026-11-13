<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NModal, NRadio, NRadioGroup, NText } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { updateUser, type ManagedUser } from '@/api/users'
import { ROLES, roleLabel, type Role } from '@/auth/permissions'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ user: ManagedUser | null }>()
const emit = defineEmits<{ updated: [user: ManagedUser] }>()

const { t } = useI18n()

const role = ref<Role>('operator')
const submitting = ref(false)
const error = ref<unknown>(null)

watch(show, (open) => {
  if (!open || !props.user) return
  role.value = props.user.role
  error.value = null
})

const roles = computed(() => ROLES.map((value) => ({ value, label: roleLabel(value) })))

async function submit() {
  if (!props.user) return
  error.value = null
  submitting.value = true
  try {
    emit('updated', await updateUser(props.user.id, { role: role.value }))
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
    :title="t('users.role.title', { username: user?.username ?? '' })"
    class="user-modal"
  >
    <NAlert v-if="error" type="error" class="user-modal__alert">{{ errorMessage(error) }}</NAlert>
    <NRadioGroup v-model:value="role" :disabled="submitting" class="roles">
      <NRadio v-for="option in roles" :key="option.value" :value="option.value" class="roles__item">
        <div>{{ option.label }}</div>
        <NText depth="3" class="roles__hint">{{ t(`users.roleHelp.${option.value}`) }}</NText>
      </NRadio>
    </NRadioGroup>
    <div class="user-modal__actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton
        type="primary"
        :loading="submitting"
        :disabled="role === user?.role"
        @click="submit"
      >
        {{ t('common.action.save') }}
      </NButton>
    </div>
  </NModal>
</template>

<style scoped>
.user-modal {
  inline-size: min(460px, calc(100vw - 32px));
}

.user-modal__alert {
  margin-block-end: 16px;
}

.roles {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-block-end: 20px;
}

.roles__hint {
  font-size: 13px;
}

.user-modal__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
