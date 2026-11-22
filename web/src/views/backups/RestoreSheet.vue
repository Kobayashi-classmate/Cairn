<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isStepUpRequired } from '@/api/client'
import { AGE_IDENTITY, isDatabaseBackup, restoreBackup, type BackupFile, type BackupPlan } from '@/api/backups'
import type { Job } from '@/api/jobs'
import { backupRoots } from '@/backups/targets'
import DirBrowser from '@/components/common/DirBrowser.vue'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import SelectField from '@/components/common/SelectField.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import type { SelectOption } from '@/components/common/types'
import { absolutePath, rootLabel } from '@/files/paths'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { formatBytes } from '@/i18n/format'
import { useBackupsStore } from '@/stores/backups'
import { useSessionStore } from '@/stores/session'

// "Restore" a backup (form sheet, step-up protected). Site and folder
// backups are extracted into a folder; database backups become a dump on
// the databases page. Encrypted backups need the private key, used for this
// request only and never kept. Restoring into the backups or system root
// takes `files.admin`, as opening them in the file manager does.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ plan: BackupPlan | null; file: BackupFile | null }>()
const emit = defineEmits<{ finished: [job: Job] }>()

const { t } = useI18n()
const store = useBackupsStore()
const session = useSessionStore()

const roots = computed(() => backupRoots(session.can('files.admin')))

const identity = ref('')
const root = ref('sites')
const path = ref('')
const overwrite = ref(false)
const submitting = ref(false)
const error = ref<unknown>(null)
const identityError = ref('')

const database = computed(() => (props.file ? isDatabaseBackup(props.file.name) : false))
const rootOptions = computed<SelectOption[]>(() => roots.value.map((id) => ({ value: id, label: rootLabel(id) })))

watch(open, (value) => {
  // The key is only kept while the sheet is open.
  identity.value = ''
  if (!value) return
  identityError.value = ''
  overwrite.value = false
  error.value = null
  // A site's backup goes back to where sites live, into a folder of the user's choosing.
  const site = props.file?.target.startsWith('site-') || props.file?.target.startsWith('dir-sites')
  root.value = site || !roots.value.includes('backups') ? 'sites' : 'backups'
  path.value = ''
})

function onRoot(value: string) {
  root.value = value
  path.value = ''
}

const steps = computed(() => {
  const file = props.file
  if (!file) return []
  const list: string[] = []
  if (file.remote && !file.local) list.push(t('backups.restore.steps.fetch'))
  if (file.encrypted) list.push(t('backups.restore.steps.decrypt'))
  if (database.value) list.push(t('backups.restore.steps.dump'))
  else {
    const dir = absolutePath(root.value, path.value)
    list.push(overwrite.value ? t('backups.restore.steps.extractOverwrite', { dir }) : t('backups.restore.steps.extract', { dir }))
  }
  return list
})

async function submit() {
  const plan = props.plan
  const file = props.file
  if (!plan || !file) return
  const key = identity.value.trim()
  identityError.value = ''
  if (file.encrypted && !key) {
    identityError.value = validationMessage('identity', 'required')
    return
  }
  if (key && !AGE_IDENTITY.test(key)) {
    identityError.value = validationMessage('identity', 'age_identity')
    return
  }
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    await store.runJob(
      () =>
        restoreBackup(plan.id, {
          name: file.name,
          ...(key ? { identity: key } : {}),
          dest: { root: root.value, path: path.value },
          overwrite: overwrite.value,
        }),
      { rethrow: true, onFinished: (done) => emit('finished', done) },
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
    :title="t('backups.restore.title', { name: file?.name ?? '' })"
    :description="file ? formatBytes(file.size) : undefined"
    :submit-label="database ? t('backups.restore.submitDatabase') : t('backups.restore.submitFiles')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <Notice v-if="database" tone="info">{{ t('backups.restore.database') }}</Notice>
    <template v-else>
      <FormField v-if="rootOptions.length > 1" id="restore-root" :label="t('backups.restore.pick')">
        <template #default="field">
          <SelectField v-bind="field" :model-value="root" :options="rootOptions" @update:model-value="onRoot" />
        </template>
      </FormField>
      <DirBrowser v-model:path="path" :root="root" :label="t('backups.restore.pick')" />
      <SwitchField v-model="overwrite" :label="t('backups.restore.overwrite')" />
    </template>

    <FormField
      v-if="file?.encrypted"
      id="restore-identity"
      :label="t('backups.restore.identity')"
      :hint="t('backups.restore.identityHint')"
      :error="identityError"
    >
      <template #default="field">
        <PasswordInput v-bind="field" v-model="identity" autocomplete="off" spellcheck="false" class="font-mono" />
      </template>
    </FormField>

    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
