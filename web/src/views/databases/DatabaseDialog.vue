<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NDynamicTags, NForm, NFormItem, NInput, NModal, NSwitch, NText, type FormInst, type FormRules } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { createDatabase, type DbCreateRequest, type DbEngine } from '@/api/databases'
import type { Job } from '@/api/jobs'
import { isDbHost, isDbName, isDbPassword, isDbUser } from '@/databases/validate'
import { checkRule, listRule, requiredRule } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ engine: DbEngine }>()
const emit = defineEmits<{ started: [job: Job] }>()

const { t } = useI18n()

const formRef = ref<FormInst | null>(null)
const form = reactive({ name: '', withUser: true, user: '', password: '', hosts: [] as string[] })
const submitting = ref(false)
const error = ref<unknown>(null)

watch(show, (open) => {
  if (!open) return
  Object.assign(form, { name: '', withUser: true, user: '', password: '', hosts: [] })
  error.value = null
})

const kind = computed(() => props.engine.kind)

const rules = computed<FormRules>(() => ({
  name: [requiredRule('name'), checkRule('name', 'db_name', (v) => isDbName(kind.value, v))],
  user: form.withUser ? [checkRule('user', 'db_user', (v) => isDbUser(kind.value, v))] : [],
  password: form.withUser ? [checkRule('password', 'db_password', isDbPassword)] : [],
  hosts: form.withUser ? [listRule('hosts', 'db_host', isDbHost)] : [],
}))

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  const request: DbCreateRequest = { name: form.name.trim() }
  if (!form.withUser) {
    request.noUser = true
  } else {
    if (form.user.trim()) request.user = form.user.trim()
    if (form.password) request.password = form.password
    if (form.hosts.length) request.hosts = form.hosts
  }
  submitting.value = true
  error.value = null
  try {
    emit('started', await createDatabase(props.engine.id, request))
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="t('databases.db.createTitle')" class="db-dialog">
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting">
      <NFormItem path="name" :label="t('common.field.name')">
        <div class="field">
          <NInput v-model:value="form.name" :input-props="{ autocomplete: 'off', spellcheck: false }" />
          <NText depth="3" class="hint">{{ kind === 'postgresql' ? t('databases.hints.pgName') : t('databases.hints.mysqlName') }}</NText>
        </div>
      </NFormItem>
      <label class="switch">
        <NSwitch v-model:value="form.withUser" />
        <span>{{ t('databases.db.withUser') }}</span>
      </label>
      <template v-if="form.withUser">
        <NFormItem path="user" :label="t('common.field.dbUser')">
          <NInput
            v-model:value="form.user"
            :placeholder="form.name || t('databases.db.userPlaceholder')"
            :input-props="{ autocomplete: 'off', spellcheck: false }"
          />
        </NFormItem>
        <NFormItem path="password" :label="t('common.field.password')">
          <div class="field">
            <NInput
              v-model:value="form.password"
              type="password"
              show-password-on="click"
              :placeholder="t('databases.hints.generate')"
              :input-props="{ autocomplete: 'new-password' }"
            />
            <NText depth="3" class="hint">{{ t('databases.hints.password') }}</NText>
          </div>
        </NFormItem>
        <NFormItem path="hosts" :label="t('common.field.hosts')">
          <div class="field">
            <NDynamicTags v-model:value="form.hosts" />
            <NText depth="3" class="hint">
              {{ kind === 'postgresql' ? t('databases.hints.pgHosts') : t('databases.hints.hosts') }}
            </NText>
          </div>
        </NFormItem>
      </template>
    </NForm>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" @click="submit">{{ t('databases.db.create') }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.db-dialog {
  inline-size: min(520px, calc(100vw - 32px));
}

.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  inline-size: 100%;
}

.hint {
  font-size: 12px;
}

.switch {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-block-end: 16px;
  cursor: pointer;
}

.block {
  margin-block: 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
