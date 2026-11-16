<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NForm, NFormItem, NModal, NSelect, NText } from 'naive-ui'
import { isApiError, isStepUpRequired } from '@/api/client'
import { restoreDump, type DbEngine, type DumpInfo } from '@/api/databases'
import type { Job } from '@/api/jobs'
import { errorMessage } from '@/i18n/errors'
import { formatBytes, formatDateTime } from '@/i18n/format'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{
  engine: DbEngine
  dumps: DumpInfo[]
  /** Preselected database and dump file. */
  database?: string | null
  file?: string | null
}>()
const emit = defineEmits<{ started: [job: Job] }>()

const { t } = useI18n()

const database = ref<string | null>(null)
const file = ref<string | null>(null)
const user = ref<string | null>(null)
const submitting = ref(false)
const error = ref<unknown>(null)

watch(show, (open) => {
  if (!open) return
  database.value = props.database ?? null
  file.value = props.file ?? null
  // A dump made of a database restores into it by default.
  if (!database.value && file.value) {
    database.value = props.dumps.find((d) => d.file === file.value)?.database ?? null
  }
  user.value = null
  error.value = null
})

const databaseOptions = computed(() => props.engine.databases.map((db) => ({ value: db.name, label: db.name })))
const fileOptions = computed(() =>
  props.dumps.map((d) => ({
    value: d.file,
    label: `${d.file} (${formatBytes(d.sizeBytes)}, ${formatDateTime(d.createdAt)})`,
  })),
)

/** Users with full access to the database whose password the panel knows. */
const userOptions = computed(() => [
  { value: '', label: t('databases.restore.owner') },
  ...props.engine.users
    .filter((u) => u.hasPassword && u.grants.some((g) => g.database === database.value && g.level === 'all'))
    .map((u) => ({ value: u.name, label: u.name })),
])

const needsUser = computed(() => isApiError(error.value) && error.value.code === 'db.restore_needs_user')

async function submit() {
  if (!database.value || !file.value) return
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    emit('started', await restoreDump(props.engine.id, database.value, file.value, user.value || undefined))
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="t('databases.restore.title')" class="restore-dialog">
    <NText depth="3" tag="p" class="intro">{{ t('databases.restore.intro') }}</NText>
    <NForm :disabled="submitting" :show-feedback="false">
      <NFormItem :label="t('databases.restore.file')" class="item">
        <NSelect v-model:value="file" :options="fileOptions" filterable />
      </NFormItem>
      <NFormItem :label="t('databases.restore.database')" class="item">
        <NSelect v-model:value="database" :options="databaseOptions" filterable />
      </NFormItem>
      <NFormItem :label="t('databases.restore.user')" class="item">
        <div class="field">
          <NSelect :value="user ?? ''" :options="userOptions" @update:value="(v: string) => (user = v || null)" />
          <NText depth="3" class="hint">{{ t('databases.restore.userHint') }}</NText>
        </div>
      </NFormItem>
    </NForm>
    <NAlert type="warning" :show-icon="false" class="block">{{ t('databases.restore.warning') }}</NAlert>
    <NAlert v-if="error" type="error" class="block">
      <div>{{ errorMessage(error) }}</div>
      <div v-if="needsUser" class="hint">{{ t('databases.restore.needsUser') }}</div>
    </NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" :disabled="!database || !file" @click="submit">
        {{ t('databases.restore.submit') }}
      </NButton>
    </div>
  </NModal>
</template>

<style scoped>
.restore-dialog {
  inline-size: min(560px, calc(100vw - 32px));
}

.intro {
  margin-block: 0 12px;
  font-size: 13px;
}

.item {
  margin-block-end: 12px;
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
