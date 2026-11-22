<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isApiError, isStepUpRequired } from '@/api/client'
import { fetchDumps, restoreDump, type DbEngine, type DumpInfo } from '@/api/databases'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import SelectField from '@/components/common/SelectField.vue'
import type { SelectOption } from '@/components/common/types'
import { errorMessage } from '@/i18n/errors'
import { formatBytes, formatDateTime } from '@/i18n/format'
import { useDatabasesStore } from '@/stores/databases'

// "Restore a dump" (form sheet, step-up protected): loads a dump into an
// existing database, as its owner or a user with full access to it.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{
  engine: DbEngine
  /** Preselected database and dump file. */
  database?: string
  file?: string
}>()

const { t } = useI18n()
const store = useDatabasesStore()

const dumps = ref<DumpInfo[]>([])
const database = ref('')
const file = ref('')
const user = ref('')
const submitting = ref(false)
const error = ref<unknown>(null)
const loadError = ref<unknown>(null)

watch(open, async (value) => {
  if (!value) return
  database.value = props.database ?? ''
  file.value = props.file ?? ''
  user.value = ''
  error.value = null
  loadError.value = null
  try {
    dumps.value = await fetchDumps(props.engine.id)
  } catch (err) {
    loadError.value = err
  }
  // A dump made of a database restores into it by default; a database takes its newest dump.
  if (!database.value && file.value) database.value = dumps.value.find((d) => d.file === file.value)?.database ?? ''
  if (!file.value && database.value) {
    const newest = dumps.value.filter((d) => d.database === database.value).sort((a, b) => b.createdAt - a.createdAt)[0]
    file.value = newest?.file ?? ''
  }
})

// Only users with full access to the chosen database can restore into it.
watch(database, () => (user.value = ''))

const databaseOptions = computed<SelectOption[]>(() =>
  props.engine.databases.filter((db) => !db.missing).map((db) => ({ value: db.name, label: db.name })),
)
const fileOptions = computed<SelectOption[]>(() =>
  [...dumps.value]
    .sort((a, b) => b.createdAt - a.createdAt)
    .map((d) => ({ value: d.file, label: `${d.file} (${formatBytes(d.sizeBytes)}, ${formatDateTime(d.createdAt)})` })),
)

/** Users with full access to the database whose password the panel knows. */
const userOptions = computed<SelectOption[]>(() => [
  { value: '', label: t('databases.restore.owner') },
  ...props.engine.users
    .filter((u) => u.hasPassword && u.grants.some((g) => g.database === database.value && g.level === 'all'))
    .map((u) => ({ value: u.name, label: u.name })),
])

const needsUser = computed(() => isApiError(error.value) && error.value.code === 'db.restore_needs_user')

const steps = computed(() =>
  database.value && file.value
    ? [
        t('databases.restore.steps.load', { file: file.value, database: database.value }),
        user.value ? t('databases.restore.steps.as', { user: user.value }) : t('databases.restore.steps.asOwner'),
      ]
    : [],
)

async function submit() {
  if (!database.value || !file.value) return
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    await store.runJob(() => restoreDump(props.engine.id, database.value, file.value, user.value || undefined), { rethrow: true })
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
    :title="t('databases.restore.title')"
    :description="t('databases.restore.intro')"
    :submit-label="t('databases.restore.submit')"
    :submitting="submitting"
    :submit-disabled="!database || !file"
    :steps="steps"
    @submit="submit"
  >
    <Notice v-if="loadError" tone="destructive">{{ errorMessage(loadError) }}</Notice>
    <FormField id="restore-file" :label="t('databases.restore.file')">
      <template #default="field">
        <SelectField v-bind="field" v-model="file" :options="fileOptions" :placeholder="t('databases.restore.pickFile')" />
      </template>
    </FormField>
    <FormField id="restore-database" :label="t('common.field.database')">
      <template #default="field">
        <SelectField v-bind="field" v-model="database" :options="databaseOptions" :placeholder="t('databases.restore.pickDatabase')" />
      </template>
    </FormField>
    <FormField id="restore-user" :label="t('databases.restore.user')" :hint="t('databases.restore.userHint')">
      <template #default="field">
        <SelectField v-bind="field" v-model="user" :options="userOptions" />
      </template>
    </FormField>
    <Notice tone="warning">{{ t('databases.restore.warning') }}</Notice>
    <Notice v-if="error" tone="destructive">
      {{ errorMessage(error) }}
      <span v-if="needsUser" class="mt-1 block font-normal">{{ t('databases.restore.needsUser') }}</span>
    </Notice>
  </FormSheet>
</template>
