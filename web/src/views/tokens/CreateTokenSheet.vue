<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isStepUpRequired } from '@/api/client'
import { createToken, type CreatedToken } from '@/api/tokens'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import SelectField from '@/components/common/SelectField.vue'
import TagInput from '@/components/common/TagInput.vue'
import type { SelectOption } from '@/components/common/types'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { permissionLabel, type Permission } from '@/auth/permissions'
import { isIpOrCidr, textLength } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

// Creating an API token (form sheet, re-authentication). The secret is
// handed to the parent, which shows it once.
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ created: [token: CreatedToken] }>()

/** Offered lifetimes in days; 0 means the token never expires. */
const EXPIRY_DAYS = [7, 30, 90, 365, 0] as const
const NAME_MAX = 64
const IPS_MAX = 20

const { t } = useI18n()
const session = useSessionStore()

const form = reactive({ name: '', permissions: [] as Permission[], expires: '30', allowedIps: [] as string[] })
const errors = ref<{ name?: string; permissions?: string; allowedIps?: string }>({})
const submitting = ref(false)
const error = ref<unknown>(null)
const ipInput = ref<InstanceType<typeof TagInput> | null>(null)

watch(open, (value) => {
  if (!value) return
  Object.assign(form, { name: '', permissions: [], expires: '30', allowedIps: [] })
  errors.value = {}
  error.value = null
})

// A token can only carry permissions its owner has; module permissions appear automatically.
const grantable = computed(() => session.permissions)

const expiryOptions = computed<SelectOption[]>(() =>
  EXPIRY_DAYS.map((days) => ({
    value: String(days),
    label: days === 0 ? t('tokens.expiry.never') : t('tokens.expiry.days', { count: days }, days),
  })),
)

const allChecked = computed(() => grantable.value.length > 0 && grantable.value.every((p) => form.permissions.includes(p)))

function toggle(permission: Permission, checked: boolean | 'indeterminate') {
  form.permissions = checked === true
    ? [...new Set([...form.permissions, permission])]
    : form.permissions.filter((p) => p !== permission)
}

function toggleAll(checked: boolean | 'indeterminate') {
  form.permissions = checked === true ? [...grantable.value] : []
}

function validate(): boolean {
  ipInput.value?.commit()
  const next: typeof errors.value = {}
  const name = form.name.trim()
  if (!name) next.name = validationMessage('name', 'required')
  else if (textLength(name) > NAME_MAX) next.name = validationMessage('name', 'max_length', { max: NAME_MAX })
  if (form.permissions.length === 0) next.permissions = validationMessage('permissions', 'required')
  if (form.allowedIps.some((ip) => !isIpOrCidr(ip))) next.allowedIps = validationMessage('allowedIps', 'ip')
  errors.value = next
  return Object.keys(next).length === 0
}

async function submit() {
  if (!validate()) return
  submitting.value = true
  error.value = null
  try {
    const created = await createToken({
      name: form.name.trim(),
      permissions: form.permissions,
      expiresInDays: Number(form.expires),
      allowedIps: form.allowedIps.map((ip) => ip.trim()),
    })
    open.value = false
    emit('created', created)
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
    :title="t('tokens.create.title')"
    :description="t('tokens.create.intro')"
    :submit-label="t('tokens.create.submit')"
    :submitting="submitting"
    @submit="submit"
  >
    <FormField id="token-name" :label="t('common.field.name')" :hint="t('tokens.create.nameHint')" :error="errors.name">
      <template #default="field">
        <Input v-bind="field" v-model="form.name" autocomplete="off" :maxlength="NAME_MAX" :disabled="submitting" dir="auto" />
      </template>
    </FormField>

    <fieldset class="m-0 flex min-w-0 flex-col gap-2 border-0 p-0" :aria-describedby="errors.permissions ? 'token-permissions-error' : undefined">
      <legend class="mb-1.5 p-0 text-detail font-medium">{{ t('common.field.permissions') }}</legend>
      <label class="flex cursor-pointer items-center gap-2.5 border-b pb-2 text-detail font-medium">
        <Checkbox
          :model-value="allChecked ? true : form.permissions.length > 0 ? 'indeterminate' : false"
          :disabled="submitting"
          @update:model-value="toggleAll"
        />
        {{ t('tokens.create.allPermissions') }}
      </label>
      <div class="grid gap-x-4 gap-y-2 sm:grid-cols-2">
        <label v-for="permission in grantable" :key="permission" class="flex cursor-pointer items-start gap-2.5 text-detail">
          <Checkbox
            class="mt-0.5"
            :model-value="form.permissions.includes(permission)"
            :disabled="submitting"
            @update:model-value="(value: boolean | 'indeterminate') => toggle(permission, value)"
          />
          <span class="flex min-w-0 flex-col">
            <span>{{ permissionLabel(permission) }}</span>
            <span class="font-mono text-xs text-muted-foreground" dir="ltr">{{ permission }}</span>
          </span>
        </label>
      </div>
      <p v-if="errors.permissions" id="token-permissions-error" class="m-0 text-xs font-medium text-destructive">
        {{ errors.permissions }}
      </p>
    </fieldset>

    <FormField id="token-expires" :label="t('common.field.expiresInDays')">
      <template #default="field">
        <SelectField v-bind="field" v-model="form.expires" :options="expiryOptions" :disabled="submitting" />
      </template>
    </FormField>

    <FormField
      id="token-ips"
      :label="t('common.field.allowedIps')"
      :hint="t('tokens.create.allowedIpsHint')"
      :error="errors.allowedIps"
    >
      <template #default="field">
        <TagInput
          ref="ipInput"
          v-bind="field"
          v-model="form.allowedIps"
          :valid="isIpOrCidr"
          :max="IPS_MAX"
          mono
          :disabled="submitting"
        />
      </template>
    </FormField>

    <Notice tone="info">{{ t('tokens.help.stepUp') }}</Notice>
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
