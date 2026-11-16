<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NDynamicTags,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NSelect,
  NText,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { createDbUser, updateDbUser, type DbEngine, type DbGrant, type DbUser } from '@/api/databases'
import type { Job } from '@/api/jobs'
import { isDbHost, isDbPassword, isDbUser } from '@/databases/validate'
import { checkRule, listRule, requiredRule } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{
  engine: DbEngine
  /** The user to change, or null to add one. */
  user: DbUser | null
}>()
const emit = defineEmits<{ started: [job: Job] }>()

const { t } = useI18n()

const formRef = ref<FormInst | null>(null)
const form = reactive({ name: '', password: '', hosts: [] as string[], grants: [] as DbGrant[] })
const submitting = ref(false)
const error = ref<unknown>(null)

const editing = computed(() => props.user !== null)
const kind = computed(() => props.engine.kind)

watch(show, (open) => {
  if (!open) return
  const user = props.user
  Object.assign(form, {
    name: user?.name ?? '',
    password: '',
    hosts: [...(user?.hosts ?? [])],
    grants: (user?.grants ?? []).map((g) => ({ ...g })),
  })
  error.value = null
})

const databaseOptions = computed(() => props.engine.databases.map((db) => ({ value: db.name, label: db.name })))
const levelOptions = computed(() => [
  { value: 'all', label: t('databases.grant.all') },
  { value: 'readonly', label: t('databases.grant.readonly') },
])

function addGrant() {
  const used = new Set(form.grants.map((g) => g.database))
  const database = props.engine.databases.find((db) => !used.has(db.name))?.name ?? ''
  form.grants.push({ database, level: 'all' })
}

function removeGrant(index: number) {
  form.grants.splice(index, 1)
}

const rules = computed<FormRules>(() => ({
  name: editing.value ? [] : [requiredRule('name'), checkRule('name', 'db_user', (v) => isDbUser(kind.value, v))],
  password: [checkRule('password', 'db_password', isDbPassword)],
  hosts: [listRule('hosts', 'db_host', isDbHost)],
}))

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  const grants = form.grants.filter((g) => g.database)
  const body = { hosts: form.hosts, grants, ...(form.password ? { password: form.password } : {}) }
  submitting.value = true
  error.value = null
  try {
    const job = props.user
      ? await updateDbUser(props.engine.id, props.user.name, body)
      : await createDbUser(props.engine.id, { ...body, name: form.name.trim() })
    emit('started', job)
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
    :title="editing ? t('databases.user.editTitle', { name: user?.name ?? '' }) : t('databases.user.createTitle')"
    class="user-dialog"
  >
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting">
      <NFormItem v-if="!editing" path="name" :label="t('common.field.name')">
        <div class="field">
          <NInput v-model:value="form.name" :input-props="{ autocomplete: 'off', spellcheck: false }" />
          <NText depth="3" class="hint">{{ kind === 'postgresql' ? t('databases.hints.pgName') : t('databases.hints.mysqlUser') }}</NText>
        </div>
      </NFormItem>
      <NFormItem path="password" :label="t('common.field.password')">
        <div class="field">
          <NInput
            v-model:value="form.password"
            type="password"
            show-password-on="click"
            :placeholder="editing ? t('databases.hints.keep') : t('databases.hints.generate')"
            :input-props="{ autocomplete: 'new-password' }"
          />
          <NText depth="3" class="hint">{{ t('databases.hints.password') }}</NText>
        </div>
      </NFormItem>
      <NFormItem path="hosts" :label="t('common.field.hosts')">
        <div class="field">
          <NDynamicTags v-model:value="form.hosts" />
          <NText depth="3" class="hint">{{ kind === 'postgresql' ? t('databases.hints.pgHosts') : t('databases.hints.hosts') }}</NText>
        </div>
      </NFormItem>
      <NFormItem :label="t('common.field.grants')" :show-feedback="false">
        <div class="field">
          <div v-for="(grant, index) in form.grants" :key="index" class="grant">
            <NSelect v-model:value="grant.database" :options="databaseOptions" class="grant__db" />
            <NSelect v-model:value="grant.level" :options="levelOptions" class="grant__level" />
            <NButton quaternary size="small" @click="removeGrant(index)">{{ t('common.action.delete') }}</NButton>
          </div>
          <NButton size="small" class="grant__add" :disabled="engine.databases.length === 0" @click="addGrant">
            {{ t('databases.grant.add') }}
          </NButton>
        </div>
      </NFormItem>
    </NForm>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" @click="submit">{{ t('common.action.save') }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.user-dialog {
  inline-size: min(560px, calc(100vw - 32px));
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

.grant {
  display: flex;
  gap: 6px;
}

.grant__db {
  flex: 2 1 0;
}

.grant__level {
  flex: 1 1 0;
}

.grant__add {
  align-self: flex-start;
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
