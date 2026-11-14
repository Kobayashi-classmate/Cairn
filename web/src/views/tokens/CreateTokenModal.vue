<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCheckbox,
  NCheckboxGroup,
  NDynamicTags,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NModal,
  NSelect,
  NText,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { CopyOutline } from '@vicons/ionicons5'
import { isStepUpRequired } from '@/api/client'
import { createToken, type ApiToken } from '@/api/tokens'
import { PERMISSIONS, permissionLabel, type Permission } from '@/auth/permissions'
import { useCopy } from '@/composables/useCopy'
import { isIpOrCidr, listRule, requiredListRule, requiredRule } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

const show = defineModel<boolean>('show', { required: true })
const emit = defineEmits<{ created: [token: ApiToken] }>()

const { t } = useI18n()
const copy = useCopy()
const session = useSessionStore()

/** Offered lifetimes in days; 0 means the token never expires. */
const EXPIRY_DAYS = [7, 30, 90, 365, 0] as const

const formRef = ref<FormInst | null>(null)
const form = reactive({
  name: '',
  permissions: [] as Permission[],
  expiresInDays: 30 as number,
  allowedIps: [] as string[],
})
const submitting = ref(false)
const error = ref<unknown>(null)
/** The new secret token, shown once after creation. */
const secret = ref<string | null>(null)

watch(show, (open) => {
  if (!open) {
    // Do not keep the secret around once the dialog is closed.
    secret.value = null
    return
  }
  Object.assign(form, { name: '', permissions: [], expiresInDays: 30, allowedIps: [] })
  error.value = null
})

// A token can only carry permissions its owner has.
const grantable = computed(() => PERMISSIONS.filter((p) => session.can(p)))

const expiryOptions = computed(() =>
  EXPIRY_DAYS.map((days) => ({
    value: days,
    label: days === 0 ? t('tokens.expiry.never') : t('tokens.expiry.days', { count: days }, days),
  })),
)

const rules: FormRules = {
  name: [requiredRule('name')],
  permissions: [requiredListRule('permissions')],
  allowedIps: [listRule('allowedIps', 'ip', isIpOrCidr)],
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
    const created = await createToken({
      name: form.name.trim(),
      permissions: form.permissions,
      expiresInDays: form.expiresInDays,
      allowedIps: form.allowedIps.map((ip) => ip.trim()),
    })
    secret.value = created.token
    emit('created', created.info)
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
    :title="t('tokens.create.title')"
    :mask-closable="secret === null"
    class="token-modal"
  >
    <template v-if="secret">
      <NAlert type="warning" :title="t('tokens.created.title')" class="block">
        {{ t('tokens.created.warning') }}
      </NAlert>
      <div class="secret">
        <code class="secret__value" dir="ltr">{{ secret }}</code>
        <NButton @click="copy(secret)">
          <template #icon>
            <NIcon><CopyOutline /></NIcon>
          </template>
          {{ t('common.action.copy') }}
        </NButton>
      </div>
      <div class="actions">
        <NButton type="primary" @click="show = false">{{ t('tokens.created.done') }}</NButton>
      </div>
    </template>

    <NForm
      v-else
      ref="formRef"
      :model="form"
      :rules="rules"
      :disabled="submitting"
      @submit.prevent="submit"
    >
      <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
      <NFormItem path="name" :label="t('common.field.name')">
        <NInput v-model:value="form.name" :input-props="{ autocomplete: 'off' }" />
      </NFormItem>
      <NFormItem path="permissions" :label="t('common.field.permissions')">
        <NCheckboxGroup v-model:value="form.permissions">
          <div class="permissions">
            <NCheckbox v-for="permission in grantable" :key="permission" :value="permission">
              {{ permissionLabel(permission) }}
            </NCheckbox>
          </div>
        </NCheckboxGroup>
      </NFormItem>
      <NFormItem path="expiresInDays" :label="t('common.field.expiresInDays')">
        <NSelect v-model:value="form.expiresInDays" :options="expiryOptions" />
      </NFormItem>
      <NFormItem path="allowedIps" :label="t('common.field.allowedIps')">
        <div class="field">
          <NDynamicTags v-model:value="form.allowedIps" />
          <NText depth="3" class="hint">{{ t('tokens.create.allowedIpsHint') }}</NText>
        </div>
      </NFormItem>
      <div class="actions">
        <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
        <NButton type="primary" attr-type="submit" :loading="submitting">
          {{ t('tokens.create.submit') }}
        </NButton>
      </div>
    </NForm>
  </NModal>
</template>

<style scoped>
.token-modal {
  inline-size: min(520px, calc(100vw - 32px));
}

.block {
  margin-block-end: 16px;
}

.permissions {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.hint {
  font-size: 13px;
}

.secret {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-block-end: 20px;
}

.secret__value {
  flex: 1 1 260px;
  padding: 8px 10px;
  border-radius: 4px;
  background: rgb(128 128 128 / 12%);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 14px;
  overflow-wrap: anywhere;
  user-select: all;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
