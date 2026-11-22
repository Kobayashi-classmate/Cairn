<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isStepUpRequired } from '@/api/client'
import { createDatabase, type DbCreateRequest, type DbEngine } from '@/api/databases'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import TagInput from '@/components/common/TagInput.vue'
import { Input } from '@/components/ui/input'
import { engineLabel } from '@/databases/labels'
import { isDbHost, isDbName, isDbPassword, isDbUser } from '@/databases/validate'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { useDatabasesStore } from '@/stores/databases'

// "Create database" (form sheet): the database and, by default, an owner of
// the same name with a generated password.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ engine: DbEngine }>()

const { t } = useI18n()
const store = useDatabasesStore()

const form = reactive({ name: '', withUser: true, user: '', password: '', hosts: [] as string[] })
type Field = 'name' | 'user' | 'password' | 'hosts'
const errors = ref<Partial<Record<Field, string>>>({})
const submitting = ref(false)
const error = ref<unknown>(null)
const hostsInput = ref<InstanceType<typeof TagInput> | null>(null)

const kind = computed(() => props.engine.kind)

watch(open, (value) => {
  if (!value) return
  Object.assign(form, { name: '', withUser: true, user: '', password: '', hosts: [] })
  errors.value = {}
  error.value = null
})

const owner = computed(() => form.user.trim() || form.name.trim())

function validate(): boolean {
  hostsInput.value?.commit()
  const next: Partial<Record<Field, string>> = {}
  const name = form.name.trim()
  if (!name) next.name = validationMessage('name', 'required')
  else if (!isDbName(kind.value, name)) next.name = validationMessage('name', 'db_name')
  if (form.withUser) {
    if (form.user.trim() && !isDbUser(kind.value, form.user.trim())) next.user = validationMessage('dbUser', 'db_user')
    if (form.password && !isDbPassword(form.password)) next.password = validationMessage('password', 'db_password')
    if (form.hosts.some((host) => !isDbHost(host))) next.hosts = validationMessage('hosts', 'db_host')
  }
  errors.value = next
  return Object.keys(next).length === 0
}

const steps = computed(() => {
  const name = form.name.trim() || t('databases.db.namePlaceholder')
  const list = [t('databases.db.steps.create', { name, engine: engineLabel(props.engine) })]
  if (form.withUser) {
    list.push(
      form.password ? t('databases.db.steps.user', { user: owner.value || name }) : t('databases.db.steps.userGenerated', { user: owner.value || name }),
    )
    list.push(
      form.hosts.length > 0
        ? t('databases.db.steps.hosts', { hosts: form.hosts.join(', ') })
        : t('databases.db.steps.local'),
    )
  }
  return list
})

async function submit() {
  if (!validate()) return
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
    await store.runJob(() => createDatabase(props.engine.id, request), { rethrow: true })
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
    :title="t('databases.db.createTitle')"
    :description="engineLabel(engine)"
    :submit-label="t('databases.db.create')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <FormField
      id="db-name"
      :label="t('common.field.name')"
      :hint="kind === 'postgresql' ? t('databases.hints.pgName') : t('databases.hints.mysqlName')"
      :error="errors.name"
    >
      <template #default="field">
        <Input v-bind="field" v-model="form.name" autocomplete="off" spellcheck="false" autocapitalize="none" class="font-mono" />
      </template>
    </FormField>

    <SwitchField v-model="form.withUser" :label="t('databases.db.withUser')" :hint="t('databases.db.withUserHint')" />

    <template v-if="form.withUser">
      <FormField
        id="db-user"
        :label="t('common.field.dbUser')"
        :hint="kind === 'postgresql' ? t('databases.hints.pgName') : t('databases.hints.mysqlUser')"
        :error="errors.user"
      >
        <template #default="field">
          <Input
            v-bind="field"
            v-model="form.user"
            autocomplete="off"
            spellcheck="false"
            autocapitalize="none"
            class="font-mono"
            :placeholder="form.name || t('databases.db.userPlaceholder')"
          />
        </template>
      </FormField>
      <FormField id="db-password" :label="t('common.field.password')" :hint="t('databases.hints.password')" :error="errors.password">
        <template #default="field">
          <PasswordInput v-bind="field" v-model="form.password" autocomplete="new-password" :placeholder="t('databases.hints.generate')" />
        </template>
      </FormField>
      <FormField
        id="db-hosts"
        :label="t('common.field.hosts')"
        :hint="kind === 'postgresql' ? t('databases.hints.pgHosts') : t('databases.hints.hosts')"
        :error="errors.hosts"
      >
        <template #default="field">
          <TagInput ref="hostsInput" v-bind="field" v-model="form.hosts" :valid="isDbHost" mono placeholder="localhost" />
        </template>
      </FormField>
    </template>

    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
