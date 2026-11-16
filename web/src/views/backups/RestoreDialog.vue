<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Restores a backup (step-up protected). Site and directory backups are
// extracted into a directory; database backups become a dump of the
// databases page. Encrypted backups need the private key, used for this
// request only and never kept.
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCheckbox, NFormItem, NInput, NModal, NSelect, NText } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { AGE_IDENTITY, isDatabaseBackup, restoreBackup, type BackupFile, type BackupPlan } from '@/api/backups'
import type { Job } from '@/api/jobs'
import { absolutePath, rootLabel } from '@/files/paths'
import { errorMessage, validationMessage } from '@/i18n/errors'
import DirPicker from '@/views/files/DirPicker.vue'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ plan: BackupPlan | null; file: BackupFile | null }>()
const emit = defineEmits<{ started: [job: Job] }>()

const ROOTS = ['sites', 'backups', 'system'] as const

const { t } = useI18n()
const identity = ref('')
const root = ref('sites')
const path = ref('')
const overwrite = ref(false)
const submitting = ref(false)
const error = ref<unknown>(null)
const identityError = ref('')

const database = computed(() => (props.file ? isDatabaseBackup(props.file.name) : false))
const rootOptions = computed(() => ROOTS.map((id) => ({ value: id, label: rootLabel(id) })))

watch(show, (open) => {
  if (!open) return
  identity.value = ''
  identityError.value = ''
  overwrite.value = false
  error.value = null
  // A site's backup goes back to where sites live, into a folder of its own choosing.
  root.value = props.file?.target.startsWith('site-') || props.file?.target.startsWith('dir-sites') ? 'sites' : 'backups'
  path.value = ''
})

// The key is only kept while the dialog is open.
watch(show, (open) => {
  if (!open) identity.value = ''
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
    const job = await restoreBackup(plan.id, {
      name: file.name,
      ...(key ? { identity: key } : {}),
      dest: { root: root.value, path: path.value },
      overwrite: overwrite.value,
    })
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
  <NModal v-model:show="show" preset="card" :title="t('backups.restore.title', { name: file?.name ?? '' })" class="restore-dialog" :mask-closable="false">
    <NAlert v-if="database" type="info" class="block">{{ t('backups.restore.database') }}</NAlert>
    <template v-else>
      <NText depth="3" tag="p" class="intro">{{ t('backups.restore.pick') }}</NText>
      <div class="dest">
        <NSelect v-model:value="root" :options="rootOptions" class="dest__root" @update:value="path = ''" />
        <DirPicker v-model:path="path" :root="root" />
      </div>
      <NCheckbox v-model:checked="overwrite" class="option">{{ t('backups.restore.overwrite') }}</NCheckbox>
    </template>

    <NFormItem
      v-if="file?.encrypted"
      :label="t('backups.restore.identity')"
      :feedback="identityError || t('backups.restore.identityHint')"
      :validation-status="identityError ? 'error' : undefined"
      class="identity"
    >
      <NInput
        v-model:value="identity"
        type="password"
        show-password-on="click"
        class="mono"
        :input-props="{ autocomplete: 'off', spellcheck: false }"
      />
    </NFormItem>

    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="warning" :loading="submitting" @click="submit">
        {{ database ? t('backups.restore.submitDatabase') : t('backups.restore.submit', { dir: absolutePath(root, path) }) }}
      </NButton>
    </div>
  </NModal>
</template>

<style scoped>
.restore-dialog {
  inline-size: min(600px, calc(100vw - 32px));
}

.intro {
  margin-block: 0 8px;
  font-size: 13px;
}

.dest {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.dest__root {
  max-inline-size: 240px;
}

.option {
  margin-block-start: 12px;
}

.identity {
  margin-block-start: 16px;
}

.block {
  margin-block: 0 12px;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}

.mono :deep(input) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
