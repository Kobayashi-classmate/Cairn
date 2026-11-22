<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, X } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import { createDbUser, updateDbUser, type DbEngine, type DbGrant, type DbUser, type GrantLevel } from '@/api/databases'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import SelectField from '@/components/common/SelectField.vue'
import TagInput from '@/components/common/TagInput.vue'
import type { SelectOption } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { engineLabel } from '@/databases/labels'
import { isDbHost, isDbPassword, isDbUser } from '@/databases/validate'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { useDatabasesStore } from '@/stores/databases'

// "Add user" and "Edit user" (form sheet): password, where the user may
// connect from and which databases it can use. Changing a user asks for
// the password (step-up).
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{
  engine: DbEngine
  /** The user to change, or null to add one. */
  user: DbUser | null
}>()

const { t } = useI18n()
const store = useDatabasesStore()

const form = reactive({ name: '', password: '', hosts: [] as string[], grants: [] as DbGrant[] })
type Field = 'name' | 'password' | 'hosts' | 'grants'
const errors = ref<Partial<Record<Field, string>>>({})
const submitting = ref(false)
const error = ref<unknown>(null)
const hostsInput = ref<InstanceType<typeof TagInput> | null>(null)

const editing = computed(() => props.user !== null)
const kind = computed(() => props.engine.kind)

watch(open, (value) => {
  if (!value) return
  const user = props.user
  Object.assign(form, {
    name: user?.name ?? '',
    password: '',
    hosts: [...(user?.hosts ?? [])],
    grants: (user?.grants ?? []).map((g) => ({ ...g })),
  })
  errors.value = {}
  error.value = null
})

const databaseOptions = computed<SelectOption[]>(() =>
  props.engine.databases.filter((db) => !db.missing).map((db) => ({ value: db.name, label: db.name })),
)
const levelOptions = computed<SelectOption[]>(() => [
  { value: 'all', label: t('databases.grant.all') },
  { value: 'readonly', label: t('databases.grant.readonly') },
])

function addGrant() {
  const used = new Set(form.grants.map((g) => g.database))
  const database = databaseOptions.value.find((option) => !used.has(option.value))?.value ?? ''
  form.grants.push({ database, level: 'all' })
}

function removeGrant(index: number) {
  form.grants.splice(index, 1)
}

function setLevel(grant: DbGrant, value: string) {
  grant.level = value as GrantLevel
}

function validate(): boolean {
  hostsInput.value?.commit()
  const next: Partial<Record<Field, string>> = {}
  if (!editing.value) {
    const name = form.name.trim()
    if (!name) next.name = validationMessage('name', 'required')
    else if (!isDbUser(kind.value, name)) next.name = validationMessage('name', 'db_user')
  }
  if (form.password && !isDbPassword(form.password)) next.password = validationMessage('password', 'db_password')
  if (form.hosts.some((host) => !isDbHost(host))) next.hosts = validationMessage('hosts', 'db_host')
  const databases = form.grants.map((g) => g.database).filter(Boolean)
  if (new Set(databases).size !== databases.length) next.grants = validationMessage('grants', 'duplicate')
  errors.value = next
  return Object.keys(next).length === 0
}

async function submit() {
  if (!validate()) return
  const grants = form.grants.filter((g) => g.database)
  const body = { hosts: form.hosts, grants, ...(form.password ? { password: form.password } : {}) }
  submitting.value = true
  error.value = null
  try {
    const user = props.user
    await store.runJob(
      () => (user ? updateDbUser(props.engine.id, user.name, body) : createDbUser(props.engine.id, { ...body, name: form.name.trim() })),
      { rethrow: true },
    )
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
    :title="editing ? t('databases.user.editTitle', { name: user?.name ?? '' }) : t('databases.user.createTitle')"
    :description="engineLabel(engine)"
    :submit-label="editing ? t('common.action.save') : t('databases.user.create')"
    :submitting="submitting"
    @submit="submit"
  >
    <FormField
      v-if="!editing"
      id="user-name"
      :label="t('common.field.name')"
      :hint="kind === 'postgresql' ? t('databases.hints.pgName') : t('databases.hints.mysqlUser')"
      :error="errors.name"
    >
      <template #default="field">
        <Input v-bind="field" v-model="form.name" autocomplete="off" spellcheck="false" autocapitalize="none" class="font-mono" />
      </template>
    </FormField>

    <FormField id="user-password" :label="t('common.field.password')" :hint="t('databases.hints.password')" :error="errors.password">
      <template #default="field">
        <PasswordInput
          v-bind="field"
          v-model="form.password"
          autocomplete="new-password"
          :placeholder="editing ? t('databases.hints.keep') : t('databases.hints.generate')"
        />
      </template>
    </FormField>

    <FormField
      id="user-hosts"
      :label="t('common.field.hosts')"
      :hint="kind === 'postgresql' ? t('databases.hints.pgHosts') : t('databases.hints.hosts')"
      :error="errors.hosts"
    >
      <template #default="field">
        <TagInput ref="hostsInput" v-bind="field" v-model="form.hosts" :valid="isDbHost" mono placeholder="localhost" />
      </template>
    </FormField>

    <fieldset class="m-0 flex min-w-0 flex-col gap-2 border-0 p-0">
      <legend class="mb-1.5 p-0 text-detail font-medium">{{ t('common.field.grants') }}</legend>
      <p v-if="form.grants.length === 0" class="m-0 text-xs text-muted-foreground">{{ t('databases.grant.none') }}</p>
      <div v-for="(grant, index) in form.grants" :key="index" class="flex items-center gap-2">
        <SelectField
          v-model="grant.database"
          :options="databaseOptions"
          :placeholder="t('common.field.database')"
          :aria-label="t('databases.grant.databaseOf', { index: index + 1 })"
          class="min-w-0 flex-[2_1_0]"
        />
        <SelectField
          :model-value="grant.level"
          :options="levelOptions"
          :aria-label="t('databases.grant.levelOf', { index: index + 1 })"
          class="min-w-0 flex-[1_1_0]"
          @update:model-value="(value: string) => setLevel(grant, value)"
        />
        <Button type="button" variant="ghost" size="icon" class="max-md:size-11" :aria-label="t('databases.grant.remove')" @click="removeGrant(index)">
          <X aria-hidden="true" />
        </Button>
      </div>
      <p v-if="errors.grants" class="m-0 text-xs font-medium text-destructive">{{ errors.grants }}</p>
      <Button type="button" variant="outline" size="sm" class="self-start" :disabled="databaseOptions.length === 0" @click="addGrant">
        <Plus aria-hidden="true" />
        {{ t('databases.grant.add') }}
      </Button>
    </fieldset>

    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
